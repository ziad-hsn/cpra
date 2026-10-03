package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	bolt "go.etcd.io/bbolt"
)

// collectionRetainedExecution contains read authority recovered from original
// retained history. It is not a collection header or a mutation input.
type collectionRetainedExecution struct {
	summary CollectionExecutionSummary
	receipt *CollectionExecutionReceipt
	expired bool
}

var errCollectionExecutionAbsent = errors.New("execution history absent")

// collectionRetainedExecution performs bounded index lookups, never a history
// scan. Once a header is gone, its immutable anchor recovers the original owner;
// that check precedes access to the publication seal and all item rows.
// errCollectionExecutionAbsent means no execution namespace evidence was found. An
// anchor without its expected unexpired seal is unavailable, never pending.
func (h *HistoryStore) collectionRetainedExecution(ctx context.Context, id, actor string, index uint64, at time.Time) (collectionRetainedExecution, error) {
	empty := collectionRetainedExecution{}
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return empty, ErrCollectionInvalid
	}
	if h == nil {
		return empty, ErrHistoryUnavailable
	}
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return empty, err
	}
	defer h.mu.RUnlock()
	if h.closed || h.err != nil || h.catalog.Index < index || len(h.catalog.Segments) > maxOperationSegments {
		return empty, ErrHistoryUnavailable
	}
	upper := fmt.Sprintf("%020d:%08d", index, 99999999)
	var anchor *Event
	var anchorDB *bolt.DB
	accept := func(e Event) error {
		if anchor != nil || validateCollectionExecutionResultEvent(e) != nil || e.ID > upper || e.CollectionExecution.Binding.OperationID != id {
			return ErrHistoryUnavailable
		}
		copy := e.Clone()
		anchor = &copy
		return nil
	}
	if h.dir == "" {
		evidence, primaryExists := h.memoryExecutionEvidence[id]
		if e, exists := h.memoryExecutionAnchors[id]; exists {
			if !primaryExists || !reflect.DeepEqual(e, evidence.anchor) {
				return empty, ErrHistoryUnavailable
			}
			if err := accept(e); err != nil {
				return empty, err
			}
		} else if primaryExists || h.memoryExecutionResults[id] != nil {
			return empty, ErrHistoryUnavailable
		}
	} else {
		prefix := []byte("collection-execution/" + id + "\x00")
		for day, active := range h.catalog.Segments {
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			if !active {
				continue
			}
			db := h.databases[day]
			if db == nil {
				return empty, ErrHistoryUnavailable
			}
			if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
				return empty, ErrHistoryUnavailable
			}
			err := db.View(func(tx *bolt.Tx) error {
				var raw []byte
				if b := tx.Bucket(collectionExecutionAnchorBucket); b != nil {
					raw = b.Get([]byte(id))
				}
				if raw == nil {
					if root := tx.Bucket(collectionExecutionPublicationBucket); root != nil && root.Bucket([]byte(id)) != nil {
						return ErrHistoryUnavailable
					}
					if primary := tx.Bucket([]byte("events")); primary != nil {
						key, _ := primary.Cursor().Seek(prefix)
						if bytes.HasPrefix(key, prefix) {
							return ErrHistoryUnavailable
						}
					}
					return nil
				}
				e, err := decodeCollectionExecutionResultEvent(raw)
				if err != nil || executionHistoryPrimary(tx, e, raw) != nil {
					return ErrHistoryUnavailable
				}
				if err := accept(e); err != nil {
					return err
				}
				anchorDB = db
				return nil
			})
			if err != nil {
				return empty, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if anchor == nil {
		return empty, errCollectionExecutionAbsent
	}
	summary := anchor.CollectionExecution.Clone()
	if summary.Actor != actor {
		return empty, ErrOperationNotFound
	}
	if at.Before(summary.FinalizedAt) {
		return empty, ErrCollectionConflict
	}
	result := collectionRetainedExecution{summary: summary}
	if !at.Before(summary.FinalizedAt.AddDate(0, 0, 30)) || !h.catalog.Cutoff.IsZero() && !summary.FinalizedAt.After(h.catalog.Cutoff) {
		result.expired = true
		return result, nil
	}
	seal := func(p collectionExecutionPublicationProgress, raw []byte) error {
		r, err := collectionExecutionRetainedSeal(*anchor, p, raw, upper)
		if err != nil {
			return err
		}
		result.receipt = &r
		return nil
	}
	if h.dir == "" {
		m := h.memoryExecutionResults[id]
		if m == nil {
			return empty, ErrHistoryUnavailable
		}
		if err := seal(m.progress, m.summary); err != nil {
			return empty, err
		}
		if !bytes.Equal(m.summary, h.memoryExecutionEvidence[id].seal) {
			return empty, ErrHistoryUnavailable
		}
	} else if err := anchorDB.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(collectionExecutionPublicationBucket)
		if root == nil || root.Bucket([]byte(id)) == nil {
			return ErrHistoryUnavailable
		}
		b := root.Bucket([]byte(id))
		p, err := decodeCollectionExecutionPublicationProgress(b.Get(collectionExecutionPublicationProgressKey))
		if err != nil {
			return err
		}
		raw := b.Get(collectionExecutionPublicationSummaryKey)
		e, err := decodeCollectionExecutionPublicationEvent(raw)
		if err != nil || executionHistoryPrimary(tx, e, raw) != nil {
			return ErrHistoryUnavailable
		}
		return seal(p, raw)
	}); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return result, nil
}

