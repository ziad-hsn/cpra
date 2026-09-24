package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

var collectionExecutionPublicationBucket = []byte("collection_execution_results")
var collectionExecutionPublicationProgressKey = []byte("progress")
var collectionExecutionPublicationSummaryKey = []byte("summary")
var collectionExecutionPublicationItemsKey = []byte("items")

type collectionExecutionPublicationMemory struct {
	progress collectionExecutionPublicationProgress
	items    map[uint64][]byte
	summary  []byte
}

func decodeCollectionExecutionPublicationProgress(raw []byte) (collectionExecutionPublicationProgress, error) {
	var p collectionExecutionPublicationProgress
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
func writeCollectionExecutionPublication(tx *bolt.Tx, e Event, raw []byte) error {
	decoded, err := decodeCollectionExecutionPublicationEvent(raw)
	if err != nil || decoded.ID != e.ID {
		return ErrHistoryUnavailable
	}
	v := decoded.CollectionExecutionHistory
	if err := validateExecutionPublicationAnchor(tx, decoded); err != nil {
		return err
	}
	root, err := tx.CreateBucketIfNotExists(collectionExecutionPublicationBucket)
	if err != nil {
		return err
	}
	b, err := root.CreateBucketIfNotExists([]byte(v.OperationID))
	if err != nil {
		return err
	}
	var p *collectionExecutionPublicationProgress
	if prior := b.Get(collectionExecutionPublicationProgressKey); prior != nil {
		x, err := decodeCollectionExecutionPublicationProgress(prior)
		if err != nil {
			return err
		}
		p = &x
	}
	var old []byte
	if v.Item != nil {
		if items := b.Bucket(collectionExecutionPublicationItemsKey); items != nil {
			old = items.Get(collectionOrdinal(v.Item.InputOrdinal))
		}
	} else {
		old = b.Get(collectionExecutionPublicationSummaryKey)
	}
	if old != nil {
		if p == nil || !bytes.Equal(old, raw) {
			return ErrHistoryUnavailable
		}
		return nil
	}
	next, err := advanceCollectionExecutionPublication(p, decoded)
	if err != nil {
		return err
	}
	if v.Item != nil {
		items, err := b.CreateBucketIfNotExists(collectionExecutionPublicationItemsKey)
		if err != nil {
			return err
		}
		if err := items.Put(collectionOrdinal(v.Item.InputOrdinal), raw); err != nil {
			return err
		}
	} else if err := b.Put(collectionExecutionPublicationSummaryKey, raw); err != nil {
		return err
	}
	encoded, _ := json.Marshal(next)
	return b.Put(collectionExecutionPublicationProgressKey, encoded)
}

// Preflight the entire memory append before changing primary events or indexes.
// Existing rows remain immutable; only this bounded batch is staged as a delta.
func (h *HistoryStore) prepareMemoryExecutionHistory(events []Event) (map[string]*collectionExecutionPublicationMemory, error) {
	pending := make(map[string]*collectionExecutionPublicationMemory)
	for _, e := range events {
		if e.CollectionExecutionHistory == nil || e.At.Before(h.catalog.Cutoff) {
			continue
		}
		if validateCollectionExecutionPublicationEvent(e) != nil {
			return nil, ErrHistoryUnavailable
		}
		v := e.CollectionExecutionHistory
		anchor, ok := h.memoryExecutionAnchors[v.OperationID]
		if !ok || executionPublicationMatchesAnchor(e, anchor) != nil {
			return nil, ErrHistoryUnavailable
		}
		m := pending[v.OperationID]
		if m == nil {
			m = &collectionExecutionPublicationMemory{items: make(map[uint64][]byte)}
			if prior := h.memoryExecutionResults[v.OperationID]; prior != nil {
				m.progress, m.summary = prior.progress, prior.summary
			}
			pending[v.OperationID] = m
		}
		raw, err := json.Marshal(e)
		if err != nil || len(raw) > maxCollectionExecutionPublicationEventBytes {
			return nil, ErrHistoryUnavailable
		}
		old := m.summary
		if v.Item != nil {
			old = m.items[v.Item.InputOrdinal]
			if old == nil {
				if previous := h.memoryExecutionResults[v.OperationID]; previous != nil {
					old = previous.items[v.Item.InputOrdinal]
				}
			}
		}
		if old != nil {
			if !bytes.Equal(old, raw) {
				return nil, ErrHistoryUnavailable
			}
			continue
		}
		var prior *collectionExecutionPublicationProgress
		if m.progress.ResultID != "" {
			prior = &m.progress
		}
		next, err := advanceCollectionExecutionPublication(prior, e)
		if err != nil {
			return nil, err
		}
		m.progress = next
		if v.Item != nil {
			m.items[v.Item.InputOrdinal] = raw
		} else {
			m.summary = raw
		}
	}
	return pending, nil
}

func (h *HistoryStore) expireMemoryExecutionHistory() {
	for id, m := range h.memoryExecutionResults {
		if m.progress.FinalizedAt.Before(h.catalog.Cutoff) {
			delete(h.memoryExecutionResults, id)
		}
	}
}

func executionHistoryPrimary(tx *bolt.Tx, e Event, raw []byte) error {
	b := tx.Bucket([]byte("events"))
	if b == nil || !bytes.Equal(b.Get([]byte(e.MonitorID+"\x00"+e.ID)), raw) {
		return ErrHistoryUnavailable
	}
	return nil
}

// Startup and offline inspection recompute every result prefix in bounded
// ordinal order. Incomplete publication is valid storage, but never a receipt.
func validateHistoryExecutionPublicationIndex(ctx context.Context, tx *bolt.Tx) error {
	root := tx.Bucket(collectionExecutionPublicationBucket)
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
		stored, err := decodeCollectionExecutionPublicationProgress(b.Get(collectionExecutionPublicationProgressKey))
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
		var progress *collectionExecutionPublicationProgress
		accept := func(raw []byte, ordinal uint64) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := decodeCollectionExecutionPublicationEvent(raw)
			if err != nil || e.CollectionExecutionHistory.OperationID != string(id) {
				return ErrHistoryUnavailable
			}
			if ordinal == 0 {
				if e.CollectionExecutionHistory.Seal == nil {
					return ErrHistoryUnavailable
				}
			} else if e.CollectionExecutionHistory.Item == nil || e.CollectionExecutionHistory.Item.InputOrdinal != ordinal {
				return ErrHistoryUnavailable
			}
			if err := validateExecutionPublicationAnchor(tx, e); err != nil {
				return err
			}
			if err := executionHistoryPrimary(tx, e, raw); err != nil {
				return err
			}
			next, err := advanceCollectionExecutionPublication(progress, e)
			if err != nil {
				return err
			}
			progress = &next
			return nil
		}
		if items := b.Bucket(collectionExecutionPublicationItemsKey); items != nil {
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
		if raw := b.Get(collectionExecutionPublicationSummaryKey); raw != nil {
			if err := accept(raw, 0); err != nil {
				return err
			}
		}
		if progress == nil || !collectionExecutionPublicationProgressEqual(*progress, stored) {
			return ErrHistoryUnavailable
		}
		return nil
	})
}

