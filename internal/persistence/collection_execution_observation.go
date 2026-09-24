package persistence

import (
	"context"
	"errors"
	"time"
)

// CollectionExecutionCounts contains bounded committed observations. Accepted
// counts catalog mutations; ChildApplied counts independent controller results.
type CollectionExecutionCounts struct {
	Processed, Accepted, Unchanged, Conflicts, DependencyBlocked uint64
	Unattempted, ChildPending, ChildApplied, ChildFailed         uint64
	ChildSuperseded, ChildInvalidated                            uint64
}

// CollectionExecutionObservation is detached read metadata, never a persisted
// receipt or an execution grant. Descriptor is present only for a sealed result.
type CollectionExecutionObservation struct {
	State      string
	Counts     CollectionExecutionCounts
	Summary    *CollectionExecutionSummary
	Descriptor *CollectionExecutionDescriptor
}

func (o CollectionExecutionObservation) Clone() CollectionExecutionObservation {
	if o.Summary != nil {
		s := o.Summary.Clone()
		o.Summary = &s
	}
	if o.Descriptor != nil {
		d := *o.Descriptor
		o.Descriptor = &d
	}
	return o
}

func collectionExecutionCountsFor(p *CollectionExecutionProgress, unattempted uint64) CollectionExecutionCounts {
	c := CollectionExecutionCounts{Unattempted: unattempted}
	if p != nil {
		c.Processed, c.Accepted, c.Unchanged = p.Processed, p.Accepted, p.Unchanged
		c.Conflicts, c.DependencyBlocked = p.Conflicts, p.DependencyBlocked
		c.ChildPending, c.ChildApplied, c.ChildFailed = p.Accepted-p.ChildTerminals, p.ChildApplied, p.ChildFailed
		c.ChildSuperseded, c.ChildInvalidated = p.ChildSuperseded, p.ChildInvalidated
	}
	return c
}

func collectionExecutionObservationFor(head CollectionState, at time.Time) *CollectionExecutionObservation {
	if head.Activation == nil {
		return nil
	}
	o := &CollectionExecutionObservation{State: "pending", Counts: collectionExecutionCountsFor(head.Execution, 0)}
	if result := head.ExecutionResult; result != nil {
		summary := result.Summary.Clone()
		o.Summary = &summary
		o.Counts = collectionExecutionCountsFor(summary.Fence.Progress, summary.Unattempted)
		if result.HistorySealed {
			o.State = "ready"
			descriptor := collectionExecutionReceiptFor(*result).Descriptor
			o.Descriptor = &descriptor
		}
		if !result.HistoryExpiredAt.IsZero() || !at.Before(summary.FinalizedAt.AddDate(0, 0, 30)) {
			o.State = "expired"
		}
	}
	return o
}

// verifyCollectionExecutionObservationLocked verifies only anchor/seal metadata;
// the final ordinal intentionally reads no item rows. Callers hold history.mu,
// never Store/FSM locks. A monotonic history cutoff is an expired observation;
// missing expected unexpired evidence remains unavailable.
func (h *HistoryStore) verifyCollectionExecutionObservationLocked(ctx context.Context, o *CollectionExecutionObservation, index uint64, at time.Time) error {
	if o == nil || o.Summary == nil || o.State == "expired" {
		return ctx.Err()
	}
	var err error
	if o.Descriptor != nil {
		r := CollectionExecutionReceipt{Summary: *o.Summary, Descriptor: *o.Descriptor}
		_, err = h.collectionExecutionPageLocked(ctx, r, index, r.Descriptor.Count, 1, at)
	} else {
		err = h.collectionExecutionAnchorLocked(ctx, *o.Summary, index, at)
	}
	if errors.Is(err, ErrOperationExpired) {
		o.State = "expired"
		return nil
	}
	return err
}

// Publication/expiry can commit while a page waits for history. Recheck the
// selected original owners and immutable results with no history lock held.
func (v ManagementOperationView) executionObservationsCurrent(ctx context.Context, rows []OperationObservation) error {
	return v.executionObservationsAt(ctx, rows, v.at.Add(time.Since(v.started)), false)
}

func (v ManagementOperationView) executionObservationsAt(ctx context.Context, rows []OperationObservation, observedAt time.Time, rejectExpiryChange bool) error {
	started := time.Now()
	return v.ordinary.store.withCollectionOperationMetadata(ctx, func(f *machine) error {
		observedAt = observedAt.Add(time.Since(started))
		if f.image.OperationEpoch != v.ordinary.epoch || f.image.OperationHighWater < v.ordinary.highWater {
			return ErrOperationCursorExpired
		}
		if f.image.Index < v.index || f.history != v.history {
			return ErrHistoryUnavailable
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if row.Collection == nil || row.Collection.ExecutionObservation == nil {
				continue
			}
			r, o := row.Collection, row.Collection.ExecutionObservation
			epoch, sequence, err := ParseOperationHandle(r.ID)
			if err != nil || epoch != v.ordinary.epoch || sequence > v.ordinary.highWater || r.Actor != v.actor {
				return ErrOperationCursorExpired
			}
			head, exists := f.image.Collections[r.ID]
			if exists && head.Actor != v.actor {
				return ErrOperationCursorExpired
			}
			if exists && (head.ID != r.ID || head.validate() != nil) {
				return ErrHistoryUnavailable
			}
			if !exists {
				if o.Summary == nil || o.Summary.validate() != nil || o.Summary.Binding.OperationID != r.ID || o.Summary.Actor != v.actor ||
					o.Summary.IdentityFormat != r.IdentityFormat || o.Summary.NormalizationProfile != r.NormalizationProfile || o.Summary.ContentDigest != r.ContentDigest || o.Summary.ItemCount != r.ItemCount ||
					o.State != "ready" && o.State != "expired" || o.Descriptor == nil && o.State != "expired" {
					return ErrHistoryUnavailable
				}
				if o.Descriptor != nil && (CollectionExecutionReceipt{Summary: *o.Summary, Descriptor: *o.Descriptor}).validate() != nil {
					return ErrHistoryUnavailable
				}
			}
			if o.Summary == nil {
				continue
			}
			expired := false
			if exists {
				current := head.ExecutionResult
				if current == nil || !collectionExecutionSummariesEqual(&current.Summary, o.Summary) {
					return ErrHistoryUnavailable
				}
				if o.Descriptor != nil && (!current.HistorySealed || collectionExecutionReceiptFor(*current).Descriptor != *o.Descriptor) {
					return ErrHistoryUnavailable
				}
				expired = !current.HistoryExpiredAt.IsZero()
			}
			if expired || !observedAt.Before(o.Summary.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(o.Summary.FinalizedAt) {
				if rejectExpiryChange && o.State != "expired" {
					return ErrOperationCursorExpired
				}
				if rejectExpiryChange {
					continue
				}
				o.State = "expired"
			}
		}
		return nil
	})
}