// collectionExecutionRetainedSeal validates bounded seal/progress metadata. The
// caller verifies original ownership and primary/index equality before return.
func collectionExecutionRetainedSeal(anchor Event, p collectionExecutionPublicationProgress, raw []byte, upper string) (CollectionExecutionReceipt, error) {
	empty := CollectionExecutionReceipt{}
	e, err := decodeCollectionExecutionPublicationEvent(raw)
	if err != nil || validateCollectionExecutionResultEvent(anchor) != nil || executionPublicationMatchesAnchor(e, anchor) != nil || e.CollectionExecutionHistory.Seal == nil ||
		p.validate() != nil || p.SummaryEvent != e.ID || e.ID > upper || e.ID <= anchor.ID || p.ResultID != anchor.CollectionExecution.Binding.ActivationID || !p.FinalizedAt.Equal(anchor.At) {
		return empty, ErrHistoryUnavailable
	}
	r := e.CollectionExecutionHistory.Seal.Clone()
	if !collectionExecutionSummariesEqual(&r.Summary, anchor.CollectionExecution) || p.Count != r.Descriptor.Count || p.Bytes != r.Descriptor.Bytes || p.Digest != r.Descriptor.Digest {
		return empty, ErrHistoryUnavailable
	}
	return r, nil
}

// observation contains only fields proven by the retained anchor. It is a
// detached read projection, deliberately not a valid persisted CollectionReceipt:
// upload timestamps, activation grants and validation input are not reconstructed.
func (r collectionRetainedExecution) observation() CollectionReceipt {
	s := r.summary.Clone()
	o := &CollectionExecutionObservation{State: "ready", Summary: &s,
		Counts: collectionExecutionCountsFor(s.Fence.Progress, s.Unattempted)}
	if r.receipt != nil {
		d := r.receipt.Descriptor
		o.Descriptor = &d
	}
	if r.expired {
		o.State = "expired"
	}
	t := s.FinalizedAt
	if s.Fence.Phase != "applying" {
		t = s.Fence.TerminalAt
	}
	return CollectionReceipt{ID: s.Binding.OperationID, UploadID: s.Binding.UploadID, Actor: s.Actor,
		NormalizationProfile: s.NormalizationProfile, IdentityFormat: s.IdentityFormat, ContentDigest: s.ContentDigest, ItemCount: s.ItemCount, Uploaded: s.ItemCount,
		Phase: s.Outcome, TerminalAt: t, CancellationID: s.Fence.CancellationID,
		InvalidatedByRestore: s.Fence.InvalidatedByRestore, ExecutionObservation: o}
}
