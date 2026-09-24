package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func requestReceiptInput(t *testing.T, s *Store, count int) CollectionState {
	t.Helper()
	head := validationHistoryCrashInput(t, s, count)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Millisecond)
	request := CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), RequestedAt: at}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: head.ID,
		UploadID: head.UploadID, ValidationRequest: &request}, at))
	at = head.ActivityAt.Add(time.Millisecond)
	claim := CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}
	return validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_claim", OperationID: head.ID,
		UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationClaim: &claim}, at))
}

func requestReceiptInterrupt(head CollectionState, at time.Time) CollectionCommand {
	progress := collectionCleanupFor(head)
	return CollectionCommand{Action: "validation_interrupt", OperationID: head.ID, UploadID: head.UploadID,
		ValidationFence: CollectionValidationRequestFenceFor(head), ValidationProgress: &progress,
		ValidationInterruption: &CollectionValidationInterruption{ID: uuid.NewString(), Reason: "coordinatorRestarted", At: at}}
}

func requestReceiptPartialPlan(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	begin, parts := planApplyArtifact(t, s, head)
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "plan_begin", OperationID: head.ID,
		UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), PlanBegin: &begin}, head.ActivityAt.Add(time.Millisecond)))
	for _, part := range parts[:2] { // Header plus a row start: intentionally incomplete.
		head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "plan_append", OperationID: head.ID,
			UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), PlanID: head.Plan.Header.PlanID,
			PlanFragment: &part}, head.ActivityAt.Add(time.Millisecond)))
	}
	return head
}

func requestReceiptResult(t *testing.T, s *Store, head CollectionState, count int) CollectionState {
	t.Helper()
	head, begin, items := validationApplyIntent(t, s, head, head.ValidationRequest.Authority, false)
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_begin", OperationID: head.ID,
		UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationBegin: &begin}, head.ActivityAt.Add(time.Millisecond)))
	for start := 0; start < count; start += 256 {
		head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_append", OperationID: head.ID,
			UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationID: head.Validation.Header.ResultID,
			ValidationItems: items[start:min(start+256, count)]}, head.ActivityAt.Add(time.Millisecond)))
	}
	return head
}

func TestCollectionValidationRequestReceiptCleanupRestartRetention(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, stage := range []string{"claimed", "partial-plan", "partial-result"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, stage), func(t *testing.T) {
				var s *Store
				if disk {
					s = validationExpiryDiskStore(t)
				} else {
					s = openCatalogMemory(t)
				}
				head := requestReceiptInput(t, s, 300)
				switch stage {
				case "partial-plan":
					head = requestReceiptPartialPlan(t, s, head)
				case "partial-result":
					head = requestReceiptResult(t, s, head, 256)
				}
				before := head.Clone()
				at := head.ActivityAt.Add(time.Second)
				command := requestReceiptInterrupt(head, at)
				result := collectionCommand(t, s, command, at)
				head = validationApplyAllowed(t, result)
				if head.Phase != "interrupted" || len(result.Events) != 1 || !reflect.DeepEqual(head.Plan, before.Plan) || !reflect.DeepEqual(head.Validation, before.Validation) ||
					!head.ActivityAt.Equal(before.ActivityAt) || !head.ExpiresAt.Equal(before.ExpiresAt) {
					t.Fatal("interruption rewrote original staged artifacts or time")
				}
				original, err := s.CollectionReceipt(context.Background(), head.ID, at)
				if err != nil || original.Phase != "interrupted" || original.Validation != nil || original.ValidationRequest == nil ||
					original.ValidationRequest.Interruption.ID != command.ValidationInterruption.ID {
					t.Fatal("interruption receipt missing or claims finalized verdict", err)
				}
				if err := s.maintainCollections(at.Add(-time.Nanosecond)); err != nil {
					t.Fatal(err)
				}
				planApplyStateUnchanged(t, s, head)
				stale := cleanupCommand(head)
				stale.Cleanup.ValidationRequest.RequestID = uuid.NewString()
				if r := collectionCommand(t, s, stale, at); !errors.Is(r.Err, ErrCollectionConflict) {
					t.Fatal("cleanup accepted different validation request", r.Err)
				}
				for step := 0; step < 5; step++ {
					current, ok, err := s.CollectionGet(head.ID)
					if errors.Is(err, ErrOperationExpired) && !ok {
						break
					}
					if err != nil || !ok {
						t.Fatal(err)
					}
					r := collectionCommand(t, s, cleanupCommand(current), at)
					next := validationApplyAllowed(t, r)
					if len(r.Events) != 0 || next.RemovedRows-current.RemovedRows > 256 || !collectionValidationRequestsEqual(next.ValidationRequest, head.ValidationRequest) {
						t.Fatal("cleanup exceeded input bound or rewrote interruption")
					}
					if next.Plan != nil && next.Plan.RemovedFragments-current.Plan.RemovedFragments > 256 ||
						next.Validation != nil && next.Validation.RemovedRows-current.Validation.RemovedRows > 256 {
						t.Fatal("cleanup exceeded artifact page bound")
					}
					if _, ok, _ := s.CollectionGet(head.ID); ok {
						retry := collectionCommand(t, s, command, at)
						if retry.Err != nil || len(retry.Events) != 0 {
							t.Fatal("lost interruption reply failed after cleanup advanced", retry.Err)
						}
					}
				}
				if _, ok, _ := s.CollectionGet(head.ID); ok {
					t.Fatal("bounded cleanup did not retire header")
				}
				if used, err := s.fsm.collections.Bytes(); err != nil || used != 0 {
					t.Fatal("cleanup retained logical staging bytes", used, err)
				}
				if disk {
					config := s.config
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					reopened, err := Open(context.Background(), config)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reopened.Close() })
					s = reopened
				}
				receipt, err := s.CollectionReceipt(context.Background(), head.ID, at.AddDate(0, 0, 29))
				if err != nil || !collectionReceiptsEqual(receipt, original) {
					t.Fatal("cleanup/restart lost original interruption receipt", err)
				}
				page, err := s.History().Page("collection/"+head.ID, "", 100)
				if err != nil || len(page.Events) != 1 || page.Events[0].Type != "collection_interrupted" ||
					page.Events[0].Reason != "coordinatorRestarted" || page.Events[0].ActionID != command.ValidationInterruption.ID {
					t.Fatal("interruption audit missing or repeated", err)
				}
				raw, err := json.Marshal(page.Events[0])
				if err != nil || len(raw) > maxCollectionReceiptEventBytes {
					t.Fatal("receipt exceeds retained event bound", err)
				}
				for _, forbidden := range []string{"ciphertext", "wrapped_key", "private-inventory", "never-write-this-collection-plaintext"} {
					if bytes.Contains(raw, []byte(forbidden)) {
						t.Fatal("private staging value entered retained receipt")
					}
				}
				if err := s.History().Expire(at.AddDate(0, 0, 31)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CollectionReceipt(context.Background(), head.ID, at); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("backward clock revived expired interruption", err)
				}
				catalog, err := s.CatalogSnapshot()
				if err != nil || catalog.Len() != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 {
					t.Fatal("interruption or cleanup admitted active resources", err)
				}
			})
		}
	}
}

