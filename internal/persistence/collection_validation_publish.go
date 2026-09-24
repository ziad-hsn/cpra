package persistence

import (
	"context"
	"errors"
	"time"
)

func collectionValidationReceiptFor(v *CollectionValidationState) *CollectionValidationReceipt {
	if v == nil || v.FinalizedAt.IsZero() {
		return nil
	}
	return &CollectionValidationReceipt{Header: v.Header, Descriptor: v.Descriptor, FinalizedAt: v.FinalizedAt}
}

// Publication copies a bounded page from already finalized committed results.
// It creates no outcome, extends no deadline and needs no execution authority.
// Exact event IDs are assigned by the enclosing log position. History must be
// synced before Apply can acknowledge or allow a snapshot to compact that log.
func (f *machine) publishCollectionValidation(c CollectionCommand, at time.Time) Result {
	epoch, seq, _ := ParseOperationHandle(c.OperationID)
	original, ok := f.image.Collections[c.OperationID]
	// Explicit restoration changes the operation epoch but preserves original
	// canceled/expired outcomes. Their inert, finalized evidence still needs to
	// reach retained history before staging can be reclaimed.
	retainedTerminal := ok && (original.Phase == "invalidated" || original.Phase == "canceled" || original.Phase == "expired")
	if epoch != f.image.OperationEpoch && !retainedTerminal {
		return Result{Err: ErrOperationExpired}
	}
	if !ok {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	s := original.Clone()
	v := s.Validation
	if s.UploadID != c.UploadID || v == nil || v.Header.ResultID != c.ValidationID || v.FinalizedAt.IsZero() || at.Before(s.ActivityAt) ||
		c.ValidationPublished > v.Published || c.ValidationPublished != v.Descriptor.Count && c.ValidationPublished%collectionLedgerBatchLimit != 0 {
		return Result{Err: ErrCollectionConflict}
	}
	if v.HistorySealed || !v.HistoryExpiredAt.IsZero() || c.ValidationPublished < v.Published {
		return collectionResult(s)
	}
	if !at.Before(v.FinalizedAt.AddDate(0, 0, 30)) {
		// Expiration is explicit. Skipped old history must never be reported as
		// a sealed, queryable result merely because its append was discarded.
		v.HistoryExpiredAt = at
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		// Persist the replicated observation's retention cutoff before allowing
		// cleanup to remove this header. The compact terminal receipt deliberately
		// preserves its own outcome; it cannot carry a later validation expiry.
		// Replay may repeat this monotonic materialization, never infer a verdict
		// from it. A failed write leaves the original header and stops admission.
		if f.history == nil {
			return f.collectionStorageFailure(ErrHistoryUnavailable)
		}
		if err := f.history.Expire(at); err != nil {
			return f.collectionStorageFailure(err)
		}
		f.image.Collections[s.ID] = s
		return collectionResult(s)
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	count := min(uint64(collectionLedgerBatchLimit), v.Descriptor.Count-v.Published)
	events := make([]Event, 0, count+1)
	if count > 0 {
		page, err := f.collections.ValidationPage(s.ID, v.Published, int(count))
		if err != nil || uint64(len(page)) != count {
			return f.collectionStorageFailure(ErrCollectionUnavailable)
		}
		for _, item := range page {
			if item.Ordinal != v.Published+1 {
				return f.collectionStorageFailure(ErrCollectionInvalid)
			}
			events = append(events, collectionValidationHistoryEvent(CollectionValidationHistory{OperationID: s.ID, ResultID: v.Header.ResultID, FinalizedAt: v.FinalizedAt, Item: &item}))
			v.Published++
		}
	}
	if v.Published == v.Descriptor.Count {
		events = append(events, collectionValidationHistoryEvent(CollectionValidationHistory{OperationID: s.ID, ResultID: v.Header.ResultID, FinalizedAt: v.FinalizedAt, Summary: collectionValidationReceiptFor(v)}))
		v.HistorySealed = true
	}
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	f.image.Collections[s.ID] = s
	result := collectionResult(s)
	result.Events = events
	return result
}

// Maintenance prioritizes one finalized-result page over inactive cleanup.
// Bounded selection uses only the existing at-most-64 staging headers. A stopped
// or reset store admits no maintenance command; rejected stale observations are
// retried from the next committed header, never from a new compiler result.
func (s *Store) maintainCollectionValidationHistory(at time.Time) (bool, error) {
	s.fsm.mu.RLock()
	f := s.fsm
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		s.fsm.mu.RUnlock()
		return false, nil
	}
	var selected CollectionState
	for _, state := range f.image.Collections {
		v := state.Validation
		if v != nil && !v.FinalizedAt.IsZero() && !v.HistorySealed && v.HistoryExpiredAt.IsZero() && !at.Before(state.ActivityAt) &&
			(selected.ID == "" || state.ID < selected.ID) {
			selected = state
		}
	}
	s.fsm.mu.RUnlock()
	if selected.ID == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := s.Submit(ctx, []Command{{Kind: "collection", At: at, Collection: &CollectionCommand{Action: "validation_publish", OperationID: selected.ID, UploadID: selected.UploadID,
		ValidationID: selected.Validation.Header.ResultID, ValidationPublished: selected.Validation.Published}}})
	if err != nil {
		select {
		case <-s.stop:
			return true, nil
		default:
			return true, err
		}
	}
	if len(results) != 1 {
		return true, ErrCollectionUnavailable
	}
	if errors.Is(results[0].Err, ErrCollectionConflict) {
		return true, nil
	}
	if results[0].Err != nil {
		return true, results[0].Err
	}
	if !results[0].Allowed {
		return true, ErrCollectionUnavailable
	}
	return true, nil
}
