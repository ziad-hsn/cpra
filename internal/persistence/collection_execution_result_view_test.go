package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func executionResultViewPublish(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	for n := 0; n < 64 && !head.ExecutionResult.HistorySealed; n++ {
		head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)))
	}
	if !head.ExecutionResult.HistorySealed {
		t.Fatal("result publication did not seal")
	}
	return head
}

func TestCollectionExecutionResultViewPendingOwnerAndReady(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 3)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, status, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil || view != nil || status.State != "pending" || status.Summary == nil || status.ResultID != head.Activation.ID {
				t.Fatal("anchor incorrectly implies published results", status, err)
			}
			// A wrong principal must not wait for history, even with a finalized
			// original header. Authentication/permission checks remain above Store.
			s.History().mu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			_, _, err = s.CollectionExecutionResultView(ctx, head.ID, "another-owner", at)
			cancel()
			s.History().mu.Unlock()
			if !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("owner check accessed history or exposed the result", err)
			}
			head = executionResultViewPublish(t, s, head)
			view, status, err = s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil || view == nil || status.State != "ready" || status.OperationID != head.ID || status.ItemCount != 3 || status.IdentityFormat != head.IdentityFormat || status.ContentDigest != head.ContentDigest {
				t.Fatal("sealed result not ready", status, err)
			}
			if !collectionExecutionReceiptsEqual(view.Receipt(), collectionExecutionReceiptFor(*head.ExecutionResult)) {
				t.Fatal("view changed original summary/descriptor")
			}
			identity, receipt := view.Identity(), view.Receipt()
			identity.Summary.Fence.Progress.Processed++
			receipt.Summary.Fence.Progress.Processed++
			if view.Identity().Summary.Fence.Progress.Processed != 0 || view.Receipt().Summary.Fence.Progress.Processed != 0 {
				t.Fatal("getter exposed a mutable result summary")
			}
			page, err := view.Page(context.Background(), 0, 0, at)
			if err != nil || len(page.Items) != 3 || page.NextAfter != 0 {
				t.Fatal("default protected page", err)
			}
			first, err := view.Page(context.Background(), 0, 1, at)
			if err != nil || len(first.Items) != 1 || first.NextAfter != 1 {
				t.Fatal("bounded protected page", err)
			}
			for _, limit := range []int{-1, 501} {
				if _, err := view.Page(context.Background(), 0, limit, at); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("invalid page bound accepted", err)
				}
			}
			if _, err := view.Page(context.Background(), 4, 100, at); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("out-of-range original cursor accepted", err)
			}
			index := view.index
			createCatalog(t, s, catalogDeltaRecord("Credential", "later-unrelated"))
			later, err := view.Page(context.Background(), 0, 500, at)
			if err != nil || view.index != index || !reflect.DeepEqual(later, page) {
				t.Fatal("later commit changed original page or watermark", err)
			}
		})
	}
}

func TestCollectionExecutionResultViewIgnoresOldCancellationDeadline(t *testing.T) {
	s := openCatalogMemory(t)
	head, command := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, command, "accepted")
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	canceledAt := head.TerminalAt
	at := canceledAt.AddDate(0, 0, 35)
	update := OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion, Applied: false}
	results, err := s.Submit(context.Background(), []Command{{Kind: "operation", Operation: &update, At: at}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("late original child settlement", err)
	}
	s.fsm.mu.RLock()
	head = s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.RUnlock()
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	view, status, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, head.ExecutionResult.Summary.FinalizedAt.Add(time.Hour))
	if err != nil || status.State != "ready" || view == nil || status.Summary.Outcome != "canceled" || !head.TerminalAt.Equal(canceledAt) {
		t.Fatal("old cancellation deadline hid retained child results", err)
	}
	page, err := view.Page(context.Background(), 0, 100, head.ExecutionResult.Summary.FinalizedAt.Add(time.Hour))
	if err != nil || len(page.Items) != 1 || page.Items[0].Decision != "accepted" || page.Items[0].Child == nil || page.Items[0].Child.State != "failed" {
		t.Fatal("late result replaced the committed decision", err)
	}
}