func TestCollectionValidationRequestReceiptClonesAndValidates(t *testing.T) {
	s := openCatalogMemory(t)
	head := requestReceiptInput(t, s, 1)
	at := head.ActivityAt.Add(time.Second)
	command := requestReceiptInterrupt(head, at)
	head = validationApplyAllowed(t, collectionCommand(t, s, command, at))
	receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	original := receipt.Clone()
	receipt.ValidationRequest.Claim.ID = uuid.NewString()
	receipt.ValidationRequest.Interruption.Reason = "changed-by-caller"
	fresh, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || !collectionReceiptsEqual(fresh, original) {
		t.Fatal("receipt leaked mutable request metadata", err)
	}
	event := collectionReceiptEvent(original)
	copy := event.Clone()
	copy.Collection.ValidationRequest.Claim.RunID = uuid.NewString()
	copy.Collection.ValidationRequest.Interruption.ID = uuid.NewString()
	if !collectionReceiptsEqual(*event.Collection, original) {
		t.Fatal("event clone aliases nested request")
	}
	zone := time.FixedZone("receipt-fixture", 19800)
	equal := original.Clone()
	equal.ValidationRequest.RequestedAt = equal.ValidationRequest.RequestedAt.In(zone)
	equal.ValidationRequest.Claim.At = equal.ValidationRequest.Claim.At.In(zone)
	equal.ValidationRequest.Interruption.At = equal.ValidationRequest.Interruption.At.In(zone)
	if !collectionReceiptsEqual(equal, original) {
		t.Fatal("equivalent persisted instants changed receipt identity")
	}
	for name, mutate := range map[string]func(*CollectionReceipt){
		"missing-request":         func(r *CollectionReceipt) { r.ValidationRequest = nil },
		"missing-interruption":    func(r *CollectionReceipt) { r.ValidationRequest.Interruption = nil },
		"wrong-actor":             func(r *CollectionReceipt) { r.ValidationRequest.Authority.Actor = "another-operator" },
		"wrong-count":             func(r *CollectionReceipt) { r.ValidationRequest.ItemCount++ },
		"unknown-reason":          func(r *CollectionReceipt) { r.ValidationRequest.Interruption.Reason = "private provider diagnostic" },
		"different-terminal-time": func(r *CollectionReceipt) { r.ValidationRequest.Interruption.At = r.TerminalAt.Add(time.Second) },
		"after-inactivity": func(r *CollectionReceipt) {
			r.TerminalAt = r.ExpiresAt
			r.ValidationRequest.Interruption.At = r.TerminalAt
		},
		"different-terminal-phase": func(r *CollectionReceipt) { r.Phase = "canceled"; r.CancellationID = uuid.NewString() },
	} {
		t.Run(name, func(t *testing.T) {
			bad := original.Clone()
			mutate(&bad)
			if bad.validate() == nil {
				t.Fatal("malformed request receipt accepted")
			}
		})
	}
}

func TestCollectionValidationRequestReceiptPreservesOtherTerminalOutcomes(t *testing.T) {
	for _, terminal := range []string{"canceled", "finalized"} {
		t.Run(terminal, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := requestReceiptInput(t, s, 3)
			if terminal == "canceled" {
				cancel := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
				head = validationApplyAllowed(t, collectionCommand(t, s, cancel, cancel.Cancel.At))
			} else {
				head = requestReceiptResult(t, s, head, 3)
				head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_finalize", OperationID: head.ID,
					UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationID: head.Validation.Header.ResultID}, head.ActivityAt.Add(time.Millisecond)))
				head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
			}
			before, err := s.CollectionReceipt(context.Background(), head.ID, head.ActivityAt.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			at := head.ActivityAt.Add(2 * time.Second)
			if result := collectionCommand(t, s, requestReceiptInterrupt(head, at), at); !errors.Is(result.Err, ErrCollectionConflict) {
				t.Fatal("interruption rewrote existing disposition", result.Err)
			}
			planApplyStateUnchanged(t, s, head)
			after, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || !collectionReceiptsEqual(after, before) {
				t.Fatal("rejected interruption changed retained outcome", err)
			}
		})
	}
}
