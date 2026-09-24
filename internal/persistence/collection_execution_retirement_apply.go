package persistence

import (
	"context"
	"errors"
	"time"
)

func (f *machine) collectionExecutionRetirementState(c CollectionExecuteCommand, at time.Time) (CollectionState, bool, error) {
	if c.Action != "retire" || c.validate(at) != nil {
		return CollectionState{}, false, ErrCollectionInvalid
	}
	s, ok := f.image.Collections[c.Binding.OperationID]
	if !ok {
		return CollectionState{}, false, ErrOperationNotFound
	}
	b, err := collectionExecutionBindingFor(s)
	if err != nil || b != c.Binding || s.validate() != nil || s.ExecutionResult == nil ||
		!collectionTerminal(s.Phase) || !collectionExecutionResultStatesEqual(c.Retirement.Result, *s.ExecutionResult, true) ||
		!s.ExecutionResult.HistorySealed && s.ExecutionResult.HistoryExpiredAt.IsZero() || at.Before(s.TerminalAt) {
		return CollectionState{}, false, ErrCollectionConflict
	}
	want, current := c.Retirement.Retirement, s.ExecutionRetirement
	if current == nil {
		if want != nil || at.Before(s.ExecutionResult.HistoryExpiredAt) {
			return CollectionState{}, false, ErrCollectionConflict
		}
		return s.Clone(), false, nil
	}
	if want == nil {
		return s.Clone(), true, nil // The original first page already committed.
	}
	if want.validateState(s) != nil || !want.StartedAt.Equal(current.StartedAt) || want.UpdatedAt.After(current.UpdatedAt) {
		return CollectionState{}, false, ErrCollectionConflict
	}
	if want.Checkpoint != nil && want.Checkpoint.Progress.Processed < current.Checkpoint.Progress.Processed {
		return s.Clone(), true, nil
	}
	if !want.PreparedRemoved && current.PreparedRemoved {
		comparison := want.Clone()
		comparison.PreparedRemoved, comparison.UpdatedAt = current.PreparedRemoved, current.UpdatedAt
		if collectionExecutionRetirementsEqual(&comparison, current) {
			return s.Clone(), true, nil
		}
	}
	if !collectionExecutionRetirementsEqual(want, current) || at.Before(current.UpdatedAt) {
		return CollectionState{}, false, ErrCollectionConflict
	}
	return s.Clone(), current.complete(s), nil
}

// The isolated transition gate remains held while the FSM lock is released for
// certification. Every frozen reader closes before the deletion transaction.
// Historical replay uses committed publication/expiry facts, never local clocks
// or the current history retention state to choose a different removal prefix.
func (f *machine) prepareCollectionExecutionRetirement(c CollectionExecuteCommand, at time.Time) error {
	if f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return ErrAuthenticationResetRequired
	}
	if f.bootstrapPending() {
		return ErrBootstrapPending
	}
	s, replayed, err := f.collectionExecutionRetirementState(c, at)
	if err != nil || replayed {
		return err
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable).Err
	}
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	ledger, observed, captured := f.collections, f.image.Index, f.image
	view, err := ledger.Freeze()
	if err != nil {
		return f.collectionStorageFailure(err).Err
	}
	f.mu.Unlock()
	err = certifyCollectionExecutionRetirement(context.Background(), captured, s, view)
	closeErr := view.Close()
	f.mu.Lock()
	if ledger != f.collections || observed != f.image.Index {
		return f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	if closeErr != nil {
		return f.collectionStorageFailure(errors.Join(err, closeErr)).Err
	}
	if err != nil {
		return f.collectionStorageFailure(err).Err
	}
	return nil
}

func certifyCollectionExecutionRetirement(ctx context.Context, captured image, s CollectionState, view *collectionLedgerView) error {
	seenChildren, seenTokens := make(map[string]bool), make(map[uint64]bool)
	observe := func(o CollectionItemOutcome) error {
		if o.Receipt == nil {
			return nil
		}
		id := o.Receipt.ID
		_, pending := captured.Operations[id]
		_, reserved := captured.OperationReservations[id]
		if pending || reserved || seenChildren[id] || seenTokens[o.MutationSequence] {
			return ErrCollectionInvalid
		}
		seenChildren[id], seenTokens[o.MutationSequence] = true, true
		return nil
	}
	if s.ExecutionRetirement != nil {
		return validateCollectionExecutionRetiredParent(ctx, captured, s, view, observe)
	}
	x, err := buildCollectionExecutionAuditIndex(ctx, s, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		return err
	}
	if err := verifyCollectionExecutionResults(ctx, s, x, view); err != nil {
		return err
	}
	var proof *collectionExecutionParentRecovery
	if s.Execution != nil {
		if err := collectionExecutionRecoveryHeader(captured, s); err != nil {
			return err
		}
		proof, err = rebuildCollectionExecutionParent(ctx, captured, s, view, observe, x)
		if err != nil {
			return err
		}
	}
	if err := validateCollectionExecutionPublishedPrefix(ctx, s, view, x, proof); err != nil {
		return err
	}
	// The exact local namespace audit also excludes rows hidden by damaged
	// metadata counters. This does not inspect unrelated collection parents.
	expected := collectionExecutionStats{}
	if s.Execution != nil {
		checkpoint, err := NewCollectionExecutionRetirementCheckpoint(s.Execution.Binding, s.ItemCount, s.Activation.At)
		if err != nil {
			return err
		}
		expected, err = collectionExecutionRetirementStats(checkpoint, *s.Execution, false)
		if err != nil {
			return err
		}
	}
	return validateCollectionExecutionRetirementNamespace(ctx, view, s.ID, expected)
}

func (f *machine) retireCollectionExecution(c CollectionExecuteCommand, at time.Time) Result {
	s, replayed, err := f.collectionExecutionRetirementState(c, at)
	if err != nil {
		return Result{Err: err}
	}
	if replayed {
		return collectionResult(s)
	}
	r := s.ExecutionRetirement
	if r == nil {
		r = &CollectionExecutionRetirementState{Version: collectionExecutionRetirementVersion,
			Result: s.ExecutionResult.Clone(), StartedAt: at, UpdatedAt: at}
		if s.Execution != nil {
			checkpoint, err := NewCollectionExecutionRetirementCheckpoint(s.Execution.Binding, s.ItemCount, s.Activation.At)
			if err != nil {
				return f.collectionStorageFailure(err)
			}
			r.Checkpoint = &checkpoint
		}
		s.ExecutionRetirement = r
	}
	if s.Execution != nil {
		removed, err := f.collections.RetireExecutionPrefix(*r.Checkpoint, *s.Execution, r.PreparedRemoved)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
		if removed.Records == 0 && !removed.Complete || removed.Records > collectionLedgerBatchLimit || removed.EncodedBytes > collectionLedgerBatchBytes {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		r.Checkpoint, r.PreparedRemoved = &removed.Checkpoint, removed.PreparedRemoved
	}
	r.UpdatedAt = at
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	f.image.Collections[s.ID] = s
	f.image.Version = max(f.image.Version, CollectionExecutionRetirementFormatVersion)
	delete(f.collectionOutcomeCommitments, s.ID)
	delete(f.collectionTerminalTrees, s.ID)
	return collectionResult(s)
}
