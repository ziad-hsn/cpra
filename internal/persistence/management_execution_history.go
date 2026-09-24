package persistence

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// advanceExecution seeks one operation namespace, skipping its item keys with a
// direct seek. The first original event is the anchor; no item body is decoded.
func (s *collectionOperationSource) advanceExecution(after string) error {
	if s.memory != nil {
		h := s.memory
		if h.memoryExecutionTree == nil {
			if len(h.memoryExecutionEvidence) != 0 || len(h.memoryExecutionAnchors) != 0 || len(h.memoryExecutionResults) != 0 {
				return ErrHistoryUnavailable
			}
			return nil
		}
		if h.memoryExecutionTree.Len() != len(h.memoryExecutionEvidence) || len(h.memoryExecutionAnchors) != len(h.memoryExecutionEvidence) || len(h.memoryExecutionResults) > len(h.memoryExecutionEvidence) {
			return ErrHistoryUnavailable
		}
		pivot := after
		if pivot == "" {
			pivot = s.prefix
		}
		h.memoryExecutionTree.AscendGreaterOrEqual(operationMemoryItem{key: pivot}, func(item operationMemoryItem) bool {
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
	primaryStart, indexStart := "collection-execution/"+s.prefix, s.prefix
	if after != "" {
		primaryStart, indexStart = "collection-execution/"+after+"\x01", after+"\x00"
	}
	var pk, praw, ik, iraw []byte
	if s.primary != nil {
		pk, praw = s.primary.Seek([]byte(primaryStart))
	}
	if s.index != nil {
		ik, iraw = s.index.Seek([]byte(indexStart))
	}
	primaryPresent, indexPresent := bytes.HasPrefix(pk, []byte("collection-execution/"+s.prefix)), bytes.HasPrefix(ik, []byte(s.prefix))
	if !primaryPresent && !indexPresent {
		return nil
	}
	if !primaryPresent || !indexPresent || len(pk) > 128 || len(ik) != 60 || len(praw) > maxCollectionExecutionResultEventBytes || len(iraw) > maxCollectionExecutionResultEventBytes {
		return ErrHistoryUnavailable
	}
	id, position, ok := strings.Cut(strings.TrimPrefix(string(pk), "collection-execution/"), "\x00")
	if !ok || !collectionEventPosition(position) || id != string(ik) || !bytes.Equal(praw, iraw) {
		return ErrHistoryUnavailable
	}
	s.id, s.raw, s.ready = id, praw, true
	return nil
}

func (s *collectionOperationSource) anchor() (Event, error) {
	if s.memory != nil {
		evidence, primaryOK := s.memory.memoryExecutionEvidence[s.id]
		indexed, indexOK := s.memory.memoryExecutionAnchors[s.id]
		if !primaryOK || !indexOK || !reflect.DeepEqual(evidence.anchor, s.event) || !reflect.DeepEqual(indexed, s.event) ||
			validateCollectionExecutionResultEvent(s.event) != nil || s.event.CollectionExecution.Binding.OperationID != s.id {
			return Event{}, ErrHistoryUnavailable
		}
		return s.event, nil
	}
	e, err := decodeCollectionExecutionResultEvent(s.raw)
	if err != nil || e.CollectionExecution.Binding.OperationID != s.id || e.At.UTC().Format("2006-01-02") != s.day {
		return Event{}, ErrHistoryUnavailable
	}
	return e, nil
}

// executionObservation reads seal/progress metadata only after the caller has
// selected an owned anchor below the frozen watermark. Source transactions stay
// inside a single bounded Page call and never survive into an HTTP cursor.
func (s *collectionOperationSource) executionObservation(ctx context.Context, h *HistoryStore, anchor Event, upper string, at time.Time) (CollectionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CollectionReceipt{}, err
	}
	r := collectionRetainedExecution{summary: anchor.CollectionExecution.Clone()}
	if !at.Before(anchor.At.AddDate(0, 0, 30)) || !h.catalog.Cutoff.IsZero() && !anchor.At.After(h.catalog.Cutoff) {
		r.expired = true
		return r.observation(), nil
	}
	var p collectionExecutionPublicationProgress
	var raw []byte
	if s.memory != nil {
		m := h.memoryExecutionResults[s.id]
		if m == nil || !bytes.Equal(m.summary, h.memoryExecutionEvidence[s.id].seal) {
			return CollectionReceipt{}, ErrHistoryUnavailable
		}
		p, raw = m.progress, m.summary
	} else {
		var b *bolt.Bucket
		if root := s.tx.Bucket(collectionExecutionPublicationBucket); root != nil {
			b = root.Bucket([]byte(s.id))
		}
		if b == nil {
			return CollectionReceipt{}, ErrHistoryUnavailable
		}
		var err error
		p, err = decodeCollectionExecutionPublicationProgress(b.Get(collectionExecutionPublicationProgressKey))
		if err != nil {
			return CollectionReceipt{}, err
		}
		raw = b.Get(collectionExecutionPublicationSummaryKey)
		e, err := decodeCollectionExecutionPublicationEvent(raw)
		if err != nil || executionHistoryPrimary(s.tx, e, raw) != nil {
			return CollectionReceipt{}, ErrHistoryUnavailable
		}
	}
	receipt, err := collectionExecutionRetainedSeal(anchor, p, raw, upper)
	if err != nil {
		return CollectionReceipt{}, err
	}
	r.receipt = &receipt
	return r.observation(), ctx.Err()
}

// A retained execution companion can supersede the earlier terminal receipt in
// the list only when both describe the same original admission and outcome.
func collectionExecutionReceiptMatchesParent(summary CollectionExecutionSummary, r CollectionReceipt) bool {
	if summary.Binding.OperationID != r.ID || summary.Binding.UploadID != r.UploadID || summary.Actor != r.Actor ||
		summary.IdentityFormat != r.IdentityFormat || summary.ContentDigest != r.ContentDigest || summary.ItemCount != r.ItemCount ||
		summary.Outcome != r.Phase || r.Activation == nil || summary.Binding.ActivationID != r.Activation.ID ||
		summary.Binding.PlanID != r.Activation.PlanID || summary.Binding.PlanDigest != r.Activation.PlanDescriptor.Digest || !summary.ActivationAt.Equal(r.Activation.At) {
		return false
	}
	if summary.Fence.Phase == "applying" {
		return r.Execution != nil && collectionExecutionSummariesEqual(&summary, r.Execution)
	}
	return summary.Fence.TerminalAt.Equal(r.TerminalAt) && summary.Fence.CancellationID == r.CancellationID && summary.Fence.InvalidatedByRestore == r.InvalidatedByRestore
}
