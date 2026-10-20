package persistence

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Each handle has one close owner. Retain creates another handle over the same
// immutable sequence; it never opens another database transaction.
type actionReader struct {
	mu     sync.RWMutex
	closed bool
	lease  *actionReaderLease
}

type actionReaderLease struct {
	pin  *actionLedgerPin
	refs atomic.Int64
}

func newActionReader(pin *actionLedgerPin) *actionReader {
	l := &actionReaderLease{pin: pin}
	l.refs.Store(1)
	return &actionReader{lease: l}
}

func (r *actionReader) retain() (*actionReader, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, errCollectionLedgerClosed
	}
	r.lease.refs.Add(1)
	return &actionReader{lease: r.lease}, nil
}

func (r *actionReader) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	if r.lease.refs.Add(-1) == 0 {
		return r.lease.pin.Close()
	}
	return nil
}

func (r *actionReader) get(ctx context.Context, id string) (actionLedgerRow, bool, error) {
	if err := lockActionRead(ctx, &r.mu); err != nil {
		return actionLedgerRow{}, false, err
	}
	defer r.mu.RUnlock()
	if r.closed {
		return actionLedgerRow{}, false, errCollectionLedgerClosed
	}
	return r.lease.pin.Get(ctx, id)
}

func (r *actionReader) page(ctx context.Context, monitorID, after string, limit int) ([]actionLedgerRow, string, error) {
	if err := lockActionRead(ctx, &r.mu); err != nil {
		return nil, "", err
	}
	defer r.mu.RUnlock()
	if r.closed {
		return nil, "", errCollectionLedgerClosed
	}
	return r.lease.pin.Page(ctx, monitorID, after, limit)
}

func lockActionRead(ctx context.Context, lock *sync.RWMutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lock.TryRLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
