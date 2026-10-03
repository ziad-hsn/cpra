//go:build externaljobs

package systems

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

const (
	workerDispatchConcurrency = 4
	workerDispatchCapacity    = 16
	workerDispatchBytes       = 16 << 20
)

type workerDispatchStore interface {
	FindWorkerExecution(context.Context, string, string, string) (persistence.WorkerDispatchState, bool, error)
	CheckWorkerExecutionCapacity(context.Context, persistence.JobTypeReference) error
	AllocateWorkerCheckID(context.Context) (string, error)
	CommitWorkerExecution(context.Context, persistence.WorkerExecutionIntent) (persistence.WorkerExecutionRecord, error)
	WorkerExecution(context.Context, string) (persistence.WorkerExecutionRecord, bool, error)
	ExpireWorkerAction(context.Context, persistence.WorkerExecutionIntent) error
	GetContext(context.Context, string) (persistence.Monitor, bool, error)
	Flush(context.Context) error
}

type workerDispatchCatalog interface {
	WorkerExecutionTimeout(context.Context, persistence.JobTypeReference, time.Duration) (time.Duration, error)
	PrepareWorkerExecution(context.Context, persistence.WorkerExecutionIntent) (persistence.WorkerExecutionIntent, error)
}

type workerDispatchRequest struct {
	token   uuid.UUID
	intent  persistence.WorkerExecutionIntent
	timeout time.Duration
}

type workerDispatchResult struct {
	token uuid.UUID
	state persistence.WorkerDispatchState
	err   error
}

// The controller owns this fixed preparation pool and joins it before storage
// closes. Only immutable identities cross its input channel; provider parameters
// are opened and sealed by the catalog outside the ECS owner. At most 16 requests
// are accepted, including results not yet consumed, so completion sends cannot
// depend on an owner that has stopped. No goroutine waits for a remote handler.
type workerDispatcher struct {
	store     workerDispatchStore
	catalog   workerDispatchCatalog
	requests  chan workerDispatchRequest
	results   chan workerDispatchResult
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
}

func newWorkerDispatcher(store workerDispatchStore, catalog workerDispatchCatalog) *workerDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &workerDispatcher{store: store, catalog: catalog, ctx: ctx, cancel: cancel,
		requests: make(chan workerDispatchRequest, workerDispatchCapacity),
		results:  make(chan workerDispatchResult, workerDispatchCapacity), done: make(chan struct{})}
}

func (d *workerDispatcher) start() {
	d.startOnce.Do(func() {
		var workers sync.WaitGroup
		for range workerDispatchConcurrency {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					select {
					case <-d.ctx.Done():
						return
					case request := <-d.requests:
						state, err := d.prepare(request)
						d.results <- workerDispatchResult{token: request.token, state: state, err: err}
					}
				}
			}()
		}
		go func() { workers.Wait(); close(d.done) }()
	})
}

func dispatchState(r persistence.WorkerExecutionRecord) persistence.WorkerDispatchState {
	_, terminal := r.TerminalReceipt()
	state := persistence.WorkerDispatchState{ID: r.Intent.ID, MonitorID: r.Intent.MonitorID,
		MonitorUID: r.Intent.MonitorUID, MonitorRevision: r.Intent.MonitorRevision,
		ActionID: r.Intent.ActionID, Generation: r.Intent.Generation, Scheduled: r.Intent.Scheduled, Terminal: terminal}
	if r.Intent.Schedule != nil {
		state.Schedule = *r.Intent.Schedule
	}
	return state
}

func (d *workerDispatcher) prepare(request workerDispatchRequest) (persistence.WorkerDispatchState, error) {
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	in := request.intent
	in.Scheduled = in.Scheduled.UTC()
	prior, exists, err := d.store.FindWorkerExecution(ctx, in.MonitorID, in.MonitorUID, in.ActionID)
	if err != nil || exists {
		return prior, err
	}
	timeout, err := d.catalog.WorkerExecutionTimeout(ctx, in.JobType, request.timeout)
	if err != nil {
		return persistence.WorkerDispatchState{}, err
	}
	in.Deadline = in.Scheduled.Add(timeout)
	if !time.Now().Before(in.Deadline) {
		if in.Category != "check" {
			if err := d.expireAction(ctx, in); err != nil {
				return persistence.WorkerDispatchState{}, err
			}
		}
		return persistence.WorkerDispatchState{}, persistence.ErrWorkerExecutionExpired
	}
	if err := d.store.CheckWorkerExecutionCapacity(ctx, in.JobType); err != nil {
		return persistence.WorkerDispatchState{}, err
	}
	in.ID, in.Revision = uuid.NewString(), uuid.NewString()
	if in.Category == "check" {
		in.ID, err = d.store.AllocateWorkerCheckID(ctx)
		if err != nil {
			// Allocation never grants work; an unreplied reservation expires.
			return persistence.WorkerDispatchState{}, err
		}
	}
	in, err = d.catalog.PrepareWorkerExecution(ctx, in)
	if err != nil {
		return persistence.WorkerDispatchState{}, err
	}
	record, err := d.store.CommitWorkerExecution(ctx, in)
	if !errors.Is(err, persistence.ErrCommitUnconfirmed) {
		return dispatchState(record), err
	}
	// Cancellation cannot discard a submission that may already have committed.
	// Retain the exact ciphertext/identity until a FIFO barrier and lookup settle
	// it. Finalize joins this owner; a supervisor supplies the hard stop deadline.
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := d.store.Flush(ctx)
		if err == nil {
			var found bool
			record, found, err = d.store.WorkerExecution(ctx, in.ID)
			if err == nil && found {
				cancel()
				if !reflect.DeepEqual(record.Intent, in) {
					return persistence.WorkerDispatchState{}, persistence.ErrWorkerExecutionConflict
				}
				return dispatchState(record), nil
			}
			if err == nil {
				if d.ctx.Err() != nil {
					cancel()
					return persistence.WorkerDispatchState{}, d.ctx.Err()
				}
				record, err = d.store.CommitWorkerExecution(ctx, in)
				if !errors.Is(err, persistence.ErrCommitUnconfirmed) {
					cancel()
					return dispatchState(record), err
				}
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *workerDispatcher) expireAction(ctx context.Context, in persistence.WorkerExecutionIntent) error {
	err := d.store.ExpireWorkerAction(ctx, in)
	if !errors.Is(err, persistence.ErrCommitUnconfirmed) {
		return err
	}
	// Settle the original submission before releasing shutdown ownership. An
	// action identity is the idempotency key; no execution ID or payload is made.
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := d.store.Flush(ctx)
		if err == nil {
			var m persistence.Monitor
			m, _, err = d.store.GetContext(ctx, in.MonitorID)
			if err == nil {
				a, ok := m.Actions[in.ActionID]
				if ok && a.CatalogUID == in.MonitorUID && a.Revision == in.MonitorRevision &&
					a.NotBefore.Equal(in.Scheduled) && a.State == persistence.Cancelled && a.Outcome == "worker_admission_expired" {
					cancel()
					return nil
				}
				if d.ctx.Err() != nil {
					cancel()
					return d.ctx.Err()
				}
				err = d.store.ExpireWorkerAction(ctx, in)
				if !errors.Is(err, persistence.ErrCommitUnconfirmed) {
					cancel()
					return err
				}
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
}
