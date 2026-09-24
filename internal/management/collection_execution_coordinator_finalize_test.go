package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func executionFinalizationFixture(t *testing.T, applied bool) (collectionSourceFixture, persistence.CollectionState, *CollectionExecutionCoordinator) {
	t.Helper()
	f, head := candidateFixture(t, collectionMonitor("settled", "http://original.example/health"), nil)
	w := executionTestWorker(t, f, head)
	for range 3 {
		executionAdvance(t, w, head.ID)
	}
	head, _, _ = f.store.CollectionGet(head.ID)
	if handled, err := w.finalizeNext(w.ctx); err != nil || handled {
		t.Fatal("parent finalized while its accepted child was pending", err)
	}
	children, err := f.store.PendingOperationsContext(w.ctx)
	if err != nil || len(children) != 1 {
		t.Fatal("missing accepted child", children, err)
	}
	child := children[0]
	update := persistence.OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion, Applied: applied}
	results, err := f.store.Submit(w.ctx, []persistence.Command{{Kind: "operation", At: head.Execution.LastAt.Add(time.Second), Operation: &update}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("record controller child disposition", results, err)
	}
	head, _, _ = f.store.CollectionGet(head.ID)
	w.now = collectionClock(head.Execution.LastAt.Add(time.Second))
	return f, head, w
}

func TestCollectionExecutionCoordinatorFinalizationRetainsSettledFactsWithoutAuthority(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{true: "applied", false: "projection failed"}[applied], func(t *testing.T) {
			f, head, w := executionFinalizationFixture(t, applied)
			w.profile = "changed-runtime-profile"
			w.now = collectionClock(head.Execution.LastAt.Add(2 * time.Hour))
			if _, err := f.store.ObserveOperatorAuthority(w.ctx, head.Actor, w.now()); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
				t.Fatal("fixture actor did not expire", err)
			}
			inner, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{99}, 32))
			f.catalog.sealer, _ = secureconfig.NewSealer(candidateFailKeys{KeyWrapper: inner, failOpen: true})
			before, exists, err := f.store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: "settled"})
			if err != nil || !exists {
				t.Fatal(err)
			}
			if handled, err := w.finalizeNext(w.ctx); err != nil || !handled || w.finalizing != nil {
				t.Fatal("known outcomes required current authority/profile/crypto", err)
			}
			after, _, err := f.store.CollectionGet(head.ID)
			want := map[bool]string{true: "completed", false: "partial"}[applied]
			if err != nil || after.ExecutionResult == nil || after.Phase != want || after.ExecutionResult.Summary.Fence.Progress.Accepted != 1 || after.ExecutionResult.Summary.Fence.Progress.ChildApplied != map[bool]uint64{true: 1, false: 0}[applied] {
				t.Fatal("finalization changed committed/applied facts", after, err)
			}
			if !reflect.DeepEqual(head.Execution, after.Execution) || !head.ActivityAt.Equal(after.ActivityAt) {
				t.Fatal("finalization modified decisions or renewed staging")
			}
			current, exists, err := f.store.CatalogGet(before.Key)
			if err != nil || !exists || !reflect.DeepEqual(current, before) {
				t.Fatal("finalization changed accepted catalog mutation", err)
			}
		})
	}
}

