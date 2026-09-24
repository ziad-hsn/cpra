package management

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestCollectionLeadershipReadRecoveryAndCancellation(t *testing.T) {
	var pauses []bool
	calls := 0
	value, err := retryCollectionRead(context.Background(), func(p bool) { pauses = append(pauses, p) }, func() (int, error) {
		calls++
		if calls == 1 {
			return 0, errors.Join(persistence.ErrCollectionUnavailable, raft.ErrNotLeader)
		}
		return 42, nil
	})
	if err != nil || value != 42 || calls != 2 || len(pauses) != 2 || !pauses[0] || pauses[1] {
		t.Fatal("did not pause then recover original read", calls, pauses, err)
	}
	calls = 0
	_, err = retryCollectionRead(context.Background(), func(bool) {}, func() (int, error) { calls++; return 0, persistence.ErrCollectionUnavailable })
	if !errors.Is(err, persistence.ErrCollectionUnavailable) || calls != 1 {
		t.Fatal("permanent failure retried", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	_, err = retryCollectionRead(ctx, func(paused bool) {
		if paused {
			cancel()
		}
	}, func() (int, error) { calls++; return 0, raft.ErrNotLeader })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("cancellation did not stop waiting", calls, err)
	}
}

func TestCollectionExecutionLeadershipBarrierRetainsOriginalCandidate(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	w := executionTestWorker(t, f, head)
	executionAdvance(t, w, head.ID)
	item := executionWork(t, w, head.ID)
	if err := w.prepareStep(w.ctx, item); err != nil {
		t.Fatal(err)
	}
	original := w.pending.command
	var submissions atomic.Int32
	w.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
		submissions.Add(1)
		_, err := f.store.Submit(ctx, commands)
		if err != nil {
			return nil, err
		}
		return nil, errors.Join(persistence.ErrCommitUnconfirmed, raft.ErrLeadershipLost)
	}
	if err := w.submitStep(w.ctx, item.Actor); !errors.Is(err, ErrOutcomeUnconfirmed) {
		t.Fatal(err)
	}
	calls := 0
	w.flush = func(ctx context.Context) error {
		calls++
		if calls == 1 {
			if submissions.Load() != 1 || w.pending == nil || !candidateExecutionSameWire(*original.Prepared, *w.pending.command.Prepared) {
				t.Fatal("uncertain candidate replaced")
			}
			return raft.ErrNotLeader
		}
		return f.store.Flush(ctx)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	_, err := retryCollectionRead(ctx, w.pauseLeadership, func() (struct{}, error) { return struct{}{}, w.reconcileBarrier(ctx) })
	if err != nil || calls != 2 || submissions.Load() != 1 || w.pending == nil || w.pending.barrier || f.catalog.failed.Load() {
		t.Fatal("barrier recovery changed work or poisoned catalog", err)
	}
	w.submit = f.store.Submit
	if err := w.submitStep(ctx, item.Actor); err != nil {
		t.Fatal("original idempotent reconciliation", err)
	}
	after, _, err := f.store.CollectionGet(head.ID)
	if err != nil || after.Execution.Prepared == nil || after.Execution.Prepared.ID != original.Prepared.ID {
		t.Fatal("did not retain original prepared identity", err)
	}
}
