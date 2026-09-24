package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func activationFixture(t *testing.T, s *Store) (CollectionState, OperatorAuthority) {
	t.Helper()
	head, _ := validationPublishFixture(t, s, 2, true)
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	return head, authority
}

func activationCommand(head CollectionState, authority OperatorAuthority, at time.Time) CollectionCommand {
	return CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, ActivationAuthority: &authority,
		Activation: &CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest,
			ItemCount: head.ItemCount, ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor,
			PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest,
			ValidationRequest: CollectionValidationRequestFenceFor(head), At: at}}
}

func activationCancel(head CollectionState, authority OperatorAuthority, at time.Time) CollectionCommand {
	return CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID,
		Cancel:              &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at},
		ActivationAuthority: &authority, ActivationFence: CollectionActivationFenceFor(head)}
}

func TestCollectionActivationImmutableAdmissionAndNoExecution(t *testing.T) {
	s := openCatalogMemory(t)
	original, authority := activationFixture(t, s)
	beforeInput, beforePlan, beforeResult := validationLedgerStreams(t, s.fsm.collections)
	at := original.ActivityAt.Add(time.Second)
	c := activationCommand(original, authority, at)
	r := collectionCommand(t, s, c, at)
	head := validationApplyAllowed(t, r)
	if head.Phase != "applying" || !collectionActivationsEqual(head.Activation, c.Activation) ||
		!head.ActivityAt.Equal(original.ActivityAt) || !head.ExpiresAt.Equal(original.ExpiresAt) || !head.TerminalAt.IsZero() ||
		len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.image.OperationReservations) != 0 ||
		s.fsm.image.Version != CollectionActivationFormatVersion {
		t.Fatal("admission altered original input, execution state or deadline")
	}
	if len(r.Events) != 1 || r.Events[0].MonitorID != "collection-activation/"+head.ID || r.Events[0].Collection != nil ||
		r.Events[0].Actor != head.Actor || r.Events[0].ActionID != c.Activation.ID || !r.Events[0].At.Equal(at) {
		t.Fatal("admission audit is absent or enters terminal receipt namespace")
	}
	audit, _ := json.Marshal(r.Events)
	for _, private := range []string{head.ProgressDigest, head.ContentDigest, c.Activation.CapabilitiesDigest, authenticationToken} {
		if bytes.Contains(audit, []byte(private)) {
			t.Fatal("admission audit exposes protected metadata")
		}
	}
	retry := collectionCommand(t, s, c, at.Add(time.Second))
	if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(*retry.Collection, head) {
		t.Fatal("exact retry changed original admission", retry.Err)
	}
	conflict := c
	a := c.Activation.Clone()
	a.ID = uuid.NewString()
	conflict.Activation = &a
	if r := collectionCommand(t, s, conflict, at.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("new identity replaced activation", r.Err)
	}
	clone := head.Clone()
	clone.Activation.PlanDescriptor.Digest = strings.Repeat("d", 64)
	planApplyStateUnchanged(t, s, head)
	input, plan, result := validationLedgerStreams(t, s.fsm.collections)
	if !bytes.Equal(input, beforeInput) || !bytes.Equal(plan, beforePlan) || !bytes.Equal(result, beforeResult) {
		t.Fatal("admission changed ledger namespaces")
	}
}

