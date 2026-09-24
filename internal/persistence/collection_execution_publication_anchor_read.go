package persistence

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	bolt "go.etcd.io/bbolt"
)

// collectionExecutionAnchor validates retained finalization independently of the
// earlier cancellation receipt. The caller checks ownership before this read.
func (h *HistoryStore) collectionExecutionAnchor(ctx context.Context, expected CollectionExecutionSummary, index uint64, at time.Time) error {
	if ctx == nil || expected.validate() != nil || at.IsZero() || at.Before(expected.FinalizedAt) {
		return ErrCollectionInvalid
	}
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return err
	}
	defer h.mu.RUnlock()
	return h.collectionExecutionAnchorLocked(ctx, expected, index, at)
}

// collectionExecutionAnchorLocked is the bounded metadata variant for callers
// that already own history.mu. It never acquires Store or FSM locks.
func (h *HistoryStore) collectionExecutionAnchorLocked(ctx context.Context, expected CollectionExecutionSummary, index uint64, at time.Time) error {
	if ctx == nil || expected.validate() != nil || at.IsZero() || at.Before(expected.FinalizedAt) {
		return ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.closed || h.err != nil || h.catalog.Index < index || len(h.catalog.Segments) > maxOperationSegments {
		return ErrHistoryUnavailable
	}
	if !at.Before(expected.FinalizedAt.AddDate(0, 0, 30)) || !h.catalog.Cutoff.IsZero() && !expected.FinalizedAt.After(h.catalog.Cutoff) {
		return ErrOperationExpired
	}
	upper := fmt.Sprintf("%020d:%08d", index, 99999999)
	valid := func(e Event) error {
		if validateCollectionExecutionResultEvent(e) != nil || e.ID > upper || !collectionExecutionSummariesEqual(e.CollectionExecution, &expected) {
			return ErrHistoryUnavailable
		}
		return nil
	}
	if h.dir == "" {
		e, ok := h.memoryExecutionAnchors[expected.Binding.OperationID]
		primary, primaryOK := h.memoryExecutionEvidence[expected.Binding.OperationID]
		if !ok || !primaryOK || !reflect.DeepEqual(e, primary.anchor) {
			return ErrHistoryUnavailable
		}
		if err := valid(e); err != nil {
			return err
		}
		return ctx.Err()
	}
	found := false
	for day, active := range h.catalog.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !active {
			continue
		}
		db := h.databases[day]
		if db == nil {
			return ErrHistoryUnavailable
		}
		if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
			return ErrHistoryUnavailable
		}
		err := db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket(collectionExecutionAnchorBucket)
			if b == nil {
				return nil
			}
			raw := b.Get([]byte(expected.Binding.OperationID))
			if raw == nil {
				return nil
			}
			if found {
				return ErrHistoryUnavailable
			}
			found = true
			e, err := decodeCollectionExecutionResultEvent(raw)
			if err != nil {
				return err
			}
			if err := valid(e); err != nil {
				return err
			}
			primary := tx.Bucket([]byte("events"))
			if primary == nil || !bytes.Equal(primary.Get([]byte(e.MonitorID+"\x00"+e.ID)), raw) {
				return ErrHistoryUnavailable
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if !found {
		return ErrHistoryUnavailable
	}
	return ctx.Err()
}
