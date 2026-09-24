package management

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// CollectionExecutionStatus describes private execution and finalization, not
// publication of retained application results.
type CollectionExecutionStatus struct {
	OperationID     string
	Condition       string
	Processed       uint64
	Accepted        uint64
	PendingChildren uint64
}

// CollectionExecutionCoordinator owns one item attempt per opened Store. It
// resumes admitted original operations and finalizes certified settled facts;
// it never admits activation. Startup remains private until retained
// application results and the public lifecycle are implemented.
type CollectionExecutionCoordinator struct {
	catalog *Catalog
	profile string
	now     func() time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	ready   atomic.Bool
	err     error // Published by closing done.
	mu      sync.RWMutex
	status  []CollectionExecutionStatus
	// These bound methods are fixed before Run, including in fault fixtures.
	submit              func(context.Context, []persistence.Command) ([]persistence.Result, error)
	flush               func(context.Context) error
	pending             *collectionExecutionPending // Owned by the sole loop.
	finalizing          *collectionExecutionFinalizationPending
	finalizationRetryAt map[string]time.Time
}

func (c *Catalog) StartCollectionExecutionCoordinator(ctx context.Context, now func() time.Time) (*CollectionExecutionCoordinator, error) {
	if ctx == nil || now == nil || c == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.verified.Load() || c.failed.Load() {
		return nil, ErrUnavailable
	}
	_, profile, err := collectionValidationProfile()
	if err != nil {
		return nil, err
	}
	if err := c.store.RegisterCollectionExecutionCoordinator(ctx, uuid.NewString()); err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancel(ctx)
	w := &CollectionExecutionCoordinator{catalog: c, profile: profile, now: now, ctx: owned, cancel: cancel, done: make(chan struct{}), submit: c.store.Submit, flush: c.store.Flush}
	if _, err := c.store.CollectionExecutionWork(owned, now()); err != nil {
		cancel()
		return nil, err
	}
	w.ready.Store(true)
	go func() {
		w.err = w.loop()
		w.ready.Store(false)
		w.clearPending()
		w.finalizing = nil
		close(w.done)
	}()
	return w, nil
}

func (w *CollectionExecutionCoordinator) Ready() bool {
	return w != nil && w.ready.Load() && w.ctx.Err() == nil && w.catalog.store.Status().Ready
}

// BeginStop never abandons a running crypto call or releases Store ownership.
// The application joins this coordinator before flushing/closing dependencies.
func (w *CollectionExecutionCoordinator) BeginStop() {
	if w != nil {
		w.ready.Store(false)
		w.cancel()
	}
}

func (w *CollectionExecutionCoordinator) Done() <-chan struct{} { return w.done }

func (w *CollectionExecutionCoordinator) Err() error {
	select {
	case <-w.done:
		return w.err
	default:
		return nil
	}
}

func (w *CollectionExecutionCoordinator) Wait(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	select {
	case <-w.done:
		return w.err
	default:
	}
	select {
	case <-w.done:
		return w.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status retains at most the admitted parent bound and returns detached scalar
// observations. No ciphertext, plaintext, credentials or arbitrary error text.
func (w *CollectionExecutionCoordinator) Status() []CollectionExecutionStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]CollectionExecutionStatus(nil), w.status...)
}

func (w *CollectionExecutionCoordinator) publish(work []persistence.CollectionExecutionWork, conditions map[string]string) {
	status := make([]CollectionExecutionStatus, 0, len(work))
	for _, item := range work {
		condition := conditions[item.OperationID]
		if condition == "" {
			condition = "queued"
		}
		status = append(status, CollectionExecutionStatus{OperationID: item.OperationID, Condition: condition,
			Processed: item.Processed, Accepted: item.Accepted, PendingChildren: item.Accepted - item.ChildTerminals})
	}
	w.mu.Lock()
	w.status = status
	w.mu.Unlock()
}

func (w *CollectionExecutionCoordinator) reason(ctx context.Context, item persistence.CollectionExecutionWork) (string, error) {
	if item.CapabilitiesDigest != w.profile {
		return "capabilitiesChanged", nil
	}
	at := w.now()
	if at.Before(item.ActivationAt) || at.Before(item.LastAt) {
		return "clockBeforeProgress", nil
	}
	_, err := retryCollectionRead(ctx, w.pauseLeadership, func() (persistence.OperatorAuthority, error) {
		return w.catalog.store.ObserveOperatorAuthority(ctx, item.Actor, w.now())
	})
	if errors.Is(err, persistence.ErrOperatorAuthorityDenied) || errors.Is(err, persistence.ErrAuthenticationConflict) {
		return "authorityUnavailable", nil
	}
	return "", err
}

