package persistence

import (
	"context"
	"errors"
	"time"
)

func (f *machine) collectionExecutionFinalizationState(c CollectionExecuteCommand, at time.Time) (CollectionState, error) {
	if c.Action != "finalize" || c.validate(at) != nil {
		return CollectionState{}, ErrCollectionInvalid
	}
	s, ok := f.image.Collections[c.Binding.OperationID]
	if !ok {
		return CollectionState{}, ErrOperationNotFound
	}
	b, err := collectionExecutionBindingFor(s)
	if err != nil || b != c.Binding || s.validate() != nil || !collectionExecutionFinalizeFencesEqual(c.Finalize, CollectionExecutionFinalizeFenceFor(s)) ||
		at.Before(s.Activation.At) || at.Before(s.TerminalAt) || s.Execution != nil && at.Before(s.Execution.LastAt) || s.ExecutionResult != nil && at.Before(s.ExecutionResult.Summary.FinalizedAt) {
		return CollectionState{}, ErrCollectionConflict
	}
	epoch, _, _ := ParseOperationHandle(s.ID)
	if s.Phase == "applying" && epoch != f.image.OperationEpoch {
		return CollectionState{}, ErrOperationExpired
	}
	if _, ready := collectionExecutionOutcome(*c.Finalize); !ready {
		return CollectionState{}, ErrCollectionConflict
	}
	return s.Clone(), nil
}

// Enter and return with f.mu held under the isolated transition gate. The
// original operation's complete frozen namespaces are certified, not just
// matching counters or a disposable hot cache. Readers close before any mutation
// or history write. The transition gate keeps the image immutable while f.mu is
// released.
func (f *machine) prepareCollectionExecutionFinalization(c CollectionExecuteCommand, at time.Time) error {
	s, err := f.collectionExecutionFinalizationState(c, at)
	if err != nil {
		return err
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable).Err
	}
	// The cold certifier owns the sole bounded original-plan index. Do not
	// retain the executor's disposable index while constructing another one.
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	ledger, observed, i := f.collections, f.image.Index, f.image
	view, err := ledger.Freeze()
	if err != nil {
		return f.collectionStorageFailure(err).Err
	}
	f.mu.Unlock()
	err = certifyCollectionExecutionFinalization(context.Background(), i, s, view)
	closeErr := view.Close()
	f.mu.Lock()
	if ledger != f.collections || observed != f.image.Index {
		return f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	if closeErr != nil {
		return f.collectionStorageFailure(errors.Join(err, closeErr)).Err
	}
	if errors.Is(err, errCollectionExecutionIndexLimit) && s.Execution == nil {
		// Admission predates executor bounds. A valid artifact too large for this
		// certifier remains retained and unfinalized; it is not storage corruption.
		return ErrCollectionQuota
	}
	if err != nil {
		return f.collectionStorageFailure(err).Err
	}
	return nil
}

// Called only after complete certification in the isolated envelope prepass.
func (f *machine) finalizeCollectionExecution(c CollectionExecuteCommand, at time.Time) Result {
	s, err := f.collectionExecutionFinalizationState(c, at)
	if err != nil {
		return Result{Err: err}
	}
	if s.ExecutionResult != nil {
		return collectionResult(s)
	}
	outcome, _ := collectionExecutionOutcome(*c.Finalize)
	summary := CollectionExecutionSummary{Version: collectionExecutionResultVersion, Binding: c.Binding, Actor: s.Actor, IdentityFormat: s.IdentityFormat,
		NormalizationProfile: s.NormalizationProfile, ContentDigest: s.ContentDigest, ItemCount: s.ItemCount, ActivationAt: s.Activation.At, Fence: c.Finalize.Clone(), Unattempted: s.ItemCount, Outcome: outcome, FinalizedAt: at}
	if s.Execution != nil {
		summary.Unattempted -= s.Execution.Processed
	}
	s.ExecutionResult = &CollectionExecutionResultState{Summary: summary}
	if s.Phase == "applying" {
		s.Phase, s.TerminalAt = outcome, at
	}
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	f.image.Collections[s.ID] = s
	f.image.Version = max(f.image.Version, CollectionExecutionResultFormatVersion)
	result := collectionResult(s)
	result.Events = []Event{collectionExecutionResultEvent(summary)}
	// Stopped parents already have an immutable receipt. Preserve it verbatim.
	if c.Finalize.Phase == "applying" {
		result.Events = append(result.Events, collectionReceiptEvent(collectionReceiptFor(s)))
	}
	return result
}

// Global child/allocation uniqueness is established at admission and full
// snapshot recovery. Finalization rechecks the selected parent's original
// artifacts, complete namespace and child absence without scanning unrelated
// retained parents. Nothing here installs a disposable cache as authority.
func certifyCollectionExecutionFinalization(ctx context.Context, i image, s CollectionState, view *collectionLedgerView) error {
	x, err := buildCollectionExecutionAuditIndex(ctx, s, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		return err
	}
	if err := verifyCollectionExecutionResults(ctx, s, x, view); err != nil {
		return err
	}
	stat, err := view.ExecutionStats(s.ID)
	if err != nil {
		return err
	}
	if s.Execution == nil {
		if stat != (collectionExecutionStats{}) {
			return ErrCollectionInvalid
		}
		return nil
	}
	if err := collectionExecutionRecoveryHeader(i, s); err != nil {
		return err
	}
	seenChildren, seenTokens := make(map[string]bool), make(map[uint64]bool)
	_, err = rebuildCollectionExecutionParent(ctx, i, s, view, func(o CollectionItemOutcome) error {
		if o.Receipt == nil {
			return nil
		}
		id := o.Receipt.ID
		if _, ok := i.Operations[id]; ok {
			return ErrCollectionInvalid
		}
		if _, ok := i.OperationReservations[id]; ok {
			return ErrCollectionInvalid
		}
		if seenChildren[id] || seenTokens[o.MutationSequence] {
			return ErrCollectionInvalid
		}
		seenChildren[id], seenTokens[o.MutationSequence] = true, true
		return nil
	}, x)
	if err != nil {
		return err
	}
	// A native bucket's cached record count alone cannot exclude an extra row.
	// Page the one namespace to exhaustion, including the unaccepted prepared slot.
	expected := stat.Outcomes + stat.Terminals
	if stat.Prepared {
		expected++
	}
	var observed uint64
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := view.ExecutionPage(s.ID, after, collectionLedgerPageLimit)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		observed += uint64(len(page))
		if observed > expected {
			return ErrCollectionInvalid
		}
		_, after, err = page[len(page)-1].identity()
		if err != nil {
			return err
		}
	}
	if observed != expected {
		return ErrCollectionInvalid
	}
	return nil
}