func TestCollectionExecutionResultViewHistoryWaitFences(t *testing.T) {
	for _, scenario := range []string{"cancel", "epoch", "owner", "health", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			s, head := executionPublicationFixture(t, false, 1)
			head = executionResultViewPublish(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, _, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "deadline" {
				at = head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30).Add(-40 * time.Millisecond)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			s.History().mu.Lock()
			locked := true
			defer func() {
				if locked {
					s.History().mu.Unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				page, err := view.Page(ctx, 0, 100, at)
				if err != nil && len(page.Items) != 0 {
					err = errors.New("failed read returned rows")
				}
				done <- err
			}()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("result never waited on history")
			}
			// A blocked disk read owns neither Store nor FSM locks.
			probe, stopProbe := context.WithTimeout(context.Background(), 100*time.Millisecond)
			err = s.ControllerHealthContext(probe)
			stopProbe()
			if err != nil {
				t.Fatal("history read retained owner locks", err)
			}
			want := ErrOperationExpired
			switch scenario {
			case "cancel":
				cancel()
				want = context.Canceled
			case "epoch":
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = uuid.NewString()
				s.fsm.mu.Unlock()
			case "owner":
				s.fsm.mu.Lock()
				changed := s.fsm.image.Collections[head.ID].Clone()
				changed.Actor = "another-owner"
				s.fsm.image.Collections[head.ID] = changed
				s.fsm.mu.Unlock()
				want = ErrOperationNotFound
			case "health":
				s.MarkUnavailable(errors.New("test storage fault"))
				want = ErrCollectionUnavailable
			case "deadline":
				time.Sleep(60 * time.Millisecond)
			}
			if scenario != "cancel" {
				s.History().mu.Unlock()
				locked = false
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatal("blocked read ignored new fence", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked result did not complete/cancel")
			}
		})
	}
}

func TestCollectionExecutionResultViewPendingAnchorAndCutoff(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			h := s.History()
			h.mu.Lock()
			if disk {
				day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
				if err := h.databases[day].Update(func(tx *bolt.Tx) error { return tx.Bucket(collectionExecutionAnchorBucket).Delete([]byte(head.ID)) }); err != nil {
					h.mu.Unlock()
					t.Fatal(err)
				}
			} else {
				delete(h.memoryExecutionAnchors, head.ID)
			}
			h.mu.Unlock()
			if view, _, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) || view != nil {
				t.Fatal("missing expected anchor became pending/expired", err)
			}
		})
	}
	s, head := executionPublicationFixture(t, false, 1)
	head = executionResultViewPublish(t, s, head)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	view, _, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.History().Expire(head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Page(context.Background(), 0, 100, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("backward read ignored monotonic cutoff", err)
	}
	if _, _, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("new view resurrected expired cohort", err)
	}
}

func TestCollectionExecutionResultViewPreservesOriginalResultAndCurrentEpoch(t *testing.T) {
	for _, scenario := range []string{"header", "summary", "expired", "policy", "history"} {
		t.Run(scenario, func(t *testing.T) {
			s, head := executionPublicationFixture(t, false, 1)
			head = executionResultViewPublish(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, _, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			want := ErrCollectionUnavailable
			s.fsm.mu.Lock()
			switch scenario {
			case "header":
				delete(s.fsm.image.Collections, head.ID)
				want = nil
			case "summary":
				changed := head.Clone()
				changed.ExecutionResult.Summary.FinalizedAt = changed.ExecutionResult.Summary.FinalizedAt.Add(time.Millisecond)
				s.fsm.image.Collections[head.ID] = changed
			case "expired":
				changed := head.Clone()
				changed.ExecutionResult.HistoryExpiredAt = head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
				s.fsm.image.Collections[head.ID] = changed
				want = ErrOperationExpired
			case "policy":
				s.fsm.image.Authentication.Revision = uuid.NewString()
				want = nil
			case "history":
				original := s.fsm.history
				s.fsm.history, err = openHistory("")
				if err != nil {
					s.fsm.mu.Unlock()
					t.Fatal(err)
				}
				replacement := s.fsm.history
				defer func() {
					s.fsm.mu.Lock()
					s.fsm.history = original
					s.fsm.mu.Unlock()
					_ = replacement.Close()
				}()
			}
			s.fsm.mu.Unlock()
			if _, err := view.Page(context.Background(), 0, 100, at); !errors.Is(err, want) {
				t.Fatal("view ignored metadata fence", scenario, err, want)
			}
		})
	}
}

func TestCollectionExecutionResultViewConstructorRechecksElapsedDeadline(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprint(sealed), func(t *testing.T) {
			s, head := executionPublicationFixture(t, false, 1)
			if sealed {
				head = executionResultViewPublish(t, s, head)
			}
			at := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30).Add(-40 * time.Millisecond)
			base, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			s.History().mu.Lock()
			locked := true
			defer func() {
				if locked {
					s.History().mu.Unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				view, status, err := s.CollectionExecutionResultView(ctx, head.ID, head.Actor, at)
				if err != nil && (view != nil || status.State != "") {
					err = errors.New("failed constructor exposed an availability result")
				}
				done <- err
			}()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("constructor did not reach history wait")
			}
			time.Sleep(60 * time.Millisecond)
			s.History().mu.Unlock()
			locked = false
			select {
			case err := <-done:
				if !errors.Is(err, ErrOperationExpired) {
					t.Fatal("constructor ignored elapsed retention deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("constructor did not leave its history wait")
			}
		})
	}
}
