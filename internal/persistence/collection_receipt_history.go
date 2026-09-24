package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var collectionReceiptBucket = []byte("collection_receipts")

const maxCollectionReceiptEventBytes = 16 << 10

func validateCollectionReceiptEvent(event Event) error {
	if event.Collection == nil || !collectionTerminal(event.Collection.Phase) || event.Collection.validate() != nil || !collectionEventPosition(event.ID) {
		return ErrHistoryUnavailable
	}
	want := collectionReceiptEvent(*event.Collection)
	want.ID = event.ID
	if !reflect.DeepEqual(want, event) {
		return ErrHistoryUnavailable
	}
	return nil
}

func collectionEventPosition(id string) bool {
	if len(id) != 29 || id[20] != ':' {
		return false
	}
	for i, c := range id {
		if i != 20 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func collectionHistoryIdentity(event Event) (string, bool) {
	if !strings.HasPrefix(event.MonitorID, "collection/") {
		return "", false
	}
	id := strings.TrimPrefix(event.MonitorID, "collection/")
	if _, _, err := ParseOperationHandle(id); err != nil {
		return "", false
	}
	return id, true
}

// writeCollectionReceipt shares the transaction with the authoritative event.
// One inactive upload has one immutable terminal outcome; replay writes the same
// event bytes. Conflicting later evidence is corruption, never last-write-wins.
func writeCollectionReceipt(tx *bolt.Tx, event Event, raw []byte) error {
	if validateCollectionReceiptEvent(event) != nil || len(raw) > maxCollectionReceiptEventBytes {
		return ErrHistoryUnavailable
	}
	b, err := tx.CreateBucketIfNotExists(collectionReceiptBucket)
	if err != nil {
		return err
	}
	key := []byte(event.Collection.ID)
	if old := b.Get(key); old != nil && !bytes.Equal(old, raw) {
		return ErrHistoryUnavailable
	}
	return b.Put(key, raw)
}

func validateHistoryCollectionIndex(ctx context.Context, tx *bolt.Tx) error {
	b := tx.Bucket(collectionReceiptBucket)
	if b == nil {
		return nil
	}
	primary := tx.Bucket([]byte("events"))
	return b.ForEach(func(id, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(id) > 256 || len(raw) > maxCollectionReceiptEventBytes || primary == nil {
			return ErrHistoryUnavailable
		}
		var event Event
		if json.Unmarshal(raw, &event) != nil || validateCollectionReceiptEvent(event) != nil || string(id) != event.Collection.ID ||
			!bytes.Equal(primary.Get([]byte(event.MonitorID+"\x00"+event.ID)), raw) {
			return ErrHistoryUnavailable
		}
		return nil
	})
}

// Legacy private terminal audit events are preserved. They lack original item
// counts and cannot be retroactively promoted into complete terminal receipts.
func validateHistoryCollectionEvent(tx *bolt.Tx, key, raw []byte, event Event) error {
	if event.Collection == nil {
		return nil
	}
	if len(raw) > maxCollectionReceiptEventBytes || validateCollectionReceiptEvent(event) != nil || string(key) != event.MonitorID+"\x00"+event.ID {
		return ErrHistoryUnavailable
	}
	b := tx.Bucket(collectionReceiptBucket)
	if b == nil || !bytes.Equal(b.Get([]byte(event.Collection.ID)), raw) {
		return ErrHistoryUnavailable
	}
	return nil
}

// collectionReceipt performs bounded point lookups in retained daily segments.
// A primary-prefix seek detects a missing new index or retained legacy evidence
// without scanning unrelated events. The FSM watermark excludes ahead-of-replay
// materialization; no history-derived result is used to decide a mutation.
func (h *HistoryStore) collectionReceipt(ctx context.Context, id string, index uint64, at time.Time) (CollectionReceipt, error) {
	return h.collectionReceiptExpected(ctx, id, index, at, nil)
}

func (h *HistoryStore) collectionReceiptExpected(ctx context.Context, id string, index uint64, at time.Time, expected *CollectionReceipt) (CollectionReceipt, error) {
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return CollectionReceipt{}, err
	}
	defer h.mu.RUnlock()
	if h.err != nil || h.closed || len(h.catalog.Segments) > maxOperationSegments || h.catalog.Index < index {
		return CollectionReceipt{}, ErrHistoryUnavailable
	}
	upper := fmt.Sprintf("%020d:%08d", min(index, h.catalog.Index), 99999999)
	cutoff := at.UTC().AddDate(0, 0, -30)
	if h.catalog.Cutoff.After(cutoff) {
		cutoff = h.catalog.Cutoff
	}
	var result *CollectionReceipt
	accept := func(event Event) error {
		if event.ID > upper || event.At.Before(cutoff) {
			return nil
		}
		if event.Collection == nil || validateCollectionReceiptEvent(event) != nil || event.Collection.ID != id {
			return ErrHistoryUnavailable
		}
		if result != nil && !reflect.DeepEqual(*result, *event.Collection) {
			return ErrHistoryUnavailable
		}
		copy := event.Collection.Clone()
		result = &copy
		return nil
	}
	if h.dir == "" {
		if event, ok := h.memoryCollectionEvidence[id]; ok {
			if event.MonitorID != "collection/"+id || !collectionEventPosition(event.ID) || event.At.IsZero() {
				return CollectionReceipt{}, ErrHistoryUnavailable
			}
			indexed, exists := h.memoryCollectionReceipts[id]
			if event.ID <= upper && !event.At.Before(cutoff) && (!exists || !reflect.DeepEqual(indexed, event)) {
				return CollectionReceipt{}, ErrHistoryUnavailable
			}
			if err := accept(event); err != nil {
				return CollectionReceipt{}, err
			}
		} else if _, exists := h.memoryCollectionReceipts[id]; exists {
			return CollectionReceipt{}, ErrHistoryUnavailable
		}
	} else {
		prefix := []byte("collection/" + id + "\x00")
		for day, active := range h.catalog.Segments {
			if err := ctx.Err(); err != nil {
				return CollectionReceipt{}, err
			}
			if !active {
				continue
			}
			db := h.databases[day]
			if db == nil {
				return CollectionReceipt{}, ErrHistoryUnavailable
			}
			if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
				return CollectionReceipt{}, ErrHistoryUnavailable
			}
			err := db.View(func(tx *bolt.Tx) error {
				primary := tx.Bucket([]byte("events"))
				var evidenceKey, evidence []byte
				if primary != nil {
					key, raw := primary.Cursor().Seek(prefix)
					if bytes.HasPrefix(key, prefix) {
						evidenceKey, evidence = key, raw
					}
				}
				var raw []byte
				if b := tx.Bucket(collectionReceiptBucket); b != nil {
					raw = b.Get([]byte(id))
				}
				if raw == nil {
					if evidence == nil {
						return nil
					}
					if len(evidence) > maxHistoryEventBytes {
						return ErrHistoryUnavailable
					}
					var event Event
					if json.Unmarshal(evidence, &event) != nil || event.MonitorID != "collection/"+id || !collectionEventPosition(event.ID) ||
						event.At.IsZero() || string(evidenceKey) != event.MonitorID+"\x00"+event.ID {
						return ErrHistoryUnavailable
					}
					if event.ID <= upper && !event.At.Before(cutoff) {
						return ErrHistoryUnavailable
					}
					return nil
				}
				if len(raw) > maxCollectionReceiptEventBytes || primary == nil {
					return ErrHistoryUnavailable
				}
				var event Event
				if json.Unmarshal(raw, &event) != nil || validateCollectionReceiptEvent(event) != nil || event.Collection.ID != id ||
					!bytes.Equal(primary.Get([]byte(event.MonitorID+"\x00"+event.ID)), raw) {
					return ErrHistoryUnavailable
				}
				return accept(event)
			})
			if err != nil {
				return CollectionReceipt{}, ErrHistoryUnavailable
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return CollectionReceipt{}, err
	}
	if result == nil {
		if expected != nil && !expected.TerminalAt.Before(cutoff) {
			return CollectionReceipt{}, ErrHistoryUnavailable
		}
		return CollectionReceipt{}, ErrOperationNotFound
	}
	return *result, nil
}

func (h *HistoryStore) expireMemoryCollections() {
	for id, event := range h.memoryCollectionEvidence {
		if event.At.Before(h.catalog.Cutoff) {
			if h.memoryCollectionTree != nil {
				h.memoryCollectionTree.Delete(operationMemoryItem{key: id})
			}
			delete(h.memoryCollectionEvidence, id)
			delete(h.memoryCollectionReceipts, id)
		}
	}
}

// indexMemoryCollection maintains a derived ordered view over primary collection
// evidence at append, never by sorting the retained map during a page read.
func (h *HistoryStore) indexMemoryCollection(id string, event Event) {
	if h.memoryCollectionTree == nil {
		h.memoryCollectionTree = newOperationMemoryTree()
	}
	h.memoryCollectionTree.ReplaceOrInsert(operationMemoryItem{key: id, event: event})
}
