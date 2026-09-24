package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func newCancelCollection(t *testing.T) (*Catalog, *persistence.Store, api.Operation, time.Time) {
	t.Helper()
	c, store := testCatalog(t)
	at := time.Now().UTC()
	p := collectionAdmissionRequest()
	ticket, err := c.PrepareCollection(context.Background(), p, "owner", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.CreateCollection(context.Background(), collectionCreateRequest(p, ticket.Ticket), "owner", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	return c, store, op, at
}

func cleanupCanceledCollection(t *testing.T, store *persistence.Store, id string, at time.Time) {
	t.Helper()
	head, exists, err := store.CollectionGet(id)
	if err != nil || !exists {
		t.Fatal("read cleanup header", err)
	}
	fence := persistence.CollectionCleanup{Uploaded: head.Uploaded, EncodedBytes: head.EncodedBytes,
		RemovedRows: head.RemovedRows, RemovedBytes: head.RemovedBytes, ActivityAt: head.ActivityAt}
	if p := head.Plan; p != nil {
		fence.Plan = &persistence.CollectionPlanCleanup{PlanID: p.Header.PlanID, UploadedFragments: p.UploadedFragments,
			EncodedBytes: p.EncodedBytes, ArtifactBytes: p.ArtifactBytes, ProgressDigest: p.ProgressDigest,
			RemovedFragments: p.RemovedFragments, RemovedBytes: p.RemovedBytes, FinalizedAt: p.FinalizedAt}
	}
	if v := head.Validation; v != nil {
		fence.Validation = &persistence.CollectionValidationCleanup{ResultID: v.Header.ResultID, Uploaded: v.Uploaded,
			EncodedBytes: v.EncodedBytes, ResultBytes: v.ResultBytes, ProgressDigest: v.ProgressDigest,
			RemovedRows: v.RemovedRows, RemovedBytes: v.RemovedBytes, FinalizedAt: v.FinalizedAt,
			Published: v.Published, HistorySealed: v.HistorySealed, HistoryExpiredAt: v.HistoryExpiredAt}
	}
	result, err := store.Submit(context.Background(), []persistence.Command{{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{
		Action: "cleanup", OperationID: id, UploadID: head.UploadID, Cleanup: &fence}}})
	if err != nil || len(result) != 1 || result[0].Err != nil {
		t.Fatal("cleanup", err, result)
	}
}

func TestCollectionCancelOriginalReceiptSurvivesCleanupWithoutAnotherWrite(t *testing.T) {
	c, store, op, at := newCancelCollection(t)
	ctx := context.Background()
	// Cancellation operates on protected identity metadata, not provider secrets.
	c.sealer = nil
	result, err := c.CancelCollection(ctx, op.ID, "owner", collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || result.ID != op.ID || result.State != "canceled" || result.Committed == nil || *result.Committed != 0 {
		t.Fatal("cancel", result.ID, result.State, err)
	}
	header, _, _ := store.CollectionGet(op.ID)
	cancellation := *header.Cancellation
	cleanupCanceledCollection(t, store, op.ID, at.Add(2*time.Second))
	if _, exists, _ := store.CollectionGet(op.ID); exists {
		t.Fatal("cleanup retained an empty header")
	}
	index := store.Status().CommittedIndex
	again, err := c.CancelCollection(ctx, op.ID, "owner", collectionClock(at.Add(29*24*time.Hour)), func(func() error) error {
		t.Error("terminal retry attempted admission")
		return errors.New("unexpected admission")
	})
	if err != nil || !reflect.DeepEqual(result, again) || store.Status().CommittedIndex != index {
		t.Fatal("retry changed original receipt or committed new work", err)
	}
	receipt, err := store.CollectionReceipt(ctx, op.ID, at.Add(2*time.Second))
	if err != nil || receipt.CancellationID != cancellation.ID || !receipt.TerminalAt.Equal(cancellation.At) {
		t.Fatal("cancellation identity changed", err)
	}
	events, err := store.History().Page("collection/"+op.ID, "", 100)
	if err != nil || len(events.Events) != 1 || events.Events[0].Type != "collection_canceled" {
		t.Fatal("retry duplicated terminal evidence", err)
	}
	if _, err := c.CancelCollection(ctx, op.ID, "owner", collectionClock(at.Add(31*24*time.Hour)), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("expired retained handle was revived", err)
	}
}

func TestCollectionCancelConcurrentRequestsCommitOnce(t *testing.T) {
	c, store, op, at := newCancelCollection(t)
	index := store.Status().CommittedIndex
	const callers = 12
	results := make(chan api.Operation, callers)
	failures := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := c.CancelCollection(context.Background(), op.ID, "owner", collectionClock(at.Add(time.Second)), allowCollectionCommit)
			results <- result
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.ID != op.ID || result.State != "canceled" {
			t.Fatal("concurrent request lost original result")
		}
	}
	if store.Status().CommittedIndex != index+1 {
		t.Fatal("concurrent cancellation submitted duplicate commands")
	}
}

func TestCollectionCancelAuthorizationContextAndDeadlineFences(t *testing.T) {
	c, store, op, at := newCancelCollection(t)
	index := store.Status().CommittedIndex
	denied := errors.New("fixture authorization revoked")
	if _, err := c.CancelCollection(context.Background(), op.ID, "owner", collectionClock(at.Add(time.Second)), func(func() error) error { return denied }); !errors.Is(err, denied) {
		t.Fatal("admission denial ignored", err)
	}
	if result, err := c.CancelCollection(context.Background(), op.ID, "another", collectionClock(at.Add(time.Second)), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationNotFound) || result.ID != "" {
		t.Fatal("another owner learned or canceled operation", err)
	}
	if _, err := c.CancelCollection(context.Background(), op.ID, "owner", collectionClock(at.Add(24*time.Hour)), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("expired inactive upload was canceled", err)
	}
	c.collectionCancel <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.CancelCollection(ctx, op.ID, "owner", collectionClock(at), allowCollectionCommit)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("blocked semaphore wait ignored cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked semaphore wait did not stop")
	}
	<-c.collectionCancel
	if store.Status().CommittedIndex != index {
		t.Fatal("failed admission or deadline changed storage")
	}
}

func TestCollectionCancelReconcilesAnotherCatalogWinner(t *testing.T) {
	c, store, op, at := newCancelCollection(t)
	other, err := NewCatalog(store, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := store.Status().CommittedIndex
	result, err := c.CancelCollection(context.Background(), op.ID, "owner", collectionClock(at.Add(time.Second)), func(commit func() error) error {
		winner, err := other.CancelCollection(context.Background(), op.ID, "owner", collectionClock(at.Add(time.Second)), allowCollectionCommit)
		if err != nil || winner.State != "canceled" {
			return errors.New("second catalog cancellation failed")
		}
		return commit()
	})
	if err != nil || result.State != "canceled" || result.ID != op.ID || store.Status().CommittedIndex != before+1 {
		t.Fatal("second catalog winner was not reconciled without another submit", err)
	}
}

// Build actual committed inactive states, including the distinction between a
// structural plan, an unsealed verdict and its published history observation.
func collectionCancelState(t *testing.T, stage string) (*Catalog, *persistence.Store, persistence.CollectionState) {
	t.Helper()
	ctx := context.Background()
	c, store := testCatalog(t)
	at := time.Now().UTC()
	if _, err := store.CommitAuthentication(ctx, collectionOwnerBootstrap(at)); err != nil {
		t.Fatal(err)
	}
	request, item := collectionOwnerInput(t)
	ticket, err := c.PrepareCollection(ctx, request, "team/operator", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.CreateCollection(ctx, collectionCreateRequest(request, ticket.Ticket), "team/operator", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadCollection(ctx, op.ID, "team/operator", []CollectionUploadItem{item}, func(persistence.CatalogKey) bool { return true }, collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	head, _, err := store.CollectionGet(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	submit := func(command persistence.CollectionCommand) {
		at = at.Add(time.Millisecond)
		command.OperationID, command.UploadID = head.ID, head.UploadID
		head = submitStagedSource(t, store, command, at)
	}
	valid := stage == "validated" || stage == "unsealed-success"
	if valid || stage == "plan-validating" {
		source, err := newCollectionValidationSource(ctx, c, head.ID, func(persistence.CatalogKey) bool { return true }, collectionClock(at))
		if err != nil {
			t.Fatal(err)
		}
		view, err := c.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		result, plan, err := c.compileStagedCollectionPlan(ctx, view, source, collectionReadAll)
		source.close()
		if err != nil || !result.Valid {
			t.Fatal("compile cancellation fixture", err)
		}
		artifact, err := prepareCollectionPlanArtifact(ctx, plan, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		if _, err := artifact.writeTo(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		submit(persistence.CollectionCommand{Action: "plan_begin", PlanBegin: &persistence.CollectionPlanBegin{Header: artifact.header, Descriptor: artifact.descriptor}})
		if stage == "plan-validating" {
			return c, store, head
		}
		var ordinal uint64
		if _, err := persistence.DecodeCollectionPlan(ctx, bytes.NewReader(raw.Bytes()), func(fragment persistence.CollectionPlanFragment) error {
			ordinal++
			submit(persistence.CollectionCommand{Action: "plan_append", PlanID: artifact.header.PlanID,
				PlanFragment: &persistence.CollectionPlanLedgerFragment{Ordinal: ordinal, Fragment: fragment}})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		proof, err := store.VerifyCollectionPlan(ctx, head.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		submit(persistence.CollectionCommand{Action: "plan_finalize", PlanFinalize: &proof})
	}
	authority, err := store.ObserveOperatorAuthority(ctx, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	verdict := persistence.CollectionValidationItem{Ordinal: item.Ordinal, Key: item.Key, Source: item.Source,
		Document: item.SourceDocument, Item: item.SourceItem, Change: "create"}
	begin := persistence.CollectionValidationBegin{Header: persistence.CollectionValidationHeader{ResultID: uuid.NewString(),
		OperationID: head.ID, UploadID: head.UploadID, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), Valid: valid}}
	if valid {
		begin.Header.PlanID, begin.Header.PlanDigest = head.Plan.Header.PlanID, head.Plan.Descriptor.Digest
	} else {
		// An internal deterministic rejection fixture; this does not claim the
		// credential resource itself fails the production graph compiler.
		begin.Header.Issue, verdict.Change, verdict.Issue = "invalidResource", "", "invalidResource"
	}
	digest, size, err := persistence.CollectionValidationNextDigest(persistence.CollectionValidationInitialDigest(), verdict)
	if err != nil {
		t.Fatal(err)
	}
	begin.Descriptor = persistence.CollectionValidationDescriptor{Count: 1, Bytes: size, Digest: digest}
	submit(persistence.CollectionCommand{Action: "validation_begin", ValidationBegin: &begin})
	submit(persistence.CollectionCommand{Action: "validation_append", ValidationID: begin.Header.ResultID, ValidationItems: []persistence.CollectionValidationItem{verdict}})
	if stage == "result-validating" {
		return c, store, head
	}
	submit(persistence.CollectionCommand{Action: "validation_finalize", ValidationID: begin.Header.ResultID})
	if stage == "validated" || stage == "rejected" {
		submit(persistence.CollectionCommand{Action: "validation_publish", ValidationID: begin.Header.ResultID})
	}
	return c, store, head
}

func TestCollectionCancelPreservesInactivePlansAndResults(t *testing.T) {
	for _, stage := range []string{"plan-validating", "result-validating", "unsealed-success", "unsealed-rejection", "validated", "rejected"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			c, store, original := collectionCancelState(t, stage)
			at := original.ActivityAt.Add(time.Second)
			receipt, err := store.CollectionReceipt(ctx, original.ID, at)
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := "validating"
			if stage == "validated" || stage == "rejected" {
				wantPhase = stage
			}
			if receipt.Phase != wantPhase {
				t.Fatal("fixture did not reach expected observed state", receipt.Phase)
			}
			observed := collectionOperationView(receipt)
			if stage == "validated" || stage == "rejected" {
				if observed.Validated == nil || *observed.Validated != (stage == "validated") {
					t.Fatal("sealed validation verdict lost before cancellation")
				}
			} else if observed.Validated != nil {
				t.Fatal("provisional state fabricated a validation verdict")
			}
			index := store.Status().CommittedIndex
			c.sealer = nil // Cancellation must not decrypt or reseal provider input.
			result, err := c.CancelCollection(ctx, original.ID, original.Actor, collectionClock(at), allowCollectionCommit)
			if err != nil || result.State != "canceled" || result.ID != original.ID ||
				result.Committed == nil || *result.Committed != 0 || result.Applied == nil || *result.Applied != 0 {
				t.Fatal("inactive cancellation failed or claimed application", err)
			}
			// Cancellation is its own terminal outcome, not a negative verdict.
			// Any finalized original result remains separately retained below.
			if result.Validated != nil {
				t.Fatal("cancellation fabricated or replaced a validation verdict")
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(encoded, &fields) != nil {
				t.Fatal("invalid cancellation response JSON")
			}
			if _, present := fields["validated"]; present {
				t.Fatal("cancellation serialized an absent validation verdict")
			}
			head, exists, err := store.CollectionGet(original.ID)
			if err != nil || !exists || store.Status().CommittedIndex != index+1 || head.Cancellation == nil {
				t.Fatal("cancellation did not commit one original outcome", err)
			}
			want := original.Clone()
			want.Phase, want.TerminalAt, want.Cancellation = "canceled", at, head.Cancellation
			if !reflect.DeepEqual(head, want) {
				t.Fatal("cancellation rewrote original encrypted input, plan, result or lifetime")
			}
			cancellation := *head.Cancellation
			if head.Validation != nil && !head.Validation.FinalizedAt.IsZero() {
				retained, err := store.CollectionReceipt(ctx, head.ID, at)
				if err != nil || retained.Validation == nil || retained.Validation.Header != original.Validation.Header || retained.Validation.Descriptor != original.Validation.Descriptor || !retained.Validation.FinalizedAt.Equal(original.Validation.FinalizedAt) {
					t.Fatal("cancellation lost immutable finalized verdict", err)
				}
				if !head.Validation.HistorySealed {
					if _, err := store.CollectionValidationPage(ctx, head.ID, 0, 100, at); !errors.Is(err, persistence.ErrHistoryUnavailable) {
						t.Fatal("cancellation fabricated unsealed history", err)
					}
					head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID,
						UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, at)
				}
			}
			for pages := 0; pages < 4; pages++ {
				if _, exists, err := store.CollectionGet(head.ID); err != nil {
					if !exists && errors.Is(err, persistence.ErrOperationExpired) {
						break // The retired header is replaced by its retained receipt.
					}
					t.Fatal(err)
				} else if !exists {
					break
				}
				cleanupCanceledCollection(t, store, head.ID, at)
			}
			if _, exists, _ := store.CollectionGet(head.ID); exists {
				t.Fatal("bounded cleanup retained inactive header")
			}
			index = store.Status().CommittedIndex
			again, err := c.CancelCollection(ctx, head.ID, head.Actor, collectionClock(at.Add(29*24*time.Hour)), func(func() error) error {
				t.Error("retained cancellation retried admission")
				return errors.New("unexpected admission")
			})
			if err != nil || !reflect.DeepEqual(again, result) || store.Status().CommittedIndex != index {
				t.Fatal("retry replaced original result or submitted work", err)
			}
			retained, err := store.CollectionReceipt(ctx, head.ID, at)
			if err != nil || retained.CancellationID != cancellation.ID || !retained.TerminalAt.Equal(cancellation.At) {
				t.Fatal("cleanup lost cancellation identity", err)
			}
			if original.Validation != nil && !original.Validation.FinalizedAt.IsZero() {
				page, err := store.CollectionValidationPage(ctx, head.ID, 0, 100, at.Add(29*24*time.Hour))
				if err != nil || len(page.Items) != 1 || page.Receipt.Header != original.Validation.Header || page.Receipt.Descriptor != original.Validation.Descriptor {
					t.Fatal("cleanup lost retained original validation result", err)
				}
			}
			events, err := store.History().Page("collection/"+head.ID, "", 100)
			if err != nil || len(events.Events) != 1 || events.Events[0].Type != "collection_canceled" {
				t.Fatal("repeated cancellation duplicated terminal audit", err)
			}
			active, err := store.CatalogSnapshot()
			if err != nil || active.Len() != 0 {
				t.Fatal("inactive cancellation activated resources", err)
			}
		})
	}
}

func TestCollectionCancelInactiveStateAdmissionFences(t *testing.T) {
	for _, stage := range []string{"result-validating", "validated", "rejected"} {
		t.Run(stage, func(t *testing.T) {
			c, store, original := collectionCancelState(t, stage)
			ctx := context.Background()
			at := original.ActivityAt.Add(time.Second)
			before := store.Status().CommittedIndex
			if result, err := c.CancelCollection(ctx, original.ID, "another", collectionClock(at), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationNotFound) || result.ID != "" {
				t.Fatal("different owner could inspect or cancel result", err)
			}
			denied := errors.New("revoked cancellation admission")
			if _, err := c.CancelCollection(ctx, original.ID, original.Actor, collectionClock(at), func(func() error) error { return denied }); !errors.Is(err, denied) {
				t.Fatal("inactive state bypassed authorization admission", err)
			}
			if _, err := c.CancelCollection(ctx, original.ID, original.Actor, collectionClock(original.ExpiresAt), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationExpired) {
				t.Fatal("expired inactive state acquired a new cancellation", err)
			}
			head, exists, err := store.CollectionGet(original.ID)
			if err != nil || !exists || !reflect.DeepEqual(head, original) || store.Status().CommittedIndex != before {
				t.Fatal("failed cancellation changed original state", err)
			}
		})
	}
}
