package persistence

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// CollectionValidationHistoryPage is an original-order page of a sealed result.
// NextAfter==0 means no further items. Items are detached values. The caller must
// bind pagination to this immutable receipt and enforce operator authorization.
type CollectionValidationHistoryPage struct {
	Receipt   CollectionValidationReceipt
	Items     []CollectionValidationItem
	NextAfter uint64
}

// collectionValidationPage never discovers an expected result from untrusted
// absence. The caller must retain the authoritative original receipt before
// permitting staging cleanup. Missing, partial or ahead-of-watermark publication
// is unavailable for its entire retention lifetime, not an expired verdict.
// At the explicit thirty-day deadline the original result expires even if its
// daily segment has not yet been reclaimed. Reads do not renew that deadline.
func (h *HistoryStore) collectionValidationPage(ctx context.Context, expected CollectionValidationReceipt, index, after uint64, limit int, at time.Time) (CollectionValidationHistoryPage, error) {
	empty := CollectionValidationHistoryPage{}
	if ctx == nil {
		return empty, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if expected.validate() != nil || at.IsZero() || at.Before(expected.FinalizedAt) || after > expected.Descriptor.Count || limit < 0 || limit > 500 {
		return empty, ErrCollectionInvalid
	}
	if limit == 0 {
		limit = 100
	}
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return empty, err
	}
	defer h.mu.RUnlock()
	return h.collectionValidationPageLocked(ctx, expected, index, after, limit, at)
}

// collectionValidationPageLocked is for callers that already hold h.mu for a
// bounded metadata page. It performs no mutation and never acquires owner locks.
func (h *HistoryStore) collectionValidationPageLocked(ctx context.Context, expected CollectionValidationReceipt, index, after uint64, limit int, at time.Time) (CollectionValidationHistoryPage, error) {
	empty := CollectionValidationHistoryPage{}
	if ctx == nil {
		return empty, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if expected.validate() != nil || at.IsZero() || at.Before(expected.FinalizedAt) || after > expected.Descriptor.Count || limit < 0 || limit > 500 {
		return empty, ErrCollectionInvalid
	}
	if limit == 0 {
		limit = 100
	}
	if h.closed || h.err != nil || h.catalog.Index < index || len(h.catalog.Segments) > maxOperationSegments {
		return empty, ErrHistoryUnavailable
	}
	if !at.Before(expected.FinalizedAt.AddDate(0, 0, 30)) || !h.catalog.Cutoff.IsZero() && !expected.FinalizedAt.After(h.catalog.Cutoff) {
		return empty, ErrOperationExpired
	}
	upper := fmt.Sprintf("%020d:%08d", index, 99999999)
	var result *CollectionValidationHistoryPage
	accept := func(p collectionValidationHistoryProgress, summary []byte, item func(uint64) ([]byte, error)) error {
		if p.validate() != nil || p.ResultID != expected.Header.ResultID || !p.FinalizedAt.Equal(expected.FinalizedAt) || p.SummaryEvent == "" || p.SummaryEvent > upper || p.Count != expected.Descriptor.Count || p.Bytes != expected.Descriptor.Bytes || p.Digest != expected.Descriptor.Digest {
			return ErrHistoryUnavailable
		}
		e, err := decodeCollectionValidationHistoryEvent(summary)
		if err != nil || e.CollectionValidation.Summary == nil || e.ID != p.SummaryEvent || !collectionValidationReceiptsEqual(*e.CollectionValidation.Summary, expected) {
			return ErrHistoryUnavailable
		}
		page := CollectionValidationHistoryPage{Receipt: expected}
		var nbytes int
		for ordinal := after + 1; ordinal <= p.Count && len(page.Items) < limit; ordinal++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, err := item(ordinal)
			if err != nil || len(raw) == 0 || len(raw) > maxCollectionValidationHistoryEventBytes {
				return ErrHistoryUnavailable
			}
			if len(raw) > collectionLedgerBatchBytes-nbytes {
				break
			}
			e, err := decodeCollectionValidationHistoryEvent(raw)
			if err != nil || e.CollectionValidation.Item == nil || e.CollectionValidation.OperationID != expected.Header.OperationID || e.CollectionValidation.ResultID != p.ResultID || !e.At.Equal(expected.FinalizedAt) || e.ID >= p.SummaryEvent || e.CollectionValidation.Item.Ordinal != ordinal {
				return ErrHistoryUnavailable
			}
			page.Items = append(page.Items, *e.CollectionValidation.Item)
			nbytes += len(raw)
		}
		if after+uint64(len(page.Items)) < p.Count {
			if len(page.Items) == 0 {
				return ErrHistoryUnavailable
			}
			page.NextAfter = after + uint64(len(page.Items))
		}
		if result != nil {
			return ErrHistoryUnavailable
		} // One original cohort only.
		result = &page
		return nil
	}
	id := expected.Header.OperationID
	if h.dir == "" {
		m := h.memoryValidationResults[id]
		if m == nil {
			return empty, ErrHistoryUnavailable
		}
		if err := accept(m.progress, m.summary, func(ordinal uint64) ([]byte, error) { return m.items[ordinal], nil }); err != nil {
			return empty, err
		}
	} else {
		prefix := []byte("collection-validation/" + id + "\x00")
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
				var b *bolt.Bucket
				if root := tx.Bucket(collectionValidationHistoryBucket); root != nil {
					b = root.Bucket([]byte(id))
				}
				if b == nil {
					if primary := tx.Bucket([]byte("events")); primary != nil {
						key, _ := primary.Cursor().Seek(prefix)
						if bytes.HasPrefix(key, prefix) {
							return ErrHistoryUnavailable
						}
					}
					return nil
				}
				p, err := decodeCollectionValidationHistoryProgress(b.Get(collectionValidationHistoryProgressKey))
				if err != nil {
					return err
				}
				summary := b.Get(collectionValidationHistorySummaryKey)
				e, err := decodeCollectionValidationHistoryEvent(summary)
				if err != nil || validationHistoryPrimary(tx, e, summary) != nil {
					return ErrHistoryUnavailable
				}
				return accept(p, summary, func(ordinal uint64) ([]byte, error) {
					rows := b.Bucket(collectionValidationHistoryItemsKey)
					if rows == nil {
						return nil, ErrHistoryUnavailable
					}
					raw := rows.Get(collectionOrdinal(ordinal))
					e, err := decodeCollectionValidationHistoryEvent(raw)
					if err != nil || validationHistoryPrimary(tx, e, raw) != nil {
						return nil, ErrHistoryUnavailable
					}
					return raw, nil
				})
			})
			if err != nil {
				return empty, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if result == nil {
		return empty, ErrHistoryUnavailable
	}
	return *result, nil
}
