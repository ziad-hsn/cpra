package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	bolt "go.etcd.io/bbolt"
)

const (
	maxHistoryEventBytes   = 256 << 10
	maxOperationEventBytes = 64 << 10
)

// operation performs one indexed lookup per retained daily segment, never a
// fleet/event scan. The history watermark excludes partly written future logs.
// Missing/corrupt segments retain the normal explicit unavailable boundary.
func (h *HistoryStore) operation(id string) (OperationReceipt, error) {
	return h.operationContext(context.Background(), id, nil)
}

// operationContext uses the caller's captured FSM index when supplied. The old
// internal wrapper retains its latest-history-watermark behavior. Neither path
// requires Store/FSM locks while reading retained segments.
func (h *HistoryStore) operationContext(ctx context.Context, id string, index *uint64) (OperationReceipt, error) {
	if ctx == nil {
		return OperationReceipt{}, ErrOperationReservation
	}
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return OperationReceipt{}, err
	}
	defer h.mu.RUnlock()
	if h.err != nil || h.closed || index != nil && (h.catalog.Index < *index || len(h.catalog.Segments) > maxOperationSegments) {
		return OperationReceipt{}, ErrHistoryUnavailable
	}
	watermark := h.catalog.Index
	if index != nil {
		watermark = *index
	}
	upper := fmt.Sprintf("%020d:%08d", watermark, 99999999)
	var latest Event
	accept := func(event Event) error {
		if validateOperationEvent(event) != nil || event.Operation.ID != id {
			return ErrHistoryUnavailable
		}
		if event.ID > latest.ID && event.ID <= upper && !event.At.Before(h.catalog.Cutoff) {
			latest = event
		}
		return nil
	}
	if h.dir == "" {
		if event, ok := h.memoryOperations[id]; ok {
			if err := accept(event); err != nil {
				return OperationReceipt{}, err
			}
		}
	} else {
		for day, active := range h.catalog.Segments {
			if err := ctx.Err(); err != nil {
				return OperationReceipt{}, err
			}
			if !active {
				continue
			}
			db := h.databases[day]
			if db == nil {
				return OperationReceipt{}, ErrHistoryUnavailable
			}
			if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
				return OperationReceipt{}, ErrHistoryUnavailable
			}
			if err := db.View(func(tx *bolt.Tx) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				bucket := tx.Bucket([]byte("operations"))
				if bucket == nil {
					return nil
				}
				raw := bucket.Get([]byte(id))
				if raw == nil {
					return nil
				}
				if len(raw) > maxOperationEventBytes {
					return ErrHistoryUnavailable
				}
				var event Event
				if json.Unmarshal(raw, &event) != nil {
					return ErrHistoryUnavailable
				}
				primary := tx.Bucket([]byte("events"))
				if primary == nil || !bytes.Equal(primary.Get([]byte(event.MonitorID+"\x00"+event.ID)), raw) {
					return ErrHistoryUnavailable
				}
				return accept(event)
			}); err != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return OperationReceipt{}, contextErr
				}
				return OperationReceipt{}, ErrHistoryUnavailable
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return OperationReceipt{}, err
	}
	if latest.Operation == nil {
		return OperationReceipt{}, ErrOperationNotFound
	}
	return *latest.Operation, nil
}

func validateOperationEvent(event Event) error {
	if event.Operation == nil || event.Operation.validate() != nil || len(event.ID) != 29 || event.ID[20] != ':' {
		return ErrHistoryUnavailable
	}
	for i, c := range event.ID {
		if i != 20 && (c < '0' || c > '9') {
			return ErrHistoryUnavailable
		}
	}
	want := receiptEvent(*event.Operation)
	want.ID = event.ID
	if !reflect.DeepEqual(want, event) {
		return ErrHistoryUnavailable
	}
	return nil
}

// Validate the derived index against its authoritative events once at startup
// and during stopped backup inspection. Missing indexes are an explicit error,
// not a claim that committed receipt history never existed.
func validateHistoryOperations(db *bolt.DB) error {
	return validateHistoryOperationsContext(context.Background(), db)
}

func validateHistoryOperationsContext(ctx context.Context, db *bolt.DB) error {
	return db.View(func(tx *bolt.Tx) error {
		if err := validateHistoryExecutionPublicationIndex(ctx, tx); err != nil {
			return err
		}
		if err := validateHistoryExecutionAnchorIndex(ctx, tx); err != nil {
			return err
		}
		if err := validateHistoryValidationIndex(ctx, tx); err != nil {
			return err
		}
		if err := validateHistoryCollectionIndex(ctx, tx); err != nil {
			return err
		}
		if err := validateOperationMonitorIndexContext(ctx, tx); err != nil {
			return err
		}
		events, operations := tx.Bucket([]byte("events")), tx.Bucket([]byte("operations"))
		if events == nil {
			if operations != nil && operations.Stats().KeyN != 0 {
				return ErrHistoryUnavailable
			}
			return nil
		}
		if operations != nil {
			if err := operations.ForEach(func(id, raw []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if len(id) > 256 || len(raw) > maxOperationEventBytes {
					return ErrHistoryUnavailable
				}
				var event Event
				if json.Unmarshal(raw, &event) != nil || validateOperationEvent(event) != nil ||
					string(id) != event.Operation.ID || !bytes.Equal(events.Get([]byte(event.MonitorID+"\x00"+event.ID)), raw) {
					return ErrHistoryUnavailable
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return events.ForEach(func(key, raw []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(key) > 1024 || len(raw) > maxHistoryEventBytes {
				return ErrHistoryUnavailable
			}
			var event Event
			if json.Unmarshal(raw, &event) != nil {
				return ErrHistoryUnavailable
			}
			if err := validateHistoryExecutionPublicationEvent(tx, key, raw, event); err != nil {
				return err
			}
			if err := validateHistoryExecutionAnchor(tx, key, raw, event); err != nil {
				return err
			}
			if err := validateHistoryValidationEvent(tx, key, raw, event); err != nil {
				return err
			}
			if err := validateHistoryCollectionEvent(tx, key, raw, event); err != nil {
				return err
			}
			if event.Operation == nil {
				return nil
			}
			if len(raw) > maxOperationEventBytes || validateOperationEvent(event) != nil || string(key) != event.MonitorID+"\x00"+event.ID || operations == nil {
				return ErrHistoryUnavailable
			}
			var indexed Event
			indexedRaw := operations.Get([]byte(event.Operation.ID))
			if len(indexedRaw) > maxOperationEventBytes || json.Unmarshal(indexedRaw, &indexed) != nil || indexed.ID < event.ID {
				return ErrHistoryUnavailable
			}
			return nil
		})
	})
}

// Called only while holding h.mu for writing; reads keep an O(1) memory lookup.
func (h *HistoryStore) expireMemoryOperations() {
	for id, event := range h.memoryOperations {
		if event.At.Before(h.catalog.Cutoff) {
			h.unindexMemoryOperation(id, event)
			delete(h.memoryOperations, id)
		}
	}
}
