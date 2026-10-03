package persistence

import (
	"context"
	"errors"
	"time"
)

func collectionCleanupFor(s CollectionState) CollectionCleanup {
	return CollectionCleanup{Uploaded: s.Uploaded, EncodedBytes: s.EncodedBytes,
		RemovedRows: s.RemovedRows, RemovedBytes: s.RemovedBytes, ActivityAt: s.ActivityAt, Plan: collectionPlanCleanupFor(s.Plan), Validation: collectionValidationCleanupFor(s.Validation),
		ValidationRequest: CollectionValidationRequestFenceFor(s), Activation: CollectionActivationFenceFor(s)}
}

// cleanupCollection retires only unactivated canceled, interrupted, expired or restore-invalidated
// input. At most one ledger page is removed per committed command. The header
// and derived ledger advance under the FSM lock; restart reconstructs that same
// prefix from the authoritative snapshot/log. No active resource is touched.
func (f *machine) cleanupCollection(c CollectionCommand, at time.Time) Result {
	original, exists := f.image.Collections[c.OperationID]
	if !exists {
		// A duplicate cleanup after the final page cannot allocate a new handle.
		return Result{Allowed: true}
	}
	s := original.Clone()
	if s.Execution != nil || s.ExecutionResult != nil {
		// Execution-bearing parents use retire and retire_sources after result
		// publication. Cancellation alone cannot discard pending children.
		return Result{Err: ErrCollectionConflict}
	}
	p := c.Cleanup
	planFence := collectionPlanCleanupFor(s.Plan)
	if s.UploadID != c.UploadID || s.Uploaded != p.Uploaded || s.EncodedBytes != p.EncodedBytes ||
		s.RemovedRows != p.RemovedRows || s.RemovedBytes != p.RemovedBytes || !s.ActivityAt.Equal(p.ActivityAt) || at.Before(s.ActivityAt) ||
		s.Cancellation != nil && at.Before(s.Cancellation.At) || !collectionPlanCleanupEqual(planFence, p.Plan) ||
		!collectionValidationCleanupEqual(collectionValidationCleanupFor(s.Validation), p.Validation) ||
		!collectionValidationRequestFenceEqual(CollectionValidationRequestFenceFor(s), p.ValidationRequest) ||
		!collectionRequestCleanupTime(s.ValidationRequest, at) ||
		!collectionActivationFenceEqual(CollectionActivationFenceFor(s), p.Activation) || s.Activation != nil && at.Before(s.Activation.At) {
		return Result{Err: ErrCollectionConflict}
	}
	var events []Event
	switch s.Phase {
	case "uploading", "validating", "validated", "rejected":
		if at.Before(s.ExpiresAt) {
			return Result{Err: ErrCollectionConflict}
		}
		s.Phase, s.TerminalAt = "expired", at
		events = []Event{collectionReceiptEvent(collectionReceiptFor(s))}
	case "expired", "invalidated", "canceled", "interrupted":
		// These terminal inputs never return to uploading or activation.
	default:
		return Result{Err: ErrCollectionConflict}
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	f.discardCollectionPlanPrefix(s.ID)
	f.discardCollectionValidationPlan(s.ID)
	if s.Validation != nil && !s.Validation.FinalizedAt.IsZero() && !s.Validation.HistorySealed && s.Validation.HistoryExpiredAt.IsZero() {
		return Result{Err: ErrCollectionConflict} // Original result must outlive input cleanup.
	}
	// Result rows retire before their source/plan rows. This inactive internal
	// staging namespace is not yet the public retained-result history contract.
	if v := s.Validation; v != nil && v.RemovedRows < v.Uploaded {
		remaining, used := v.Uploaded-v.RemovedRows, v.EncodedBytes-v.RemovedBytes
		removed, err := f.collections.DeleteValidationPage(s.ID, remaining, used)
		if err != nil || removed.Rows == 0 || removed.Rows > remaining || removed.EncodedBytes <= 0 ||
			removed.EncodedBytes > used || removed.More != (removed.Rows < remaining) {
			return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
		}
		v.RemovedRows += removed.Rows
		v.RemovedBytes += removed.EncodedBytes
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		f.image.Collections[s.ID] = s
		result := collectionResult(s)
		result.Events = events
		return result
	}
	// Remove plan tails before input tails. The header survives both namespaces,
	// so a stopped backup never confuses absent input with an executable plan.
	if plan := s.Plan; plan != nil && plan.RemovedFragments < plan.UploadedFragments {
		remaining, used := plan.UploadedFragments-plan.RemovedFragments, plan.EncodedBytes-plan.RemovedBytes
		removed, err := f.collections.DeletePlanPage(s.ID, remaining, used)
		if err != nil || removed.Rows == 0 || removed.Rows > remaining || removed.EncodedBytes <= 0 ||
			removed.EncodedBytes > used || removed.More != (removed.Rows < remaining) {
			return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
		}
		plan.RemovedFragments += removed.Rows
		plan.RemovedBytes += removed.EncodedBytes
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		f.image.Collections[s.ID] = s
		result := collectionResult(s)
		result.Events = events
		return result
	}
	remaining := s.Uploaded - s.RemovedRows
	bytes := s.EncodedBytes - s.RemovedBytes
	removed, err := f.collections.DeletePage(s.ID, remaining, bytes)
	if err != nil {
		return f.collectionStorageFailure(err)
	}
	if removed.Rows > remaining || removed.EncodedBytes > bytes || removed.More != (removed.Rows < remaining) ||
		remaining > 0 && (removed.Rows == 0 || removed.EncodedBytes <= 0) {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	s.RemovedRows += removed.Rows
	s.RemovedBytes += removed.EncodedBytes
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	if removed.More {
		f.image.Collections[s.ID] = s
	} else {
		delete(f.image.Collections, s.ID)
	}
	result := collectionResult(s)
	result.Events = events
	return result
}

// Request and interruption times remain original metadata during cleanup; a
// backward maintenance observation cannot retire a later admitted disposition.
func collectionRequestCleanupTime(request *CollectionValidationRequest, at time.Time) bool {
	return request == nil || !at.Before(request.RequestedAt) &&
		(request.Claim == nil || !at.Before(request.Claim.At)) &&
		(request.Interruption == nil || !at.Before(request.Interruption.At))
}

// maintainCollections selects at most one eligible header and commits one
// bounded cleanup page. Idle stores submit nothing. The source observation is
// fenced in Apply so a concurrent accepted upload cannot be collected using an
// older inactivity deadline. Physical database compaction is separate work.
func (s *Store) maintainCollections(at time.Time) error {
	if handled, err := s.maintainCollectionExecutionHistory(at); handled || err != nil {
		return err
	}
	if handled, err := s.maintainCollectionValidationHistory(at); handled || err != nil {
		return err
	}
	if handled, err := s.maintainCollectionExecutionRetirement(at); handled || err != nil {
		return err
	}
	s.fsm.mu.RLock()
	if s.fsm.err != nil || s.fsm.bootstrapPending() || s.fsm.restorePending() ||
		s.fsm.image.Authentication != nil && s.fsm.image.Authentication.ResetRequired {
		s.fsm.mu.RUnlock()
		return nil
	}
	var selected CollectionState
	for _, state := range s.fsm.image.Collections {
		eligible := state.Phase == "canceled" || state.Phase == "expired" || state.Phase == "invalidated" || state.Phase == "interrupted" || collectionInactive(state.Phase) && !at.Before(state.ExpiresAt)
		if eligible && state.Activation == nil && state.Execution == nil && state.ExecutionResult == nil && !at.Before(state.ActivityAt) && (state.Cancellation == nil || !at.Before(state.Cancellation.At)) && collectionRequestCleanupTime(state.ValidationRequest, at) && (state.Activation == nil || !at.Before(state.Activation.At)) &&
			(selected.ID == "" || state.ID < selected.ID) {
			selected = state
		}
	}
	s.fsm.mu.RUnlock()
	if selected.ID == "" {
		return nil
	}
	progress := collectionCleanupFor(selected)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := s.Submit(ctx, []Command{{Kind: "collection", At: at, Collection: &CollectionCommand{
		Action: "cleanup", OperationID: selected.ID, UploadID: selected.UploadID, Cleanup: &progress}}})
	if err != nil {
		select {
		case <-s.stop:
			return nil
		default:
			return err
		}
	}
	if len(results) != 1 {
		return ErrCollectionUnavailable
	}
	if errors.Is(results[0].Err, ErrCollectionConflict) {
		return nil // Another committed command renewed or advanced this header.
	}
	if results[0].Err != nil {
		return results[0].Err
	}
	if !results[0].Allowed {
		return ErrCollectionUnavailable
	}
	return nil
}