func TestCollectionActivationAuthorityRotationAndRetry(t *testing.T) {
	s := openCatalogMemory(t)
	head, originalAuthority := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	c := activationCommand(head, originalAuthority, at)
	head = validationApplyAllowed(t, collectionCommand(t, s, c, at))
	policy, _ := s.Authentication()
	replace := lifecycleReplacement(policy)
	replace.At = at.Add(time.Second)
	if r := lifecycleCommand(t, s, CollectionActivationFormatVersion, replace); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := collectionCommand(t, s, c, replace.At); !errors.Is(r.Err, ErrAuthenticationConflict) {
		t.Fatal("stale authority reconciled admission", r.Err)
	}
	fresh, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, replace.At)
	if err != nil {
		t.Fatal(err)
	}
	c.ActivationAuthority = &fresh
	r := collectionCommand(t, s, c, replace.At)
	if r.Err != nil || !reflect.DeepEqual(*r.Collection, head) || r.Collection.Activation.Authority != originalAuthority || len(r.Events) != 0 {
		t.Fatal("fresh authority rewrote original admission", r.Err)
	}
	policy, _ = s.Authentication()
	revoke := lifecycleReplacement(policy)
	revoke.At = replace.At.Add(time.Second)
	for i := range revoke.Principals {
		if revoke.Principals[i].ID == head.Actor {
			revoke.Principals[i].Revoked = true
		}
	}
	if r := lifecycleCommand(t, s, CollectionActivationFormatVersion, revoke); r.Err != nil {
		t.Fatal(r.Err)
	}
	denied := OperatorAuthority{Epoch: revoke.Epoch, Revision: revoke.Revision, Actor: head.Actor}
	c.ActivationAuthority = &denied
	if r := collectionCommand(t, s, c, revoke.At); !errors.Is(r.Err, ErrOperatorAuthorityDenied) {
		t.Fatal("revoked actor reconciled admission", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
}

func TestCollectionActivationRejectsChangedOriginalBinding(t *testing.T) {
	for _, mode := range []string{"upload", "progress", "count", "result", "result-descriptor", "plan", "plan-descriptor", "profile", "request", "actor", "owner-epoch", "initial-authority", "initial-time", "unsealed", "expired", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := activationFixture(t, s)
			if mode == "rejected" {
				head, _ = validationPublishFixture(t, s, 2, false)
				for !head.Validation.HistorySealed {
					head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
				}
			}
			at := head.ActivityAt.Add(time.Second)
			var c CollectionCommand
			if mode == "rejected" {
				// Borrow structurally valid identities, which must never promote a
				// rejected original verdict into a success-only plan.
				good, _ := activationFixture(t, s)
				c = activationCommand(good, authority, at)
				c.OperationID, c.UploadID = head.ID, head.UploadID
			} else {
				c = activationCommand(head, authority, at)
			}
			switch mode {
			case "upload":
				c.UploadID = uuid.NewString()
			case "progress":
				c.Activation.InputProgressDigest = strings.Repeat("d", 64)
			case "count":
				c.Activation.ItemCount++
			case "result":
				c.Activation.ResultID = uuid.NewString()
			case "result-descriptor":
				c.Activation.ResultDescriptor.Bytes++
			case "plan":
				c.Activation.PlanID = uuid.NewString()
			case "plan-descriptor":
				c.Activation.PlanDescriptor.Bytes++
			case "profile":
				c.Activation.CapabilitiesDigest = strings.Repeat("d", 64)
			case "request":
				c.Activation.ValidationRequest = &CollectionValidationRequestFence{RequestID: uuid.NewString(), ClaimID: uuid.NewString(), RunID: uuid.NewString()}
			case "actor":
				c.ActivationAuthority.Actor = "other"
			case "owner-epoch":
				c.ActivationAuthority.Epoch = uuid.NewString()
			case "initial-authority":
				c.Activation.Authority.Revision = uuid.NewString()
			case "initial-time":
				c.Activation.At = at.Add(-time.Millisecond)
			case "unsealed":
				head.Validation.HistorySealed = false
				s.fsm.mu.Lock()
				s.fsm.image.Collections[head.ID] = head.Clone()
				s.fsm.mu.Unlock()
			case "expired":
				at = head.ExpiresAt
				c.Activation.At = at
			}
			before := head.Clone()
			if err := c.validate(at); err != nil {
				// A malformed count/descriptor is rejected before entering Raft.
				if _, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: at, Collection: &c}}); err == nil {
					t.Fatal("malformed admission reached durable submission")
				}
				planApplyStateUnchanged(t, s, before)
				return
			}
			if r := collectionCommand(t, s, c, at); r.Err == nil || r.Allowed || len(r.Events) != 0 {
				t.Fatal("invalid admission accepted")
			}
			planApplyStateUnchanged(t, s, before)
			if len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 {
				t.Fatal("rejection changed active state")
			}
		})
	}
}

