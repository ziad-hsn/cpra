package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/raft"
)

// CollectionValidationResultStatus distinguishes absence and pending publication
// from an original sealed verdict. Its collection commitment is the original
// public inventory MAC, not the encrypted-input progress digest or source hash.
type CollectionValidationResultStatus struct {
	State                string
	OperationID          string
	NormalizationProfile string
	IdentityFormat       string
	ContentDigest        string
	ItemCount            uint64
	ResultID             string
	TerminalPhase        string
	InterruptionReason   string
}

// CollectionValidationResultView pins a single immutable verdict and history
// watermark. It owns no transaction, resource body, encryption key or execution
// grant. HTTP callers separately enforce current authentication and permissions.
type CollectionValidationResultView struct {
	store   *Store
	owner   string
	epoch   string
	index   uint64
	status  CollectionValidationResultStatus
	receipt CollectionValidationReceipt
}

func (v *CollectionValidationResultView) Identity() CollectionValidationResultStatus { return v.status }
func (v *CollectionValidationResultView) Receipt() CollectionValidationReceipt       { return v.receipt }

func validationResultStatus(r CollectionReceipt) CollectionValidationResultStatus {
	s := CollectionValidationResultStatus{OperationID: r.ID, NormalizationProfile: r.NormalizationProfile, IdentityFormat: r.IdentityFormat,
		ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
	if collectionTerminal(r.Phase) {
		s.TerminalPhase = r.Phase
	}
	if r.ValidationRequest != nil && r.ValidationRequest.Interruption != nil {
		s.InterruptionReason = r.ValidationRequest.Interruption.Reason
	}
	if r.Validation != nil {
		s.ResultID = r.Validation.Header.ResultID
	}
	return s
}

// CollectionValidationResultView checks the original owner before reading result
// rows. Finalized results follow thirty-day history retention independently of
// the upload's twenty-four-hour inactivity deadline. An unsealed known result is
// pending; missing expected sealed history is unavailable, never a rejection.
func (s *Store) CollectionValidationResultView(ctx context.Context, id, actor string, at time.Time) (*CollectionValidationResultView, CollectionValidationResultStatus, error) {
	empty := CollectionValidationResultStatus{}
	if !catalogIdentifier(actor, 128) {
		return nil, empty, ErrCollectionInvalid
	}
	v := &CollectionValidationResultView{store: s, owner: actor}
	epoch, _, err := ParseOperationHandle(id)
	if err != nil {
		return nil, empty, ErrOperationNotFound
	}
	v.epoch = epoch
	var receipt CollectionReceipt
	var headerFound, sealed, expired bool
	err = v.inspect(ctx, id, at, func(f *machine, head *CollectionState) error {
		v.index = f.image.Index
		if head != nil {
			headerFound = true
			receipt = collectionReceiptFor(*head)
			if head.Validation != nil {
				sealed = head.Validation.HistorySealed
				expired = !head.Validation.HistoryExpiredAt.IsZero()
			}
			if receipt.Validation == nil && collectionInactive(head.Phase) && !at.Before(head.ExpiresAt) {
				return ErrOperationExpired
			}
		}
		return nil
	})
	if err != nil {
		return nil, empty, err
	}
	if !headerFound || collectionTerminal(receipt.Phase) {
		var expected *CollectionReceipt
		if headerFound {
			copy := receipt.Clone()
			expected = &copy
		}
		receipt, err = s.fsm.history.collectionReceiptExpected(ctx, id, v.index, at, expected)
		if err != nil {
			if errors.Is(err, ErrOperationNotFound) {
				err = ErrOperationExpired // The issued sequence was already checked.
			}
			return nil, empty, err
		}
		if receipt.Actor != actor {
			return nil, empty, ErrOperationNotFound
		}
		if expected != nil && !collectionReceiptsEqual(receipt, *expected) {
			return nil, empty, ErrHistoryUnavailable
		}
		// Cleanup cannot remove a finalized result before its retained seal or
		// explicit expiry. History validates that seal and monotonic cutoff below.
		if !headerFound {
			sealed = receipt.Validation != nil
		}
	}
	status := validationResultStatus(receipt)
	if expired {
		return nil, empty, ErrOperationExpired
	}
	if receipt.Validation == nil {
		switch {
		case collectionTerminal(receipt.Phase):
			status.State = "noVerdict"
		case receipt.Phase == "uploading" && receipt.ValidationRequest == nil:
			status.State = "notRequested"
		default:
			status.State = "pending"
		}
	} else {
		v.receipt = *receipt.Validation
		if at.Before(v.receipt.FinalizedAt) {
			return nil, empty, ErrCollectionInvalid
		}
		if !at.Before(v.receipt.FinalizedAt.AddDate(0, 0, 30)) {
			return nil, empty, ErrOperationExpired
		}
		status.State = "pending"
		if sealed {
			status.State = "ready"
		}
	}
	v.status = status
	if status.State != "ready" {
		// A reset/close/owner change during the retained receipt read cannot
		// become a successful stale observation either.
		if err := v.inspect(ctx, id, at, nil); err != nil {
			return nil, empty, err
		}
		return nil, status, nil
	}
	if _, err := v.Page(ctx, 0, 1, at); err != nil {
		return nil, empty, err
	}
	return v, status, nil
}

// inspect performs bounded metadata inspection under Store -> FSM locks. It
// neither reads history rows nor leaves a lock held for the caller.
func (v *CollectionValidationResultView) inspect(ctx context.Context, id string, at time.Time, read func(*machine, *CollectionState) error) error {
	if v == nil || v.store == nil || ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return ErrCollectionInvalid
	}
	s := v.store
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if s.fsm == nil {
		return ErrCollectionUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	f := s.fsm
	select {
	case <-s.stop:
		return ErrCollectionUnavailable
	default:
	}
	if s.err != nil || f.err != nil || f.bootstrapPending() || f.restorePending() ||
		f.image.Authentication != nil && f.image.Authentication.ResetRequired || s.raft != nil && s.raft.State() != raft.Leader {
		return ErrCollectionUnavailable
	}
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
	if f.image.Index < v.index {
		return ErrCollectionUnavailable
	}
	var head *CollectionState
	if current, ok := f.image.Collections[id]; ok {
		if current.Actor != v.owner {
			return ErrOperationNotFound
		}
		if current.ID != id || current.validate() != nil {
			return ErrCollectionUnavailable
		}
		if current.Activation != nil && at.Before(current.Activation.At) {
			return ErrCollectionConflict
		}
		if current.Validation != nil && !current.Validation.HistoryExpiredAt.IsZero() {
			return ErrOperationExpired
		}
		if v.receipt.Header.ResultID != "" {
			expected := collectionValidationReceiptFor(current.Validation)
			if expected == nil || !collectionValidationReceiptsEqual(*expected, v.receipt) {
				return ErrCollectionUnavailable
			}
		}
		head = &current
	}
	if read != nil {
		if err := read(f, head); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Page keeps the original descriptor, item ceiling and log watermark across
// later commits and staging cleanup. Current restore, health and history expiry
// are still checked for each call. Zero limit means 100; maximum 500 and 4 MiB.
func (v *CollectionValidationResultView) Page(ctx context.Context, after uint64, limit int, at time.Time) (CollectionValidationHistoryPage, error) {
	empty := CollectionValidationHistoryPage{}
	if v == nil || v.status.State != "ready" || limit < 0 || limit > 500 || after > v.receipt.Descriptor.Count {
		return empty, ErrCollectionInvalid
	}
	if err := v.inspect(ctx, v.status.OperationID, at, nil); err != nil {
		return empty, err
	}
	page, err := v.store.fsm.history.collectionValidationPage(ctx, v.receipt, v.index, after, limit, at)
	if err != nil {
		return empty, err
	}
	if err := v.inspect(ctx, v.status.OperationID, at, nil); err != nil {
		return empty, err
	}
	return page, nil
}
