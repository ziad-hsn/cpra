package persistence

import (
	"context"
	"errors"
	"math"
	"time"
)

// CollectionExecutionFormatVersion adds isolated item commands and the complete
// execution ledger. Older snapshots cannot preserve execution progress.
const CollectionExecutionFormatVersion = 9

const collectionExecutionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-9\n"

func collectionActivationStorageFormat(version int) bool {
	return version == CollectionActivationFormatVersion || collectionExecutionStorageFormat(version)
}

// applyCollectionExecution requires the machine lock and an original index
// reconstructed before the isolated envelope begins. No current observation
// escapes this critical section, and all ledger readers close before writes.
func (f *machine) applyCollectionExecution(c CollectionExecuteCommand, index uint64, at time.Time, x *collectionExecutionIndex) Result {
	if c.Action == "finalize" {
		return f.finalizeCollectionExecution(c, at)
	}
	s, err := f.collectionExecutionState(c, at)
	if err != nil {
		return Result{Err: err}
	}
	if x == nil || !x.matches(s) || f.collections == nil || index == 0 {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	if c.Action == "begin" {
		if s.Execution != nil {
			if err := f.checkCollectionExecutionMaterialization(s); err != nil {
				return f.collectionStorageFailure(err)
			}
			return collectionResult(s)
		}
		stats, err := f.collections.ExecutionStats(s.ID)
		if err != nil || stats != (collectionExecutionStats{}) {
			return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
		}
		progress, err := NewCollectionExecutionProgress(c.Binding, s.ItemCount, s.Activation.At)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
		certified, err := newCollectionExecutionOutcomeCommitments(c.Binding)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
		s.Execution = &progress
		f.image.Collections[s.ID] = s
		if f.collectionOutcomeCommitments == nil {
			f.collectionOutcomeCommitments = make(map[string]*collectionExecutionOutcomeCommitments)
		}
		f.collectionOutcomeCommitments[s.ID] = certified
		f.image.Version = max(f.image.Version, CollectionExecutionFormatVersion)
		return collectionResult(s)
	}
	if s.Execution == nil {
		return Result{Err: ErrCollectionConflict}
	}
	if err := f.checkCollectionExecutionMaterialization(s); err != nil {
		return f.collectionStorageFailure(err)
	}
	row, found := x.row(c.Ordinal)
	if !found {
		return Result{Err: ErrCollectionConflict}
	}
	if c.Action == "prepare" {
		return f.prepareCollectionExecutionItem(c, s, row)
	}
	if c.Ordinal <= s.Execution.Processed {
		r, found, err := f.collections.ExecutionRecord(s.ID, collectionExecutionOutcomeSlot(c.Ordinal))
		if err != nil || !found || !f.collectionOutcomeCommitments[s.ID].matchesRecord(r) {
			return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
		}
		if r.Outcome.PreparedID != c.PreparedID || r.Outcome.RowDigest != row.RowDigest {
			return Result{Err: ErrCollectionConflict}
		}
		return collectionResult(s) // Original disposition, no new receipt/events.
	}
	if c.Ordinal != s.Execution.Processed+1 {
		return Result{Err: ErrCollectionConflict}
	}
	if s.Execution.Prepared != nil && at.Before(s.Execution.Prepared.At) {
		return Result{Err: ErrCollectionConflict}
	}
	return f.decideCollectionExecutionItem(c, s, row, x, index, at)
}

func (f *machine) checkCollectionExecutionMaterialization(s CollectionState) error {
	p := s.Execution
	if p == nil || p.validateState(s) != nil || !f.collectionOutcomeCommitments[s.ID].matches(p.Binding, p.Processed, p.OutcomeDigest) {
		return ErrCollectionInvalid
	}
	stat, err := f.collections.ExecutionStats(s.ID)
	if err != nil {
		return err
	}
	if p.Processed == 0 && p.Prepared == nil {
		if stat != (collectionExecutionStats{}) {
			return ErrCollectionInvalid
		}
		return nil
	}
	if stat.Binding != p.Binding || stat.Outcomes != p.Processed || stat.Terminals != p.ChildTerminals || stat.Prepared != (p.Prepared != nil) ||
		stat.EncodedBytes != p.EncodedBytes || stat.TerminalBytes != p.TerminalBytes || stat.ChargedBytes != p.ChargedBytes ||
		stat.TerminalCapacity != int64(p.Accepted)*collectionChildTerminalReserve {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionPreparedRowMatches(p CollectionPreparedItem, row collectionExecutionRow) bool {
	if p.Ordinal != row.Row.Ordinal || p.InputOrdinal != row.Row.InputOrdinal || p.RowDigest != row.RowDigest || p.Record.Key != row.Row.Key {
		return false
	}
	switch row.Row.Change {
	case "create":
		return p.Record.Generation == 1
	case "update":
		g := uint64(row.Row.Target.OriginalGeneration)
		return p.Record.UID == row.Row.Target.OriginalUID && p.Record.Revision != row.Row.Target.OriginalRevision &&
			p.Record.Generation >= g && p.Record.Generation-g <= 1
	default:
		return false
	}
}

func (f *machine) prepareCollectionExecutionItem(c CollectionExecuteCommand, s CollectionState, row collectionExecutionRow) Result {
	p := c.Prepared
	if c.Ordinal != s.Execution.Processed+1 || p == nil || !collectionPreparedRowMatches(*p, row) {
		return Result{Err: ErrCollectionConflict}
	}
	if err := validateCatalogJobTypeReferences(f.image, p.Record, false); err != nil {
		return Result{Err: err}
	}
	next, err := s.Execution.withPrepared(*p)
	if err != nil {
		return Result{Err: err}
	}
	s.Execution = &next
	if s.validate() != nil {
		return Result{Err: ErrCollectionConflict}
	}
	if err := f.collections.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Prepared: p}}); err != nil {
		if errors.Is(err, errCollectionLedgerQuota) {
			return Result{Err: ErrCollectionQuota}
		}
		return f.collectionStorageFailure(err)
	}
	f.image.Collections[s.ID] = s
	f.image.Version = max(f.image.Version, CollectionExecutionFormatVersion, catalogRecordMinimumFormat(p.Record))
	return collectionResult(s)
}