func TestCollectionActivationRemainsLiveThenCancelsAfterStagingExpiry(t *testing.T) {
	s := openCatalogMemory(t)
	original, authority := activationFixture(t, s)
	at := original.ActivityAt.Add(time.Second)
	c := activationCommand(original, authority, at)
	head := validationApplyAllowed(t, collectionCommand(t, s, c, at))
	policy, _ := s.Authentication()
	replacement := lifecycleReplacement(policy)
	replacement.At = at.Add(time.Second)
	for i := range replacement.Principals {
		if replacement.Principals[i].ID == head.Actor {
			replacement.Principals[i].ExpiresAt = at.AddDate(1, 0, 0)
		}
	}
	if r := lifecycleCommand(t, s, CollectionActivationFormatVersion, replacement); r.Err != nil {
		t.Fatal(r.Err)
	}
	late := original.ExpiresAt.Add(time.Hour)
	fresh, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, late)
	if err != nil {
		t.Fatal(err)
	}
	if r := collectionCommand(t, s, cleanupCommand(head), late); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("inactivity retired admitted activation", r.Err)
	}
	if err := s.maintainCollections(late); err != nil {
		t.Fatal(err)
	}
	planApplyStateUnchanged(t, s, head)
	observed, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, late)
	if err != nil || observed.Phase != "applying" || observed.Activation == nil || !observed.TerminalAt.IsZero() {
		t.Fatal("point read lost admitted live state", err)
	}
	if _, err := s.CollectionOperationAs(context.Background(), head.ID, "reader", late); !errors.Is(err, ErrOperationNotFound) {
		t.Fatal("foreign principal saw admission", err)
	}
	view, err := s.ManagementOperationSnapshot(context.Background(), head.Actor, late, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := view.Page(context.Background(), "", "", 100)
	if err != nil || len(page) != 1 || page[0].Collection == nil || page[0].Collection.Phase != "applying" {
		t.Fatal("list lost live admission or audit polluted receipt index", err)
	}
	validation, status, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, late)
	if err != nil || validation == nil || status.State != "ready" || status.TerminalPhase != "" {
		t.Fatal("applying original verdict treated as terminal/pending", err)
	}
	far := head.Validation.FinalizedAt.AddDate(0, 0, 31)
	observed, err = s.CollectionOperationAs(context.Background(), head.ID, head.Actor, far)
	if err != nil || observed.Phase != "applying" || observed.Validation != nil {
		t.Fatal("validation retention expired activation", err)
	}
	if _, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, far); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("result retention was extended by activation", err)
	}
	if _, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, at.Add(-time.Nanosecond)); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("backdated activation observation accepted", err)
	}
	c.ActivationAuthority = &fresh
	if r := collectionCommand(t, s, c, late); r.Err != nil || !reflect.DeepEqual(*r.Collection, head) {
		t.Fatal("admitted retry expired with inactive input", r.Err)
	}
	cancel := activationCancel(head, fresh, late)
	missing := cancel
	missing.ActivationFence, missing.ActivationAuthority = nil, nil
	if r := collectionCommand(t, s, missing, late); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("old inactive cancel stopped admitted activation", r.Err)
	}
	staleCleanup := cleanupCommand(head)
	canceledResult := collectionCommand(t, s, cancel, late)
	canceled := validationApplyAllowed(t, canceledResult)
	if canceled.Phase != "canceled" || !reflect.DeepEqual(canceled.Activation, head.Activation) || len(canceledResult.Events) != 1 ||
		!canceled.TerminalAt.Equal(late) || !canceled.ActivityAt.Equal(original.ActivityAt) {
		t.Fatal("cancellation rewrote original admission")
	}
	if r := collectionCommand(t, s, c, late.Add(time.Second)); r.Err != nil || r.Collection.Phase != "canceled" || len(r.Events) != 0 {
		t.Fatal("retry resurrected canceled activation", r.Err)
	}
	if r := collectionCommand(t, s, cancel, late.Add(time.Second)); r.Err != nil || len(r.Events) != 0 || !reflect.DeepEqual(*r.Collection, canceled) {
		t.Fatal("cancel exact retry changed disposition", r.Err)
	}
	// The unchanged input counters alone are insufficient for cleanup: the
	// immutable activation identity must remain in the cleanup CAS boundary.
	staleCleanup.Cleanup.Activation = nil
	if r := collectionCommand(t, s, staleCleanup, late); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("pre-admission cleanup fence accepted", r.Err)
	}
	// Format10 writers retain admitted input for finalization. Exercise the
	// original format8 cleanup contract explicitly, including its retained receipt.
	executionResultHistoricalCleanup(t, s, canceled, late)
	receipt, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, late.Add(time.Second))
	if err != nil || receipt.Phase != "canceled" || !collectionActivationsEqual(receipt.Activation, head.Activation) {
		t.Fatal("terminal history lost activation after cleanup", err)
	}
	copy := receipt.Clone()
	if !collectionReceiptsEqual(receipt, copy) || receipt.Activation == copy.Activation {
		t.Fatal("receipt equality compared cloned pointer addresses")
	}
	copy.Activation.ID = uuid.NewString()
	if collectionReceiptsEqual(receipt, copy) {
		t.Fatal("receipt equality ignored changed original activation")
	}
	copy = receipt.Clone()
	copy.Validation = nil
	if copy.validate() == nil {
		t.Fatal("terminal activation lost original sealed result metadata")
	}
	if r := collectionCommand(t, s, c, late.Add(time.Second)); !errors.Is(r.Err, ErrOperationExpired) {
		t.Fatal("cleaned header reconstructed execution", r.Err)
	}
}
