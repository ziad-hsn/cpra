package persistence

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// CollectionExecutionResultStatus is an observation of the original admitted
// operation's result availability. Empty State means no activation exists;
// pending never asserts that an item result or final seal is available.
type CollectionExecutionResultStatus struct {
	State                string
	OperationID          string
	NormalizationProfile string
	IdentityFormat       string
	ContentDigest        string
	ItemCount            uint64
	ResultID             string
	Summary              *CollectionExecutionSummary
}

func (s CollectionExecutionResultStatus) Clone() CollectionExecutionResultStatus {
	if s.Summary != nil {
		summary := s.Summary.Clone()
		s.Summary = &summary
	}
	return s
}

// CollectionExecutionResultView retains a detached original receipt and history
// watermark. It is not authorization or an execution grant. Public adapters
// must separately check current authentication, owner identity and permissions
// before and after every read, including their own cursor generation fences.
// After source retirement, original owner and result identity come from the
// verified immutable anchor and seal, independently of the source ledger.
type CollectionExecutionResultView struct {
	store   *Store
	history *HistoryStore
	owner   string
	epoch   string
	index   uint64
	status  CollectionExecutionResultStatus
	receipt CollectionExecutionReceipt
	at      time.Time // First caller observation; immutable after construction.
	expired atomic.Bool
}

func (v *CollectionExecutionResultView) Identity() CollectionExecutionResultStatus {
	if v == nil {
		return CollectionExecutionResultStatus{}
	}
	return v.status.Clone()
}

func (v *CollectionExecutionResultView) Receipt() CollectionExecutionReceipt {
	if v == nil {
		return CollectionExecutionReceipt{}
	}
	return v.receipt.Clone()
}

// Recheck verifies only current owner/epoch/health/retention metadata. Adapters
// call it after waiting for their own authorization or cursor admission locks;
// no history lock or disk read is acquired here.
func (v *CollectionExecutionResultView) Recheck(ctx context.Context, at time.Time) error {
	if v == nil {
		return ErrCollectionInvalid
	}
	return v.inspect(ctx, v.status.OperationID, at, nil)
}

// CollectionExecutionResultView checks a live header's owner before history I/O.
// After retirement, bounded anchor metadata recovers that owner before seal/items.
// Retention starts at immutable finalization, even when the original canceled
// receipt is already expired. Finalized but unsealed results remain pending;
// a missing expected unexpired anchor/seal is unavailable, never pending/expired.
func (s *Store) CollectionExecutionResultView(ctx context.Context, id, actor string, at time.Time) (*CollectionExecutionResultView, CollectionExecutionResultStatus, error) {
	started := time.Now()
	empty := CollectionExecutionResultStatus{}
	if !catalogIdentifier(actor, 128) {
		return nil, empty, ErrCollectionInvalid
	}
	epoch, _, err := ParseOperationHandle(id)
	if err != nil {
		return nil, empty, ErrOperationNotFound
	}
	v := &CollectionExecutionResultView{store: s, owner: actor, epoch: epoch, at: at}
	absent := false
	err = v.inspect(ctx, id, at, func(f *machine, head *CollectionState) error {
		v.index, v.history = f.image.Index, f.history
		if head == nil {
			absent = true
			return nil
		}
		v.status = CollectionExecutionResultStatus{OperationID: id, NormalizationProfile: head.NormalizationProfile, IdentityFormat: head.IdentityFormat,
			ContentDigest: head.ContentDigest, ItemCount: head.ItemCount}
		if head.Activation == nil {
			return nil
		}
		v.status.State, v.status.ResultID = "pending", head.Activation.ID
		if head.ExecutionResult == nil {
			return nil
		}
		r := head.ExecutionResult
		summary := r.Summary.Clone()
		v.status.Summary = &summary
		if r.HistorySealed {
			v.status.State = "ready"
			v.receipt = collectionExecutionReceiptFor(*r)
		}
		return nil
	})
	if err != nil {
		return nil, empty, err
	}
	if absent {
		retained, readErr := v.history.collectionRetainedExecution(ctx, id, actor, v.index, at)
		if readErr == nil {
			summary := retained.summary.Clone()
			v.status = CollectionExecutionResultStatus{OperationID: id, NormalizationProfile: summary.NormalizationProfile, IdentityFormat: summary.IdentityFormat,
				ContentDigest: summary.ContentDigest, ItemCount: summary.ItemCount,
				ResultID: summary.Binding.ActivationID, State: "ready", Summary: &summary}
			if retained.receipt != nil {
				v.receipt = retained.receipt.Clone()
			}
			v.expired.Store(retained.expired)
		}
		// Recheck health/epoch after metadata I/O even when discovery failed.
		if err := v.inspect(ctx, id, at.Add(time.Since(started)), func(*machine, *CollectionState) error { return nil }); err != nil {
			return nil, empty, err
		}
		if errors.Is(readErr, errCollectionExecutionAbsent) {
			readErr = ErrHistoryUnavailable
		}
		if readErr != nil {
			return nil, empty, readErr
		}
	}
	if v.status.State == "ready" {
		if _, err := v.Page(ctx, 0, 1, at.Add(time.Since(started))); err != nil {
			return nil, empty, err
		}
		return v, v.Identity(), nil
	}
	if v.status.Summary != nil {
		if v.history == nil {
			return nil, empty, ErrHistoryUnavailable
		}
		readErr := v.history.collectionExecutionAnchor(ctx, *v.status.Summary, v.index, at)
		// Always recheck owner/epoch/health after attempted I/O, even if history
		// failed. A restore or cancellation is not hidden behind an old read error.
		if err := v.inspect(ctx, id, at.Add(time.Since(started)), nil); err != nil {
			return nil, empty, err
		}
		if readErr != nil {
			return nil, empty, readErr
		}
	} else if err := v.inspect(ctx, id, at.Add(time.Since(started)), nil); err != nil {
		return nil, empty, err
	}
	return nil, v.Identity(), nil
}

