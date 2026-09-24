package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// retryCollectionRead retries only reads and FIFO barriers across temporary
// leadership changes. It never retries a resource mutation or compiler attempt.
func retryCollectionRead[T any](ctx context.Context, pause func(bool), read func() (T, error)) (T, error) {
	for {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		value, err := read()
		if !persistence.IsLeadershipUnavailable(err) {
			if err == nil {
				pause(false)
			}
			return value, err
		}
		pause(true)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			var zero T
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *CollectionValidationCoordinator) pauseLeadership(paused bool) {
	w.ready.Store(!paused && w.ctx.Err() == nil)
}

func (w *CollectionExecutionCoordinator) pauseLeadership(paused bool) {
	w.ready.Store(!paused && w.ctx.Err() == nil)
}

func (w *CollectionValidationCoordinator) work(ctx context.Context) ([]persistence.CollectionValidationWork, error) {
	return retryCollectionRead(ctx, w.pauseLeadership, func() ([]persistence.CollectionValidationWork, error) {
		return w.catalog.store.CollectionValidationWork(ctx, w.now())
	})
}

func (w *CollectionExecutionCoordinator) work(ctx context.Context) ([]persistence.CollectionExecutionWork, error) {
	return retryCollectionRead(ctx, w.pauseLeadership, func() ([]persistence.CollectionExecutionWork, error) {
		return w.catalog.store.CollectionExecutionWork(ctx, w.now())
	})
}

func (w *CollectionValidationCoordinator) flushLeadership(ctx context.Context) error {
	_, err := retryCollectionRead(ctx, w.pauseLeadership, func() (struct{}, error) {
		return struct{}{}, w.catalog.store.Flush(ctx)
	})
	return err
}
