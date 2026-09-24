package persistence

import (
	"context"
	"reflect"
)

// collectionPreparationCaches runs under FSM ownership and performs no ledger
// I/O. These caches are installed by verified recovery or a successful atomic
// transaction. Their identities must match committed progress before reuse;
// merely canonical ledger rows or matching row counts cannot replace them.
func collectionPreparationCaches(f *machine, s CollectionState) error {
	p := s.Execution
	if p == nil {
		if f.collectionOutcomeCommitments[s.ID] != nil || f.collectionTerminalTrees[s.ID] != nil {
			return ErrCollectionUnavailable
		}
		return nil
	}
	if p.validateState(s) != nil || !f.collectionOutcomeCommitments[s.ID].matches(p.Binding, p.Processed, p.OutcomeDigest) {
		return ErrCollectionUnavailable
	}
	tree := f.collectionTerminalTrees[s.ID]
	if tree == nil {
		if p.ChildTerminals != 0 || p.TerminalRoot != collectionExecutionTerminalEmptyRoot() {
			return ErrCollectionUnavailable
		}
	} else if tree.itemCount != p.ItemCount || tree.Root() != p.TerminalRoot {
		return ErrCollectionUnavailable
	}
	return nil
}

// Only independent child completion may change while a detached candidate is
// prepared. Every original field and outcome/prepared identity remains fixed.
// Both headers must already be structurally validated. Fresh authority, current
// cache commitments and the original target are checked separately by the view.
func collectionPreparationSameInput(a, b CollectionState) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	if a.Execution == nil || b.Execution == nil {
		return false
	}
	old, next := a.Execution, *b.Execution
	if next.ChildTerminals <= old.ChildTerminals || next.ChildApplied < old.ChildApplied || next.ChildFailed < old.ChildFailed ||
		next.ChildSuperseded < old.ChildSuperseded || next.ChildInvalidated < old.ChildInvalidated || next.TerminalBytes <= old.TerminalBytes || next.LastAt.Before(old.LastAt) {
		return false
	}
	next.ChildTerminals, next.ChildApplied, next.ChildFailed = old.ChildTerminals, old.ChildApplied, old.ChildFailed
	next.ChildSuperseded, next.ChildInvalidated = old.ChildSuperseded, old.ChildInvalidated
	next.TerminalBytes, next.EncodedBytes, next.TerminalRoot, next.LastAt = old.TerminalBytes, old.EncodedBytes, old.TerminalRoot, old.LastAt
	b.Execution = &next
	return reflect.DeepEqual(a, b)
}

// verifyCachedProgress checks bounded per-parent metadata against the same
// frozen generation captured with the authoritative header/cache pointers.
// It does not rescan completed outcomes or the complete immutable namespaces.
func (v *CollectionExecutionPreparationView) verifyCachedProgress(ctx context.Context) error {
	s, view := v.head, v.ledger
	if err := collectionExecutionInputStats(ctx, view, s); err != nil {
		return err
	}
	if err := collectionExecutionPlanStats(ctx, view, s.ID, s.Plan.UploadedFragments, s.Plan.EncodedBytes); err != nil {
		return err
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	count, used, err := view.validationStats(s.ID)
	if err != nil || count != s.Validation.Descriptor.Count || used != s.Validation.EncodedBytes {
		return ErrCollectionUnavailable
	}
	stat, err := view.executionStat(s.ID)
	if err != nil {
		return err
	}
	p := s.Execution
	if p == nil || p.Processed == 0 && p.Prepared == nil {
		if stat != (collectionExecutionStats{}) {
			return ErrCollectionUnavailable
		}
	} else if stat.Binding != p.Binding || stat.Outcomes != p.Processed || stat.Terminals != p.ChildTerminals || stat.Prepared != (p.Prepared != nil) ||
		stat.EncodedBytes != p.EncodedBytes || stat.TerminalBytes != p.TerminalBytes || stat.ChargedBytes != p.ChargedBytes ||
		stat.TerminalCapacity != int64(p.Accepted)*collectionChildTerminalReserve {
		return ErrCollectionUnavailable
	}
	return ctx.Err()
}
