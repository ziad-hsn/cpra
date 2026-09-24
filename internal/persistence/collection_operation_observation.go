package persistence

import (
	"context"
	"time"
)

// CollectionOperationObservation retains one protected receipt and its metadata
// fence. It owns no history transaction, input, plan, or execution authority.
type CollectionOperationObservation struct {
	store   *Store
	receipt CollectionReceipt
	access  collectionOperationAccess
	at      time.Time
	started time.Time
}

func (s *Store) CollectionOperationObservation(ctx context.Context, id, actor string, at time.Time) (CollectionOperationObservation, error) {
	var observation CollectionOperationObservation
	_, err := s.collectionOperationAs(ctx, id, actor, at, &observation)
	return observation, err
}

func (o CollectionOperationObservation) Receipt() CollectionReceipt { return o.receipt.Clone() }

// Recheck runs only bounded metadata checks after response-admission waits. It
// never acquires a history lock or reads disk. A changed availability rejects the
// response instead of silently changing an already projected receipt.
func (o CollectionOperationObservation) Recheck(ctx context.Context, at time.Time) error {
	if o.store == nil || ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 || at.Before(o.at) {
		return ErrCollectionInvalid
	}
	started := time.Now()
	if elapsed := o.at.Add(time.Since(o.started)); elapsed.After(at) {
		at = elapsed
	}
	return o.store.withCollectionOperationMetadata(ctx, func(f *machine) error {
		at = at.Add(time.Since(started))
		if f.image.OperationEpoch != o.access.epoch || f.image.OperationHighWater < o.access.highWater {
			return ErrOperationExpired
		}
		if f.image.Index < o.access.index || f.history != o.access.history {
			return ErrCollectionUnavailable
		}
		r := o.receipt
		head, exists := f.image.Collections[r.ID]
		if exists {
			if head.Actor != r.Actor {
				return ErrOperationNotFound
			}
			if head.ID != r.ID || head.validate() != nil || head.UploadID != r.UploadID || head.IdentityFormat != r.IdentityFormat || head.NormalizationProfile != r.NormalizationProfile ||
				head.ContentDigest != r.ContentDigest || head.ItemCount != r.ItemCount || r.Activation != nil && !collectionActivationsEqual(head.Activation, r.Activation) {
				return ErrCollectionUnavailable
			}
		}
		if execution := r.ExecutionObservation; execution != nil {
			if !exists && (execution.Summary == nil || execution.State != "ready" && execution.State != "expired" || execution.Descriptor == nil && execution.State != "expired") {
				return ErrCollectionUnavailable
			}
			if execution.Summary == nil {
				return nil
			}
			expired := !at.Before(execution.Summary.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(execution.Summary.FinalizedAt)
			if exists {
				current := head.ExecutionResult
				if current == nil || !collectionExecutionSummariesEqual(&current.Summary, execution.Summary) {
					return ErrCollectionUnavailable
				}
				if execution.Descriptor != nil && (!current.HistorySealed || collectionExecutionReceiptFor(*current).Descriptor != *execution.Descriptor) {
					return ErrHistoryUnavailable
				}
				expired = expired || !current.HistoryExpiredAt.IsZero()
			}
			if expired && execution.State != "expired" {
				return ErrOperationExpired
			}
			return nil
		}
		if collectionInactive(r.Phase) && !at.Before(r.ExpiresAt) || !r.TerminalAt.IsZero() &&
			(!at.Before(r.TerminalAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(r.TerminalAt)) {
			return ErrOperationExpired
		}
		if r.Validation != nil && (!at.Before(r.Validation.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(r.Validation.FinalizedAt)) {
			return ErrOperationExpired
		}
		return nil
	})
}
