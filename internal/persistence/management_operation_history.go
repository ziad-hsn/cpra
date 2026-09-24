package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (v ManagementOperationView) checkSegments(h *HistoryStore) error {
	if len(v.ordinary.segments) > maxOperationSegments {
		return ErrHistoryUnavailable
	}
	for _, day := range v.ordinary.segments {
		if !h.catalog.Segments[day] || h.databases[day] == nil {
			return ErrHistoryUnavailable
		}
		if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
			return ErrHistoryUnavailable
		}
	}
	return nil
}

type collectionOperationSource struct {
	tx             *bolt.Tx
	day            string
	primary, index *bolt.Cursor
	prefix         string
	id             string
	raw            []byte
	event          Event
	ready          bool
	memory         *HistoryStore
	execution      bool
}

func (s *collectionOperationSource) advance(after string) error {
	s.ready = false
	if s.execution {
		return s.advanceExecution(after)
	}
	if s.memory != nil {
		h := s.memory
		if h.memoryCollectionTree == nil {
			if len(h.memoryCollectionEvidence) != 0 || len(h.memoryCollectionReceipts) != 0 {
				return ErrHistoryUnavailable
			}
			return nil
		}
		if h.memoryCollectionTree.Len() != len(h.memoryCollectionEvidence) || len(h.memoryCollectionReceipts) > len(h.memoryCollectionEvidence) {
			return ErrHistoryUnavailable
		}
		pivot := after
		if pivot == "" {
			pivot = s.prefix
		}
		h.memoryCollectionTree.AscendGreaterOrEqual(operationMemoryItem{key: pivot}, func(item operationMemoryItem) bool {
			if item.key <= after {
				return true
			}
			if !strings.HasPrefix(item.key, s.prefix) {
				return false
			}
			s.id, s.event, s.ready = item.key, item.event, true
			return false
		})
		return nil
	}
	// Both cursors are ordered by operation ID. Comparing their next identities
	// detects an omitted derived row as well as an orphan index without a scan.
	primaryStart := "collection/" + s.prefix
	indexStart := s.prefix
	if after != "" {
		primaryStart = "collection/" + after + "\x01"
		indexStart = after + "\x00"
	}
	var pk, praw, ik, iraw []byte
	if s.primary != nil {
		pk, praw = s.primary.Seek([]byte(primaryStart))
	}
	if s.index != nil {
		ik, iraw = s.index.Seek([]byte(indexStart))
	}
	primaryPresent := bytes.HasPrefix(pk, []byte("collection/"+s.prefix))
	indexPresent := bytes.HasPrefix(ik, []byte(s.prefix))
	if !primaryPresent && !indexPresent {
		return nil
	}
	if !primaryPresent || !indexPresent || len(pk) > 128 || len(ik) != 60 || len(praw) > maxCollectionReceiptEventBytes || len(iraw) > maxCollectionReceiptEventBytes {
		return ErrHistoryUnavailable
	}
	suffix := strings.TrimPrefix(string(pk), "collection/")
	id, position, ok := strings.Cut(suffix, "\x00")
	if !ok || !collectionEventPosition(position) || id != string(ik) || !bytes.Equal(praw, iraw) {
		return ErrHistoryUnavailable
	}
	// New terminal receipts are immutable: two primary events for one handle
	// cannot be collapsed into a fabricated latest result.
	next, _ := s.primary.Next()
	if bytes.HasPrefix(next, []byte("collection/"+id+"\x00")) {
		return ErrHistoryUnavailable
	}
	s.id, s.raw, s.ready = id, praw, true
	return nil
}

func (s *collectionOperationSource) receipt() (Event, error) {
	if s.memory != nil {
		primary, ok := s.memory.memoryCollectionEvidence[s.id]
		indexed, indexedOK := s.memory.memoryCollectionReceipts[s.id]
		if !ok || !indexedOK || !reflect.DeepEqual(primary, s.event) || !reflect.DeepEqual(indexed, s.event) || validateCollectionReceiptEvent(s.event) != nil || s.event.Collection.ID != s.id {
			return Event{}, ErrHistoryUnavailable
		}
		return s.event, nil
	}
	var event Event
	if len(s.raw) == 0 || json.Unmarshal(s.raw, &event) != nil || validateCollectionReceiptEvent(event) != nil || event.Collection.ID != s.id || event.At.UTC().Format("2006-01-02") != s.day {
		return Event{}, ErrHistoryUnavailable
	}
	return event, nil
}

