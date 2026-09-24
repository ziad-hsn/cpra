package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"

	bolt "go.etcd.io/bbolt"
)

const maxCollectionExecutionResultEventBytes = 16 << 10

// This anchor is immutable summary evidence. It is deliberately distinct from
// the future item/seal events; its presence never asserts result availability.
func collectionExecutionResultEvent(summary CollectionExecutionSummary) Event {
	summary = summary.Clone()
	return Event{MonitorID: "collection-execution/" + summary.Binding.OperationID, At: summary.FinalizedAt,
		Type: "collection_execution_anchor", Kind: "Collection", Actor: summary.Actor, Revision: summary.ContentDigest,
		ActionID: summary.Binding.ActivationID, Outcome: summary.Outcome, CollectionExecution: &summary}
}

func collectionExecutionHistoryEvent(e Event) bool {
	return e.CollectionExecution != nil || e.CollectionExecutionHistory != nil || strings.HasPrefix(e.MonitorID, "collection-execution/")
}

func validateCollectionExecutionResultEvent(e Event) error {
	if e.CollectionExecution == nil || e.CollectionExecution.validate() != nil || !collectionEventPosition(e.ID) {
		return ErrHistoryUnavailable
	}
	expected := collectionExecutionResultEvent(*e.CollectionExecution)
	expected.ID = e.ID
	if !reflect.DeepEqual(e, expected) {
		return ErrHistoryUnavailable
	}
	return nil
}

func decodeCollectionExecutionResultEvent(raw []byte) (Event, error) {
	var event Event
	if len(raw) == 0 || len(raw) > maxCollectionExecutionResultEventBytes || collectionPlanJSON(context.Background(), raw) != nil {
		return event, ErrHistoryUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&event) != nil || validateCollectionExecutionResultEvent(event) != nil {
		return Event{}, ErrHistoryUnavailable
	}
	canonical, _ := json.Marshal(event)
	if !bytes.Equal(raw, canonical) {
		return Event{}, ErrHistoryUnavailable
	}
	return event, nil
}

var collectionExecutionAnchorBucket = []byte("collection_execution_anchors")

// Independent primary metadata for bounded memory point reads. Item pages stay
// in their existing index; this retains one original anchor and seal per result.
type collectionExecutionMemoryEvidence struct {
	anchor Event
	seal   []byte
}

func (h *HistoryStore) indexMemoryExecutionEvidence(e Event) {
	if e.CollectionExecution == nil && (e.CollectionExecutionHistory == nil || e.CollectionExecutionHistory.Seal == nil) {
		return
	}
	id, ok := collectionExecutionPublicationIdentity(e)
	if !ok {
		return
	}
	if h.memoryExecutionEvidence == nil {
		h.memoryExecutionEvidence = make(map[string]collectionExecutionMemoryEvidence)
	}
	v := h.memoryExecutionEvidence[id]
	if e.CollectionExecution != nil {
		v.anchor = e.Clone()
		if h.memoryExecutionTree == nil {
			h.memoryExecutionTree = newOperationMemoryTree()
		}
		h.memoryExecutionTree.ReplaceOrInsert(operationMemoryItem{key: id, event: v.anchor})
	} else {
		v.seal, _ = json.Marshal(e)
	}
	h.memoryExecutionEvidence[id] = v
}

func writeCollectionExecutionAnchor(tx *bolt.Tx, event Event, raw []byte) error {
	if validateCollectionExecutionResultEvent(event) != nil || len(raw) > maxCollectionExecutionResultEventBytes {
		return ErrHistoryUnavailable
	}
	b, err := tx.CreateBucketIfNotExists(collectionExecutionAnchorBucket)
	if err != nil {
		return err
	}
	id := []byte(event.CollectionExecution.Binding.OperationID)
	if old := b.Get(id); old != nil && !bytes.Equal(old, raw) {
		return ErrHistoryUnavailable
	}
	return b.Put(id, raw)
}

func validateHistoryExecutionAnchorIndex(ctx context.Context, tx *bolt.Tx) error {
	b := tx.Bucket(collectionExecutionAnchorBucket)
	if b == nil {
		return nil
	}
	primary := tx.Bucket([]byte("events"))
	return b.ForEach(func(id, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := decodeCollectionExecutionResultEvent(raw)
		if err != nil || primary == nil || string(id) != e.CollectionExecution.Binding.OperationID || !bytes.Equal(primary.Get([]byte(e.MonitorID+"\x00"+e.ID)), raw) {
			return ErrHistoryUnavailable
		}
		return nil
	})
}

func validateHistoryExecutionAnchor(tx *bolt.Tx, key, raw []byte, event Event) error {
	if !collectionExecutionHistoryEvent(event) || event.CollectionExecutionHistory != nil {
		return nil
	}
	e, err := decodeCollectionExecutionResultEvent(raw)
	if err != nil || string(key) != e.MonitorID+"\x00"+e.ID {
		return ErrHistoryUnavailable
	}
	b := tx.Bucket(collectionExecutionAnchorBucket)
	if b == nil || !bytes.Equal(b.Get([]byte(e.CollectionExecution.Binding.OperationID)), raw) {
		return ErrHistoryUnavailable
	}
	return nil
}

func (h *HistoryStore) prepareMemoryExecutionAnchors(events []Event) (map[string]Event, error) {
	pending := make(map[string]Event)
	for _, event := range events {
		if event.CollectionExecution == nil {
			continue
		}
		id := event.CollectionExecution.Binding.OperationID
		if prior, ok := h.memoryExecutionAnchors[id]; ok && !reflect.DeepEqual(prior, event) {
			return nil, ErrHistoryUnavailable
		}
		if prior, ok := pending[id]; ok && !reflect.DeepEqual(prior, event) {
			return nil, ErrHistoryUnavailable
		}
		pending[id] = event.Clone()
	}
	return pending, nil
}

func (h *HistoryStore) expireMemoryExecutionAnchors() {
	for id, event := range h.memoryExecutionAnchors {
		if event.At.Before(h.catalog.Cutoff) {
			delete(h.memoryExecutionAnchors, id)
		}
	}
	for id, evidence := range h.memoryExecutionEvidence {
		if evidence.anchor.At.Before(h.catalog.Cutoff) {
			if h.memoryExecutionTree != nil {
				h.memoryExecutionTree.Delete(operationMemoryItem{key: id})
			}
			delete(h.memoryExecutionEvidence, id)
		}
	}
}

func validateCollectionExecutionHistoryEvent(e Event) error {
	if e.CollectionExecutionHistory != nil {
		return validateCollectionExecutionPublicationEvent(e)
	}
	return validateCollectionExecutionResultEvent(e)
}
