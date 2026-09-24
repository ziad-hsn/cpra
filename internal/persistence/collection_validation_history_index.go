package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"time"

	bolt "go.etcd.io/bbolt"
)

var collectionValidationHistoryBucket = []byte("collection_validation_results")
var collectionValidationHistoryProgressKey = []byte("progress")
var collectionValidationHistorySummaryKey = []byte("summary")
var collectionValidationHistoryItemsKey = []byte("items")

type collectionValidationHistoryMemory struct {
	progress collectionValidationHistoryProgress
	items    map[uint64][]byte
	summary  []byte
}

func decodeCollectionValidationHistoryProgress(raw []byte) (collectionValidationHistoryProgress, error) {
	var p collectionValidationHistoryProgress
	if len(raw) == 0 || len(raw) > 2048 || collectionPlanJSON(context.Background(), raw) != nil {
		return p, ErrHistoryUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || p.validate() != nil {
		return p, ErrHistoryUnavailable
	}
	canonical, _ := json.Marshal(p)
	if !bytes.Equal(raw, canonical) {
		return p, ErrHistoryUnavailable
	}
	return p, nil
}

// Shares the transaction with the primary event. The summary is published only
// after the exact canonical item inventory is complete, not merely after a count.
func writeCollectionValidationHistory(tx *bolt.Tx, e Event, raw []byte) error {
	decoded, err := decodeCollectionValidationHistoryEvent(raw)
	if err != nil || decoded.ID != e.ID {
		return ErrHistoryUnavailable
	}
	v := decoded.CollectionValidation
	root, err := tx.CreateBucketIfNotExists(collectionValidationHistoryBucket)
	if err != nil {
		return err
	}
	b, err := root.CreateBucketIfNotExists([]byte(v.OperationID))
	if err != nil {
		return err
	}
	var p *collectionValidationHistoryProgress
	if prior := b.Get(collectionValidationHistoryProgressKey); prior != nil {
		x, err := decodeCollectionValidationHistoryProgress(prior)
		if err != nil {
			return err
		}
		p = &x
	}
	var old []byte
	if v.Item != nil {
		if items := b.Bucket(collectionValidationHistoryItemsKey); items != nil {
			old = items.Get(collectionOrdinal(v.Item.Ordinal))
		}
	} else {
		old = b.Get(collectionValidationHistorySummaryKey)
	}
	if old != nil {
		if p == nil || !bytes.Equal(old, raw) {
			return ErrHistoryUnavailable
		}
		return nil
	}
	next, err := advanceCollectionValidationHistory(p, decoded)
	if err != nil {
		return err
	}
	if v.Item != nil {
		items, err := b.CreateBucketIfNotExists(collectionValidationHistoryItemsKey)
		if err != nil {
			return err
		}
		if err := items.Put(collectionOrdinal(v.Item.Ordinal), raw); err != nil {
			return err
		}
	} else if err := b.Put(collectionValidationHistorySummaryKey, raw); err != nil {
		return err
	}
	encoded, _ := json.Marshal(next)
	return b.Put(collectionValidationHistoryProgressKey, encoded)
}

// Preflight the entire memory append before changing primary events or indexes.
// Only affected ordinal maps are copied, once per batch; item buffers are owned.
func (h *HistoryStore) prepareMemoryValidationHistory(events []Event) (map[string]*collectionValidationHistoryMemory, error) {
	pending := make(map[string]*collectionValidationHistoryMemory)
	for _, e := range events {
		if e.CollectionValidation == nil || e.At.Before(h.catalog.Cutoff) {
			continue
		}
		if validateCollectionValidationHistoryEvent(e) != nil {
			return nil, ErrHistoryUnavailable
		}
		v := e.CollectionValidation
		m := pending[v.OperationID]
		if m == nil {
			m = &collectionValidationHistoryMemory{items: make(map[uint64][]byte)}
			if prior := h.memoryValidationResults[v.OperationID]; prior != nil {
				*m = *prior
				m.items = maps.Clone(prior.items)
			}
			pending[v.OperationID] = m
		}
		raw, err := json.Marshal(e)
		if err != nil || len(raw) > maxCollectionValidationHistoryEventBytes {
			return nil, ErrHistoryUnavailable
		}
		old := m.summary
		if v.Item != nil {
			old = m.items[v.Item.Ordinal]
		}
		if old != nil {
			if !bytes.Equal(old, raw) {
				return nil, ErrHistoryUnavailable
			}
			continue
		}
		var prior *collectionValidationHistoryProgress
		if m.progress.ResultID != "" {
			prior = &m.progress
		}
		next, err := advanceCollectionValidationHistory(prior, e)
		if err != nil {
			return nil, err
		}
		m.progress = next
		if v.Item != nil {
			m.items[v.Item.Ordinal] = raw
		} else {
			m.summary = raw
		}
	}
	return pending, nil
}

func (h *HistoryStore) expireMemoryValidationHistory() {
	for id, m := range h.memoryValidationResults {
		if m.progress.FinalizedAt.Before(h.catalog.Cutoff) {
			delete(h.memoryValidationResults, id)
		}
	}
}

func validationHistoryPrimary(tx *bolt.Tx, e Event, raw []byte) error {
	b := tx.Bucket([]byte("events"))
	if b == nil || !bytes.Equal(b.Get([]byte(e.MonitorID+"\x00"+e.ID)), raw) {
		return ErrHistoryUnavailable
	}
	return nil
}

// Startup and offline inspection recompute every result prefix in bounded
// ordinal order. Incomplete publication is valid storage, but never a receipt.
func validateHistoryValidationIndex(ctx context.Context, tx *bolt.Tx) error {
	root := tx.Bucket(collectionValidationHistoryBucket)
	if root == nil {
		return nil
	}
	return root.ForEach(func(id, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := ParseOperationHandle(string(id)); err != nil || value != nil {
			return ErrHistoryUnavailable
		}
		b := root.Bucket(id)
		if b == nil {
			return ErrHistoryUnavailable
		}
		stored, err := decodeCollectionValidationHistoryProgress(b.Get(collectionValidationHistoryProgressKey))
		if err != nil {
			return err
		}
		if err := b.ForEach(func(k, v []byte) error {
			switch string(k) {
			case "progress", "summary":
				if v == nil {
					return ErrHistoryUnavailable
				}
			case "items":
				if v != nil {
					return ErrHistoryUnavailable
				}
			default:
				return ErrHistoryUnavailable
			}
			return nil
		}); err != nil {
			return err
		}
		var progress *collectionValidationHistoryProgress
		accept := func(raw []byte, ordinal uint64) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := decodeCollectionValidationHistoryEvent(raw)
			if err != nil || e.CollectionValidation.OperationID != string(id) {
				return ErrHistoryUnavailable
			}
			if ordinal == 0 {
				if e.CollectionValidation.Summary == nil {
					return ErrHistoryUnavailable
				}
			} else if e.CollectionValidation.Item == nil || e.CollectionValidation.Item.Ordinal != ordinal {
				return ErrHistoryUnavailable
			}
			if err := validationHistoryPrimary(tx, e, raw); err != nil {
				return err
			}
			next, err := advanceCollectionValidationHistory(progress, e)
			if err != nil {
				return err
			}
			progress = &next
			return nil
		}
		if items := b.Bucket(collectionValidationHistoryItemsKey); items != nil {
			var ordinal uint64
			if err := items.ForEach(func(key, raw []byte) error {
				ordinal++
				if !bytes.Equal(key, collectionOrdinal(ordinal)) || raw == nil {
					return ErrHistoryUnavailable
				}
				return accept(raw, ordinal)
			}); err != nil {
				return err
			}
		}
		if raw := b.Get(collectionValidationHistorySummaryKey); raw != nil {
			if err := accept(raw, 0); err != nil {
				return err
			}
		}
		if progress == nil || !collectionValidationHistoryProgressEqual(*progress, stored) {
			return ErrHistoryUnavailable
		}
		return nil
	})
}