func (v ManagementOperationView) collectionHistoryPageBudget(ctx context.Context, h *HistoryStore, after string, limit int, budget *operationReadBudget) ([]OperationObservation, string, error) {
	sourceCount := 2 * len(v.ordinary.segments)
	if h.dir == "" {
		sourceCount = 2
	}
	if !budget.take(8 * sourceCount) {
		return nil, "c:" + after, nil
	}
	sources := make([]*collectionOperationSource, 0, sourceCount)
	transactions := make([]*bolt.Tx, 0, len(v.ordinary.segments))
	defer func() {
		for _, tx := range transactions {
			_ = tx.Rollback()
		}
	}()
	prefix := "op." + v.ordinary.epoch + "."
	if h.dir == "" {
		sources = append(sources, &collectionOperationSource{memory: h, prefix: prefix},
			&collectionOperationSource{memory: h, prefix: prefix, execution: true})
	} else {
		for _, day := range v.ordinary.segments {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			tx, err := h.databases[day].Begin(false)
			if err != nil {
				return nil, "", ErrHistoryUnavailable
			}
			transactions = append(transactions, tx)
			for _, execution := range []bool{false, true} {
				source := &collectionOperationSource{tx: tx, day: day, prefix: prefix, execution: execution}
				if bucket := tx.Bucket([]byte("events")); bucket != nil {
					source.primary = bucket.Cursor()
				}
				index := collectionReceiptBucket
				if execution {
					index = collectionExecutionAnchorBucket
				}
				if bucket := tx.Bucket(index); bucket != nil {
					source.index = bucket.Cursor()
				}
				sources = append(sources, source)
			}
		}
	}
	for _, source := range sources {
		if err := source.advance(after); err != nil {
			return nil, "", err
		}
	}
	rows := make([]OperationObservation, 0, limit)
	frontier, usedBytes := after, 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		id := ""
		for _, source := range sources {
			if source.ready && (id == "" || source.id < id) {
				id = source.id
			}
		}
		if id == "" {
			return rows, "", nil
		}
		epoch, sequence, err := ParseOperationHandle(id)
		if err != nil || epoch != v.ordinary.epoch || id <= frontier {
			return nil, "", ErrHistoryUnavailable
		}
		if sequence > v.ordinary.highWater {
			return rows, "", nil
		}
		var terminal, execution *collectionOperationSource
		cost := 0
		for _, source := range sources {
			if !source.ready || source.id != id {
				continue
			}
			cost += 8
			if source.execution {
				if execution != nil {
					return nil, "", ErrHistoryUnavailable
				}
				execution = source
			} else {
				if terminal != nil {
					return nil, "", ErrHistoryUnavailable
				}
				terminal = source
			}
		}
		if !budget.take(cost) {
			return rows, "c:" + frontier, nil
		}
		var receiptEvent, anchor Event
		if terminal != nil {
			receiptEvent, err = terminal.receipt()
			if err != nil {
				return nil, "", err
			}
		}
		if execution != nil {
			anchor, err = execution.anchor()
			if err != nil {
				return nil, "", err
			}
		}
		receiptVisible := terminal != nil && receiptEvent.ID <= v.ordinary.upper
		anchorVisible := execution != nil && anchor.ID <= v.ordinary.upper
		if receiptVisible && anchorVisible && !collectionExecutionReceiptMatchesParent(*anchor.CollectionExecution, *receiptEvent.Collection) {
			return nil, "", ErrHistoryUnavailable
		}
		_, frozen := v.collectionIDs[id]
		ownedExecution := anchorVisible && !anchor.At.Before(v.ordinary.cutoff) && anchor.CollectionExecution.Actor == v.actor
		ownedReceipt := receiptVisible && !receiptEvent.At.Before(v.ordinary.cutoff) && receiptEvent.Collection.Actor == v.actor
		if !frozen && (ownedExecution || ownedReceipt) {
			if len(rows) == limit {
				return rows, "c:" + frontier, nil
			}
			// Metadata only: this bound covers the original receipt or detached
			// execution summary plus projection overhead, never an item page.
			bytes := maxCollectionReceiptEventBytes + 64
			if bytes > collectionLedgerPageBytes-usedBytes {
				return rows, "c:" + frontier, nil
			}
			var owned CollectionReceipt
			if ownedExecution {
				if !budget.take(8) {
					return rows, "c:" + frontier, nil
				}
				owned, err = execution.executionObservation(ctx, h, anchor, v.ordinary.upper, v.at.Add(time.Since(v.started)))
				if err != nil {
					return nil, "", err
				}
			} else {
				owned = receiptEvent.Collection.Clone()
				if owned.Validation != nil && !v.at.Before(owned.Validation.FinalizedAt.AddDate(0, 0, 30)) {
					owned.Validation = nil
				}
			}
			rows = append(rows, OperationObservation{Collection: &owned})
			usedBytes += bytes
		}
		frontier = id
		for _, source := range sources {
			if source.ready && source.id == id {
				if err := source.advance(frontier); err != nil {
					return nil, "", err
				}
			}
		}
	}
}