func (w *CollectionExecutionCoordinator) loop() error {
	conditions := make(map[string]string)
	activeID := ""
	delay := false
	for {
		if delay {
			timer := time.NewTimer(time.Second)
			select {
			case <-w.ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		} else {
			runtime.Gosched()
		}
		delay = true
		if w.ctx.Err() != nil {
			return nil
		}
		// Resolve uncertain admission before any view can imply absence or let
		// cancellation discard the retained candidate. An uncertain barrier
		// fails closed; restart can reconcile after completed log recovery.
		if _, err := retryCollectionRead(w.ctx, w.pauseLeadership, func() (struct{}, error) {
			return struct{}{}, w.reconcileBarrier(w.ctx)
		}); err != nil {
			return w.failExecution(err)
		}
		if _, err := retryCollectionRead(w.ctx, w.pauseLeadership, func() (struct{}, error) {
			return struct{}{}, w.reconcileFinalizationBarrier(w.ctx)
		}); err != nil {
			return w.failExecution(err)
		}
		if !w.catalog.verified.Load() || w.catalog.failed.Load() {
			return w.failExecution(ErrUnavailable)
		}
		if handled, err := w.finalizeNext(w.ctx); err != nil {
			return w.failExecution(err)
		} else if handled {
			continue
		}
		work, err := w.work(w.ctx)
		if err != nil {
			return w.failExecution(err)
		}
		live := make(map[string]bool, len(work))
		for _, item := range work {
			live[item.OperationID] = true
		}
		for id := range conditions {
			if !live[id] {
				delete(conditions, id)
			}
		}
		if w.pending != nil && !live[w.pending.command.Binding.OperationID] {
			w.clearPending() // A confirmed cancellation/restore/retirement won.
		}
		selected := -1
		for i, item := range work {
			isPending := w.pending != nil && w.pending.command.Binding.OperationID == item.OperationID
			if item.Begun && item.Processed == item.ItemCount && !isPending {
				conditions[item.OperationID] = "awaitingCompletion"
				continue
			}
			reason, err := w.reason(w.ctx, item)
			if err != nil {
				return w.failExecution(err)
			}
			if reason != "" {
				conditions[item.OperationID] = reason
				if w.pending != nil && w.pending.command.Binding.OperationID == item.OperationID {
					// No uncertain submission remains. A committed prepared slot
					// remains in Raft; an unsubmitted candidate can be discarded.
					w.clearPending()
				}
				continue
			}
			if selected < 0 || item.OperationID == activeID {
				selected = i
			}
			if w.pending != nil && w.pending.command.Binding.OperationID == item.OperationID {
				selected = i
				break
			}
		}
		w.publish(work, conditions)
		if selected < 0 {
			activeID = ""
			continue
		}
		item := work[selected]
		activeID = item.OperationID
		conditions[activeID] = "executing"
		if w.pending == nil {
			err = w.prepareStep(w.ctx, item)
			if errors.Is(err, persistence.ErrCollectionConflict) {
				conditions[activeID] = "viewChanged"
				continue // No command was submitted; reobserve the original row.
			}
		}
		if err == nil {
			err = w.submitStep(w.ctx, item.Actor)
		}
		switch {
		case w.ctx.Err() != nil:
			return nil
		case err == nil:
			conditions[activeID] = "queued"
			delay = false
		case persistence.IsLeadershipUnavailable(err):
			w.pauseLeadership(true)
			conditions[activeID] = "leadershipUnavailable"
		case errors.Is(err, ErrOutcomeUnconfirmed):
			conditions[activeID] = "commitUnconfirmed"
		case errors.Is(err, persistence.ErrCatalogBusy), errors.Is(err, persistence.ErrCollectionQuota):
			conditions[activeID] = "backpressure"
		case errors.Is(err, persistence.ErrOperatorAuthorityDenied), errors.Is(err, persistence.ErrAuthenticationConflict), errors.Is(err, persistence.ErrOperationExpired), errors.Is(err, persistence.ErrOperationNotFound):
			w.clearPending()
			conditions[activeID] = "authorityUnavailable"
		case errors.Is(err, persistence.ErrCollectionConflict):
			// A confirmed original-command rejection may be cancellation. Read
			// once, without rebasing. Still-applying original conflicts halt.
			current, readErr := w.work(w.ctx)
			if readErr != nil {
				return w.failExecution(readErr)
			}
			present := false
			for _, candidate := range current {
				present = present || candidate.OperationID == activeID
			}
			if present {
				return w.failExecution(err)
			}
			w.clearPending()
		case errors.Is(err, persistence.ErrCatalogSequenceExhausted):
			conditions[activeID] = "sequenceExhausted"
			w.publish(work, conditions)
			return w.failExecution(err) // This counter cannot recover by waiting.
		default:
			return w.failExecution(err)
		}
		w.publish(work, conditions)
	}
}

func (w *CollectionExecutionCoordinator) failExecution(err error) error {
	if w.ctx.Err() != nil {
		return nil
	}
	w.ready.Store(false)
	// This is management admission failure, not an invented disk fault. Main
	// must supervise the owner and retain diagnostics while dependencies drain.
	w.catalog.failed.Store(true)
	return err
}