func collectionValidationHistoryProgressEqual(a, b collectionValidationHistoryProgress) bool {
	at, bt := a.FinalizedAt, b.FinalizedAt
	a.FinalizedAt = time.Time{}
	b.FinalizedAt = time.Time{}
	return a == b && at.Equal(bt)
}

// Validate the reverse edge as well: a dropped secondary bucket or stripped
// typed payload cannot make primary result evidence look like absent history.
func validateHistoryValidationEvent(tx *bolt.Tx, key, raw []byte, e Event) error {
	_, reserved := collectionValidationHistoryIdentity(e)
	if e.CollectionValidation == nil {
		if reserved {
			return ErrHistoryUnavailable
		}
		return nil
	}
	if validateCollectionValidationHistoryEvent(e) != nil || len(raw) > maxCollectionValidationHistoryEventBytes || string(key) != e.MonitorID+"\x00"+e.ID {
		return ErrHistoryUnavailable
	}
	root := tx.Bucket(collectionValidationHistoryBucket)
	if root == nil {
		return ErrHistoryUnavailable
	}
	b := root.Bucket([]byte(e.CollectionValidation.OperationID))
	if b == nil {
		return ErrHistoryUnavailable
	}
	var indexed []byte
	if e.CollectionValidation.Item != nil {
		if items := b.Bucket(collectionValidationHistoryItemsKey); items != nil {
			indexed = items.Get(collectionOrdinal(e.CollectionValidation.Item.Ordinal))
		}
	} else {
		indexed = b.Get(collectionValidationHistorySummaryKey)
	}
	if !bytes.Equal(indexed, raw) {
		return ErrHistoryUnavailable
	}
	return nil
}