func collectionExecutionPublicationProgressEqual(a, b collectionExecutionPublicationProgress) bool {
	at, bt := a.FinalizedAt, b.FinalizedAt
	a.FinalizedAt = time.Time{}
	b.FinalizedAt = time.Time{}
	return a == b && at.Equal(bt)
}

// Validate the reverse edge as well: a dropped secondary bucket or stripped
// typed payload cannot make primary result evidence look like absent history.
func validateHistoryExecutionPublicationEvent(tx *bolt.Tx, key, raw []byte, e Event) error {
	_, reserved := collectionExecutionPublicationIdentity(e)
	if e.CollectionExecutionHistory == nil {
		if reserved && e.CollectionExecution == nil {
			return ErrHistoryUnavailable
		}
		return nil
	}
	if validateCollectionExecutionPublicationEvent(e) != nil || len(raw) > maxCollectionExecutionPublicationEventBytes || string(key) != e.MonitorID+"\x00"+e.ID {
		return ErrHistoryUnavailable
	}
	root := tx.Bucket(collectionExecutionPublicationBucket)
	if root == nil {
		return ErrHistoryUnavailable
	}
	b := root.Bucket([]byte(e.CollectionExecutionHistory.OperationID))
	if b == nil {
		return ErrHistoryUnavailable
	}
	var indexed []byte
	if e.CollectionExecutionHistory.Item != nil {
		if items := b.Bucket(collectionExecutionPublicationItemsKey); items != nil {
			indexed = items.Get(collectionOrdinal(e.CollectionExecutionHistory.Item.InputOrdinal))
		}
	} else {
		indexed = b.Get(collectionExecutionPublicationSummaryKey)
	}
	if !bytes.Equal(indexed, raw) {
		return ErrHistoryUnavailable
	}
	return nil
}

// Publication retains the finalization cohort and source identity from its anchor.
func executionPublicationMatchesAnchor(event, anchor Event) error {
	if validateCollectionExecutionPublicationEvent(event) != nil || validateCollectionExecutionResultEvent(anchor) != nil || event.ID <= anchor.ID {
		return ErrHistoryUnavailable
	}
	v, summary := event.CollectionExecutionHistory, anchor.CollectionExecution
	if v.OperationID != summary.Binding.OperationID || v.ResultID != summary.Binding.ActivationID || !v.FinalizedAt.Equal(summary.FinalizedAt) {
		return ErrHistoryUnavailable
	}
	if v.Item != nil {
		return v.Item.validateSummary(*summary)
	}
	if !collectionExecutionSummariesEqual(&v.Seal.Summary, summary) {
		return ErrHistoryUnavailable
	}
	return nil
}

func validateExecutionPublicationAnchor(tx *bolt.Tx, event Event) error {
	anchors := tx.Bucket(collectionExecutionAnchorBucket)
	if anchors == nil || event.CollectionExecutionHistory == nil {
		return ErrHistoryUnavailable
	}
	raw := anchors.Get([]byte(event.CollectionExecutionHistory.OperationID))
	anchor, err := decodeCollectionExecutionResultEvent(raw)
	if err != nil {
		return err
	}
	if err := executionHistoryPrimary(tx, anchor, raw); err != nil {
		return err
	}
	return executionPublicationMatchesAnchor(event, anchor)
}