// inspect holds Store before FSM only for bounded metadata. Live ownership or
// captured retained authority precedes result reads. It neither follows an old
// cancellation receipt nor retains a lock across history I/O.
func (v *CollectionExecutionResultView) inspect(ctx context.Context, id string, at time.Time, read func(*machine, *CollectionState) error) error {
	if v == nil || v.store == nil || ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if at.Before(v.at) {
		return ErrCollectionInvalid
	}
	s := v.store
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return errors.Join(ErrCollectionUnavailable, err)
	}
	defer unlock()
	f := s.fsm
	_, sequence, err := ParseOperationHandle(id)
	if err != nil {
		return ErrOperationNotFound
	}
	if v.epoch != f.image.OperationEpoch {
		return ErrOperationExpired
	}
	if sequence > f.image.OperationHighWater {
		return ErrOperationNotFound
	}
	if f.image.Index < v.index || v.history != nil && v.history != f.history {
		return ErrCollectionUnavailable
	}
	head, ok := f.image.Collections[id]
	if ok && head.Actor != v.owner {
		return ErrOperationNotFound
	}
	if v.expired.Load() {
		return ErrOperationExpired
	}
	if summary := v.status.Summary; summary != nil {
		if !at.Before(summary.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(summary.FinalizedAt) {
			v.expired.Store(true)
			return ErrOperationExpired
		}
		if at.Before(summary.FinalizedAt) {
			return ErrCollectionInvalid
		}
	}
	if !ok {
		if v.status.OperationID == "" && read != nil {
			return read(f, nil) // Discover original authority outside metadata locks.
		}
		if v.status.State != "ready" || v.status.Summary == nil || v.status.Summary.Actor != v.owner ||
			v.status.OperationID != id || v.receipt.validate() != nil || !collectionExecutionSummariesEqual(&v.receipt.Summary, v.status.Summary) {
			return ErrCollectionUnavailable
		}
		return ctx.Err()
	}
	if head.ID != id || head.validate() != nil {
		return ErrCollectionUnavailable
	}
	if head.Activation != nil && at.Before(head.Activation.At) {
		return ErrCollectionConflict
	}
	if r := head.ExecutionResult; r != nil {
		if !r.HistoryExpiredAt.IsZero() || !at.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(r.Summary.FinalizedAt) {
			v.expired.Store(true)
			return ErrOperationExpired
		}
		if at.Before(r.Summary.FinalizedAt) {
			return ErrCollectionInvalid
		}
	}
	if v.status.Summary != nil {
		if head.ExecutionResult == nil || !collectionExecutionSummariesEqual(&head.ExecutionResult.Summary, v.status.Summary) {
			return ErrCollectionUnavailable
		}
		if v.status.State == "ready" && (!head.ExecutionResult.HistorySealed || !collectionExecutionReceiptsEqual(collectionExecutionReceiptFor(*head.ExecutionResult), v.receipt)) {
			return ErrHistoryUnavailable
		}
	}
	if read != nil {
		if err := read(f, &head); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Page keeps the original summary, descriptor and watermark across later commits.
// Zero limit means 100; maximum 500 and 4 MiB. No history transaction survives
// the read, and current owner/epoch/health/expiry are checked again before return.
func (v *CollectionExecutionResultView) Page(ctx context.Context, after uint64, limit int, at time.Time) (CollectionExecutionHistoryPage, error) {
	started := time.Now()
	empty := CollectionExecutionHistoryPage{}
	if v == nil || v.status.State != "ready" || v.history == nil || limit < 0 || limit > 500 || after > v.receipt.Descriptor.Count {
		return empty, ErrCollectionInvalid
	}
	if err := v.inspect(ctx, v.status.OperationID, at, nil); err != nil {
		return empty, err
	}
	page, readErr := v.history.collectionExecutionPage(ctx, v.receipt, v.index, after, limit, at)
	if err := v.inspect(ctx, v.status.OperationID, at.Add(time.Since(started)), nil); err != nil {
		return empty, err
	}
	if readErr != nil {
		return empty, readErr
	}
	return page, nil
}