func TestCollectionExecutionCoordinatorFinalizationLostReplyKeepsOriginalCommand(t *testing.T) {
	f, head, w := executionFinalizationFixture(t, true)
	var commands []persistence.Command
	w.submit = func(ctx context.Context, batch []persistence.Command) ([]persistence.Result, error) {
		commands = append(commands, batch[0])
		results, err := f.store.Submit(ctx, batch)
		if err != nil {
			return results, err
		}
		return nil, errors.Join(persistence.ErrCommitUnconfirmed, raft.ErrLeadershipLost)
	}
	if handled, err := w.finalizeNext(w.ctx); err != nil || !handled || w.finalizing == nil || !w.finalizing.barrier {
		t.Fatal("lost finalization reply released immutable command", err)
	}
	original := w.finalizing.command
	committed, _, _ := f.store.CollectionGet(head.ID)
	work, err := f.store.CollectionExecutionFinalizationWork(w.ctx, w.now())
	if err != nil || len(work) != 0 {
		t.Fatal("committed finalization still selected", err)
	}
	w.flush = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithTimeout(w.ctx, 20*time.Millisecond)
	err = w.submitFinalization(ctx)
	cancel()
	if !errors.Is(err, ErrOutcomeUnconfirmed) || len(commands) != 1 || w.finalizing == nil || !w.finalizing.barrier || !reflect.DeepEqual(w.finalizing.command, original) {
		t.Fatal("uncertain FIFO barrier retried or dropped the original finalization", err)
	}
	barriers := 0
	w.flush = func(ctx context.Context) error {
		barriers++
		if barriers == 1 {
			return raft.ErrNotLeader
		}
		return f.store.Flush(ctx)
	}
	if _, err := retryCollectionRead(w.ctx, w.pauseLeadership, func() (struct{}, error) {
		return struct{}{}, w.reconcileFinalizationBarrier(w.ctx)
	}); err != nil || len(commands) != 1 || w.finalizing == nil || !reflect.DeepEqual(w.finalizing.command, original) {
		t.Fatal("leadership barrier changed retained finalization", err)
	}
	w.submit = func(ctx context.Context, batch []persistence.Command) ([]persistence.Result, error) {
		commands = append(commands, batch[0])
		return f.store.Submit(ctx, batch)
	}
	w.now = collectionClock(w.now().Add(time.Hour))
	if handled, err := w.finalizeNext(w.ctx); err != nil || !handled || w.finalizing != nil || barriers != 2 || len(commands) != 2 || !reflect.DeepEqual(commands[0], commands[1]) {
		t.Fatal("lost-reply retry changed original identity/fence/time", err)
	}
	after, _, _ := f.store.CollectionGet(head.ID)
	if after.ExecutionResult == nil || !reflect.DeepEqual(after.Execution, committed.Execution) || !reflect.DeepEqual(after.ExecutionResult.Summary, committed.ExecutionResult.Summary) || after.Phase != committed.Phase {
		t.Fatal("exact retry changed immutable result")
	}
	page, err := f.store.History().Page("collection-execution/"+head.ID, "", 10)
	anchors := 0
	for _, event := range page.Events {
		if event.CollectionExecution != nil {
			anchors++
		}
	}
	if err != nil || anchors != 1 {
		t.Fatal("finalization retry duplicated anchor", err)
	}
}

