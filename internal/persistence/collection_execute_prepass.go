package persistence

import (
	"context"
	"errors"
	"time"
)

// prepareCollectionExecutionIndex is entered and returns with f.mu held, under
// f.transition. The isolated envelope cannot admit or rewrite its own plan.
// Only a bounded immutable index survives; every frozen view closes before the
// first mutation or bbolt write. Request cancellation never changes replay.
func (f *machine) prepareCollectionExecutionIndex(c CollectionExecuteCommand, at time.Time) (*collectionExecutionIndex, error) {
	f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	if f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return nil, ErrAuthenticationResetRequired
	}
	if f.bootstrapPending() {
		return nil, ErrBootstrapPending
	}
	if c.Action == "finalize" {
		return nil, f.prepareCollectionExecutionFinalization(c, at)
	}
	s, err := f.collectionExecutionState(c, at)
	if err != nil {
		return nil, err
	}
	if f.collections == nil {
		return nil, f.collectionStorageFailure(ErrCollectionUnavailable).Err
	}
	if f.collectionExecutionLedger == f.collections && f.collectionExecutionIndex != nil && f.collectionExecutionIndex.matches(s) {
		return f.collectionExecutionIndex, nil
	}
	// Keep at most one plan index, including while reconstructing its successor.
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	ledger, observed := f.collections, f.image.Index
	view, err := ledger.Freeze()
	if err != nil {
		return nil, f.collectionStorageFailure(err).Err
	}
	f.mu.Unlock()
	x, err := buildCollectionExecutionIndex(context.Background(), s, view, defaultCollectionExecutionIndexLimits())
	if err == nil {
		err = verifyCollectionExecutionResults(context.Background(), s, x, view)
	}
	closeErr := view.Close()
	f.mu.Lock()
	if ledger != f.collections || observed != f.image.Index {
		return nil, f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	if closeErr != nil {
		return nil, f.collectionStorageFailure(errors.Join(err, closeErr)).Err
	}
	if err != nil {
		// An admitted but not started operation may exceed the executor's
		// published bounded-work limits. It has made no execution commitment.
		if errors.Is(err, errCollectionExecutionIndexLimit) && s.Execution == nil {
			return nil, ErrCollectionQuota
		}
		return nil, f.collectionStorageFailure(err).Err
	}
	current, exists := f.image.Collections[s.ID]
	if !exists || !x.matches(current) {
		return nil, f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	f.collectionExecutionIndex, f.collectionExecutionLedger = x, ledger
	return x, nil
}

// recoverCollectionExecution is called only for a complete snapshot whose
// original input, plan and validation namespaces already passed validation.
// The returned caches are detached before a caller publishes the generation.
func recoverCollectionExecution(i image, ledger *collectionLedger) (*collectionExecutionRecovery, error) {
	if !collectionExecutionStorageFormat(i.Version) {
		return nil, nil
	}
	if ledger == nil {
		return nil, ErrCollectionUnavailable
	}
	view, err := ledger.Freeze()
	if err != nil {
		return nil, err
	}
	recovery, err := validateCollectionExecutionInventory(context.Background(), i, view)
	return recovery, errors.Join(err, view.Close())
}

func (f *machine) installCollectionExecutionRecovery(r *collectionExecutionRecovery) {
	f.collectionChildren, f.collectionTerminalTrees, f.collectionOutcomeCommitments = nil, nil, nil
	if r != nil {
		f.collectionChildren, f.collectionTerminalTrees, f.collectionOutcomeCommitments = r.children, r.trees, r.commitments
	}
}
