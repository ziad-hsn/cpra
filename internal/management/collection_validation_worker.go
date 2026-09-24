package management

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// CollectionValidationCoordinator owns one supervisor and at most one compiler
// attempt. Durable headers, bounded by Store admission, are its work queue.
// Main must stop/join this owner before closing storage or its encryption keys.
type CollectionValidationCoordinator struct {
	catalog *Catalog
	runID   string
	profile string
	now     func() time.Time // May be called by supervisor and attempt concurrently.
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	ready   atomic.Bool
	err     error // Written before closing done; read only after observing closure.
}

// StartCollectionValidationCoordinator registers once per opened Store and
// retires abandoned claims synchronously, before starting any compiler work.
// A failed startup owns no goroutine but cannot register again on the same Store.
// now must be safe for concurrent calls and return actual observation time.
func (c *Catalog) StartCollectionValidationCoordinator(ctx context.Context, now func() time.Time) (*CollectionValidationCoordinator, error) {
	if ctx == nil || now == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.Ready() || c.validationStopping.Load() {
		return nil, ErrUnavailable
	}
	_, profile, err := collectionValidationProfile()
	if err != nil {
		return nil, err
	}
	runID := uuid.NewString()
	if err := c.store.RegisterCollectionValidationCoordinator(ctx, runID); err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancel(ctx)
	w := &CollectionValidationCoordinator{catalog: c, runID: runID, profile: profile, now: now, ctx: owned, cancel: cancel, done: make(chan struct{})}
	work, err := c.store.CollectionValidationWork(owned, now())
	if err == nil {
		for _, item := range work {
			if w.eligible(item, now()) && item.Request.Claim != nil {
				if err = w.interrupt(owned, item, "coordinatorRestarted"); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = owned.Err()
	}
	if err != nil {
		cancel()
		c.validationStopping.Store(true)
		return nil, err
	}
	w.ready.Store(true)
	go w.run()
	return w, nil
}

func (w *CollectionValidationCoordinator) Ready() bool {
	return w != nil && w.ready.Load() && w.ctx.Err() == nil && w.catalog.store.Status().Ready
}

// BeginStop closes new validation admission and requests cooperative cancellation.
// It does not release Store registration or abandon an uncooperative compiler.
func (w *CollectionValidationCoordinator) BeginStop() {
	if w != nil {
		w.catalog.validationStopping.Store(true)
		w.ready.Store(false)
		w.cancel()
	}
}

func (w *CollectionValidationCoordinator) Done() <-chan struct{} { return w.done }

func (w *CollectionValidationCoordinator) Err() error {
	select {
	case <-w.done:
		return w.err
	default:
		return nil
	}
}

// Wait returns the terminal failure only after joining, or the caller's context
// error while ownership remains live. Done distinguishes those two outcomes.
func (w *CollectionValidationCoordinator) Wait(ctx context.Context) error {
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

func (w *CollectionValidationCoordinator) fail(err error) error {
	w.ready.Store(false)
	w.catalog.validationStopping.Store(true)
	// Stop management admission while main supervises this failure. Do not
	// manufacture a storage fault: after the compiler joins, its outstanding
	// commits still need the ordinary durable flush before dependencies close.
	// Actual catalog-integrity/storage failures already mark Store unavailable.
	w.catalog.failed.Store(true)
	return err
}

func (w *CollectionValidationCoordinator) run() {
	w.err = w.loop()
	w.ready.Store(false)
	close(w.done)
}

type collectionValidationAttempt struct {
	item   persistence.CollectionValidationWork
	cancel context.CancelFunc
	done   chan struct{}
	err    error // Published by done; contains no borrowed input.
}

func (w *CollectionValidationCoordinator) loop() error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var active *collectionValidationAttempt
	defer func() {
		w.catalog.validationStopping.Store(true)
		w.ready.Store(false)
		if active != nil {
			active.cancel()
			<-active.done // Keep dependencies owned even when a caller's Wait expires.
		}
	}()
	for {
		if w.ctx.Err() != nil {
			return nil // The original claim survives for the next exclusive startup.
		}
		work, err := w.work(w.ctx)
		if err != nil {
			if w.ctx.Err() != nil {
				return nil
			}
			return w.fail(err)
		}
		if active == nil {
			for _, item := range work {
				if !w.eligible(item, w.now()) || item.Request.Claim != nil {
					continue
				}
				reason, err := w.reason(w.ctx, item)
				if err != nil {
					if w.ctx.Err() != nil {
						return nil
					}
					return w.fail(err)
				}
				if reason != "" {
					if err := w.interrupt(w.ctx, item, reason); err != nil {
						if w.ctx.Err() != nil {
							return nil
						}
						return w.fail(err)
					}
					continue
				}
				ctx, cancel := context.WithCancel(w.ctx)
				active = &collectionValidationAttempt{item: item, cancel: cancel, done: make(chan struct{})}
				go func(attempt *collectionValidationAttempt) {
					defer close(attempt.done)
					_, attempt.err = w.catalog.runCollectionValidation(ctx, attempt.item.OperationID, w.runID, w.now)
				}(active)
				break
			}
		} else {
			item, found := validationWorkByID(work, active.item.OperationID)
			if !found || !w.eligible(item, w.now()) || item.Request.ID != active.item.Request.ID {
				active.cancel()
			} else {
				reason, err := w.reason(w.ctx, item)
				if err != nil {
					if w.ctx.Err() != nil {
						return nil
					}
					return w.fail(err)
				}
				if reason != "" {
					active.cancel()
				}
			}
		}
		var finished <-chan struct{}
		if active != nil {
			finished = active.done
		}
		select {
		case <-w.ctx.Done():
			return nil
		case <-tick.C:
		case <-w.catalog.validationWake:
		case <-finished:
			joined := active
			active = nil
			joined.cancel()
			if w.ctx.Err() != nil {
				return nil
			}
			if err := w.settle(w.ctx, joined); err != nil {
				if w.ctx.Err() != nil {
					return nil
				}
				return w.fail(err)
			}
		}
	}
}

func validationWorkByID(work []persistence.CollectionValidationWork, id string) (persistence.CollectionValidationWork, bool) {
	for _, item := range work {
		if item.OperationID == id {
			return item, true
		}
	}
	return persistence.CollectionValidationWork{}, false
}

func (w *CollectionValidationCoordinator) eligible(item persistence.CollectionValidationWork, at time.Time) bool {
	return (item.Phase == "validating" || item.Phase == "validated") && !item.ResultFinalized && item.Request.Interruption == nil && at.Before(item.ExpiresAt)
}

// A stale/revoked authority is a known retirement reason. Failure to read policy
// is infrastructure unavailability and must not be turned into a resource verdict.
func (w *CollectionValidationCoordinator) reason(ctx context.Context, item persistence.CollectionValidationWork) (string, error) {
	if item.Request.CapabilitiesDigest != w.profile {
		return "capabilitiesChanged", nil
	}
	current, err := retryCollectionRead(ctx, w.pauseLeadership, func() (persistence.OperatorAuthority, error) {
		return w.catalog.store.ObserveOperatorAuthority(ctx, item.Request.Authority.Actor, w.now())
	})
	if errors.Is(err, persistence.ErrOperatorAuthorityDenied) || errors.Is(err, persistence.ErrAuthenticationConflict) {
		return "authorityChanged", nil
	}
	if err != nil {
		return "", err
	}
	if current != item.Request.Authority {
		return "authorityChanged", nil
	}
	return "", nil
}

// settle runs only after the attempt joined. Uncertain or canceled submissions
// can still commit, so join the durable submission prefix before reading progress.
func (w *CollectionValidationCoordinator) settle(ctx context.Context, attempt *collectionValidationAttempt) error {
	if attempt.err != nil {
		if err := w.flushLeadership(ctx); err != nil {
			return err
		}
	}
	work, err := w.work(ctx)
	if err != nil {
		return err
	}
	item, found := validationWorkByID(work, attempt.item.OperationID)
	if !found || !w.eligible(item, w.now()) {
		return nil // Existing finalization, cancellation, expiry or cleanup wins.
	}
	if item.Request.ID != attempt.item.Request.ID || item.Request.Claim != nil && item.Request.Claim.RunID != w.runID {
		return persistence.ErrCollectionConflict
	}
	reason, err := w.reason(ctx, item)
	if err != nil {
		return err
	}
	if reason == "" {
		if item.Request.Claim == nil {
			if persistence.IsLeadershipUnavailable(attempt.err) {
				return nil // Successful FIFO barrier proved that no claim was committed.
			}
			return errors.Join(ErrOutcomeUnconfirmed, attempt.err)
		}
		reason = "validationInterrupted"
	}
	return w.interrupt(ctx, item, reason)
}

// interrupt retains the complete original prefix and identity. A leadership
// retry requires a successful FIFO barrier and never refreshes the original CAS.
func (w *CollectionValidationCoordinator) interrupt(ctx context.Context, item persistence.CollectionValidationWork, reason string) error {
	interruption := persistence.CollectionValidationInterruption{ID: uuid.NewString(), Reason: reason, At: w.now().UTC()}
	command := persistence.CollectionCommand{Action: "validation_interrupt", OperationID: item.OperationID,
		UploadID: item.UploadID, ValidationFence: item.Progress.ValidationRequest, ValidationProgress: &item.Progress, ValidationInterruption: &interruption}
	for {
		_, err := w.catalog.submitCollectionValidation(ctx, command, interruption.At)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrOutcomeUnconfirmed) || persistence.IsLeadershipUnavailable(err) {
			if flushErr := w.flushLeadership(ctx); flushErr != nil {
				return errors.Join(err, flushErr)
			}
		}
		work, readErr := w.work(ctx)
		if readErr != nil {
			return errors.Join(err, readErr)
		}
		current, found := validationWorkByID(work, item.OperationID)
		if !found || !w.eligible(current, w.now()) {
			return nil
		}
		if !persistence.IsLeadershipUnavailable(err) {
			return err
		}
		// The exact original request/progress fences still guard the retry.
	}
}