func TestCollectionExecutionCoordinatorFinalizesCanceledAdmissionBeforeBegin(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("never-started", "http://original.example/health"), nil)
	at := head.Activation.At.Add(time.Second)
	if _, err := f.catalog.CancelCollection(context.Background(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	stopped, _, _ := f.store.CollectionGet(head.ID)
	w, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), collectionClock(at.Add(2*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executionStop(t, w) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		got, _, err := f.store.CollectionGet(head.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ExecutionResult != nil {
			if got.Phase != "canceled" || got.Execution != nil || !reflect.DeepEqual(got.Cancellation, stopped.Cancellation) || !got.TerminalAt.Equal(stopped.TerminalAt) || got.ExecutionResult.Summary.Unattempted != head.ItemCount {
				t.Fatal("pre-begin finalization invented execution or changed cancellation")
			}
			break
		}
		select {
		case <-w.Done():
			t.Fatal("coordinator failed before finalization", w.Err())
		case <-deadline.C:
			t.Fatal("coordinator did not finalize canceled admission", w.Status())
		case <-time.After(5 * time.Millisecond):
		}
	}
	executionStop(t, w)
	if view, err := f.store.CatalogSnapshot(); err != nil || view.Len() != 0 {
		t.Fatal("finalization installed an unattempted resource", err)
	}
}

func TestCollectionExecutionCoordinatorFinalizationQuotaDoesNotBlockNewDecisions(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("quota", "http://original.example/health"), nil)
	at := head.Activation.At.Add(time.Second)
	if _, err := f.catalog.CancelCollection(context.Background(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	w := executionTestWorker(t, f, head)
	w.submit = func(context.Context, []persistence.Command) ([]persistence.Result, error) {
		return []persistence.Result{{Err: persistence.ErrCollectionQuota}}, nil
	}
	if handled, err := w.finalizeNext(w.ctx); err != nil || !handled || w.finalizing != nil || w.finalizationRetryAt[head.ID].IsZero() {
		t.Fatal("confirmed quota blocked all work", err)
	}
	fresh := stagedSourceFixtureStore(t, f.catalog, f.store, 1, func(int) (persistence.CatalogKey, []byte) {
		r := collectionMonitor("next", "http://original.example/health")
		raw, _ := json.Marshal(r)
		return persistence.CatalogKey{Kind: "Monitor", ID: "next"}, raw
	}, func(h *persistence.CollectionState) { h.Actor, h.Owner = head.Actor, head.Owner }, nil)
	next := executionAdmitFixture(t, &fresh)
	w.submit = f.store.Submit
	w.now = collectionClock(next.Activation.At.Add(10 * time.Second))
	if handled, err := w.finalizeNext(w.ctx); err != nil || handled {
		t.Fatal("quota-deferred parent immediately monopolized worker", err)
	}
	executionAdvance(t, w, next.ID)
	if got, _, err := f.store.CollectionGet(next.ID); err != nil || got.Execution == nil {
		t.Fatal("unrelated admitted parent could not begin", err)
	}
	if !f.store.Status().Ready {
		t.Fatal("ordinary finalization quota poisoned store")
	}
	item := executionWork(t, w, next.ID)
	if err := w.prepareStep(w.ctx, item); err != nil {
		t.Fatal(err)
	}
	original := w.pending
	delete(w.finalizationRetryAt, head.ID)
	if handled, err := w.finalizeNext(w.ctx); err != nil || handled || w.finalizing != nil || w.pending != original {
		t.Fatal("finalization displaced unresolved item command", err)
	}
}

func TestCollectionExecutionCoordinatorFinalizationWaitsForOriginalClock(t *testing.T) {
	f, head, w := executionFinalizationFixture(t, true)
	w.now = collectionClock(head.Execution.LastAt.Add(-time.Nanosecond))
	if handled, err := w.finalizeNext(w.ctx); err != nil || handled || w.finalizing != nil {
		t.Fatal("backward clock created a finalization command", err)
	}
	if after, _, err := f.store.CollectionGet(head.ID); err != nil || !reflect.DeepEqual(after, head) {
		t.Fatal("clock wait changed original facts", err)
	}
	w.now = collectionClock(head.Execution.LastAt)
	if handled, err := w.finalizeNext(w.ctx); err != nil || !handled {
		t.Fatal("finalization did not resume at the original fence time", err)
	}
}

func TestCollectionExecutionCoordinatorShutdownJoinsFinalizationSubmission(t *testing.T) {
	f, head, w := executionFinalizationFixture(t, true)
	if err := f.store.RegisterCollectionExecutionCoordinator(w.ctx, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	w.submit = func(ctx context.Context, batch []persistence.Command) ([]persistence.Result, error) {
		if len(batch) != 1 || batch[0].CollectionExecute.Action != "finalize" {
			t.Error("unexpected submission")
		}
		close(entered)
		<-release
		return nil, ctx.Err()
	}
	w.ready.Store(true)
	go func() {
		w.err = w.loop()
		w.ready.Store(false)
		w.clearPending()
		w.finalizing = nil
		close(w.done)
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		executionStop(t, w)
	}()
	select {
	case <-entered:
	case <-w.Done():
		t.Fatal("finalizer stopped before submit", w.Err())
	case <-time.After(5 * time.Second):
		t.Fatal("finalizer did not submit")
	}
	w.BeginStop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := w.Wait(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || w.Ready() {
		t.Fatal("shutdown abandoned active finalization call", err)
	}
	if err := f.store.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, persistence.ErrCollectionExecutionCoordinatorRegistered) {
		t.Fatal("shutdown released single-owner registration", err)
	}
	close(release)
	released = true
	executionStop(t, w)
	if after, _, err := f.store.CollectionGet(head.ID); err != nil || !reflect.DeepEqual(after, head) || f.catalog.failed.Load() {
		t.Fatal("canceled submission changed facts or poisoned catalog", err)
	}
}