func collectionOriginalTargetMatches(g CollectionPlanGuard, record CatalogRecord, exists bool) bool {
	active := exists && !record.Removed
	if g.Absent {
		return !active
	}
	return active && record.UID == g.OriginalUID && record.Revision == g.OriginalRevision && record.Generation == uint64(g.OriginalGeneration)
}

func (f *machine) decideCollectionExecutionItem(c CollectionExecuteCommand, s CollectionState, row collectionExecutionRow, x *collectionExecutionIndex, index uint64, at time.Time) Result {
	o := CollectionItemOutcome{Binding: c.Binding, Ordinal: c.Ordinal, InputOrdinal: row.Row.InputOrdinal, RowDigest: row.RowDigest,
		Key: row.Row.Key, Source: row.Row.Source, SourceDocument: row.Row.Document, SourceItem: row.Row.Item,
		PreparedID: c.PreparedID, CommittedIndex: index, At: at}
	current, exists := f.image.Catalog[row.Row.Key.indexKey()]
	var candidate CatalogRecord
	var guard collectionItemGuardDecision
	err := f.collections.read(func(view *collectionLedgerView) error {
		if c.PreparedID == "" && row.Row.Change != "unchanged" {
			// Preparation may be impossible because the exact original target
			// needed for credential omission has changed. Only an actual target
			// identity conflict can certify this no-candidate disposition.
			if s.Execution.Prepared != nil || collectionOriginalTargetMatches(row.Row.Target, current, exists) {
				return ErrCollectionConflict
			}
			if err := x.walkRow(context.Background(), view, c.Ordinal, func(CollectionPlanFragment) error { return nil }); err != nil {
				return err
			}
			guard.Code = collectionItemGuardsConflict
			return nil
		}
		if row.Row.Change == "unchanged" {
			if c.PreparedID != "" || s.Execution.Prepared != nil {
				return ErrCollectionConflict
			}
			candidate = current
			// A disappeared/changed target must still yield conflict, rather
			// than using a different current identity as an executable grant.
			candidate.Key, candidate.UID, candidate.Revision, candidate.Generation = row.Row.Key, row.Row.Target.OriginalUID, row.Row.Target.OriginalRevision, uint64(row.Row.Target.OriginalGeneration)
			candidate.Removed = false
		} else {
			if s.Execution.Prepared == nil || s.Execution.Prepared.ID != c.PreparedID {
				return ErrCollectionConflict
			}
			r, found, err := view.ExecutionRecord(s.ID, "prepared")
			if err != nil || !found || r.Prepared == nil {
				return errors.Join(err, ErrCollectionInvalid)
			}
			commitment, err := collectionExecutionPreparedCommitment(*r.Prepared)
			if err != nil || !collectionPreparedCommitmentsEqual(commitment, *s.Execution.Prepared) || !collectionPreparedRowMatches(*r.Prepared, row) {
				return errors.Join(err, ErrCollectionInvalid)
			}
			candidate = r.Prepared.Record
		}
		var err error
		guard, err = verifyCollectionItemGuards(context.Background(), x, view, c.Ordinal, candidate,
			func(_ context.Context, key CatalogKey) (CatalogRecord, bool, error) {
				r, ok := f.image.Catalog[key.indexKey()]
				return r, ok, nil
			}, func(_ context.Context, ordinal uint64) (collectionCertifiedItem, bool, error) {
				if ordinal > s.Execution.Processed {
					return collectionCertifiedItem{}, false, nil
				}
				r, found, err := view.ExecutionRecord(s.ID, collectionExecutionOutcomeSlot(ordinal))
				if err != nil || !found || !f.collectionOutcomeCommitments[s.ID].matchesRecord(r) {
					return collectionCertifiedItem{}, false, errors.Join(err, ErrCollectionPlanInvalid)
				}
				o := r.Outcome
				return collectionCertifiedItem{OperationID: o.Binding.OperationID, ActivationID: o.Binding.ActivationID,
					PlanID: o.Binding.PlanID, PlanDigest: o.Binding.PlanDigest, Ordinal: o.Ordinal, RowDigest: o.RowDigest,
					Key: o.Key, Decision: o.Decision, UID: o.UID, Revision: o.Revision, Generation: o.Generation, MutationSequence: o.MutationSequence}, true, nil
			}, defaultCollectionItemGuardLimits())
		return err
	})
	if err != nil {
		if errors.Is(err, ErrCollectionConflict) {
			return Result{Err: err}
		}
		return f.collectionStorageFailure(err)
	}
	var catalog *preparedCatalogMutation
	switch guard.Code {
	case collectionItemGuardsConflict:
		o.Decision = "conflict"
	case collectionItemGuardsDependencyBlocked:
		o.Decision = "dependencyBlocked"
	case collectionItemGuardsAllowed:
		if row.Row.Change == "unchanged" {
			o.Decision, o.UID, o.Revision, o.Generation, o.OldVersion = "unchanged", candidate.UID, candidate.Revision, candidate.Generation, candidate.Revision
			break
		}
		if f.image.OperationHighWater == math.MaxUint64 {
			return Result{Err: ErrCatalogBusy}
		}
		m := CatalogMutation{Record: candidate, Create: row.Row.Change == "create", Actor: s.Actor,
			OperationID: operationHandle(f.image.OperationEpoch, f.image.OperationHighWater+1)}
		if !m.Create {
			m.ExpectedUID, m.ExpectedRevision, m.ExpectedDependentsVersion = row.Row.Target.OriginalUID, row.Row.Target.OriginalRevision, current.DependentsVersion
		}
		// Item execution always uses unique mutation-token semantics. This
		// helper's token policy is independent of the outer opcode format.
		catalog, err = f.prepareCatalogMutation(m, index, at, max(CatalogMutationFormatVersion, catalogRecordMinimumFormat(candidate)))
		if err != nil {
			if errors.Is(err, ErrCatalogBusy) || errors.Is(err, ErrCatalogSequenceExhausted) {
				return Result{Err: err}
			}
			if errors.Is(err, ErrCatalogConflict) || errors.Is(err, ErrCatalogNotFound) || errors.Is(err, ErrCatalogDependency) || errors.Is(err, ErrCatalogReferenced) {
				o.Decision, catalog = "conflict", nil
				break
			}
			return f.collectionStorageFailure(err)
		}
		o.Decision, o.UID, o.Revision, o.Generation = "accepted", candidate.UID, candidate.Revision, candidate.Generation
		o.OldVersion, o.MutationSequence, o.Receipt = m.ExpectedRevision, catalog.sequence, catalog.result.Operation
	default:
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	return f.commitCollectionExecutionDecision(s, o, catalog)
}

func collectionPreparedCommitmentsEqual(a, b CollectionPreparedCommitment) bool {
	if !a.At.Equal(b.At) {
		return false
	}
	a.At, b.At = time.Time{}, time.Time{}
	return a == b
}

func (f *machine) commitCollectionExecutionDecision(s CollectionState, outcome CollectionItemOutcome, catalog *preparedCatalogMutation) Result {
	terminals, err := f.prepareCollectionChildCommit(collectionCatalogTerminals(catalog))
	if err != nil {
		return f.collectionStorageFailure(err)
	}
	if changed, exists := terminals.parents[s.ID]; exists {
		s = changed
	}
	progress, err := s.Execution.withOutcome(outcome)
	if err != nil {
		return f.collectionStorageFailure(err)
	}
	certified, err := f.collectionOutcomeCommitments[s.ID].append(outcome)
	if err != nil || !certified.matches(progress.Binding, progress.Processed, progress.OutcomeDigest) {
		return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
	}
	var link collectionChildLink
	if outcome.Receipt != nil {
		if len(f.collectionChildren)-len(terminals.children) >= maxPendingCatalogOperations {
			return Result{Err: ErrCatalogBusy}
		}
		link, err = collectionChildLinkFor(outcome, *outcome.Receipt)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
		if _, exists := f.collectionChildren[link.ChildID]; exists {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
	}
	s.Execution = &progress
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	records := append([]collectionExecutionRecord{{Version: 1, Outcome: &outcome}}, terminals.records...)
	if err := f.collections.ApplyExecutionBatch(records); err != nil {
		if errors.Is(err, errCollectionLedgerQuota) {
			return Result{Err: ErrCollectionQuota}
		}
		return f.collectionStorageFailure(err)
	}
	// From this point to the history barrier, installation has no fallible I/O.
	f.installCollectionChildCommit(terminals)
	var result Result
	if catalog != nil {
		f.image.OperationHighWater++
		result = f.installCatalogMutation(catalog)
		result.Events = append(result.Events, f.supersedeCatalogControls(*result.Catalog, outcome.At)...)
		if f.collectionChildren == nil {
			f.collectionChildren = make(collectionChildLinks)
		}
		f.collectionChildren[link.ChildID] = link
	}
	f.image.Collections[s.ID] = s
	f.collectionOutcomeCommitments[s.ID] = certified
	f.image.Version = max(f.image.Version, CollectionExecutionFormatVersion)
	result.Allowed, result.CollectionID = true, s.ID
	copy := s.Clone()
	result.Collection = &copy
	return result
}
