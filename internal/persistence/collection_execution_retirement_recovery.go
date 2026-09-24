package persistence

import (
	"bytes"
	"context"
)

// validateCollectionExecutionRetiredParent reconstructs the immutable original
// execution from a committed retired prefix and every remaining original row.
// Before source retirement, input, plan and validation remain complete and its
// temporary audit index never becomes an executor/preparation cache. Source
// retirement instead verifies committed surviving-prefix digests without an
// executable index. Neither path grants child ownership or execution progress.
func validateCollectionExecutionRetiredParent(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, observe func(CollectionItemOutcome) error) error {
	if ctx == nil || view == nil {
		return ErrCollectionInvalid
	}
	if err := collectionExecutionRetiredRecoveryHeader(i, s); err != nil {
		return err
	}
	if s.ExecutionRetirement.Sources != nil {
		// The selected source auditor also requires an empty execution namespace.
		return validateCollectionExecutionSourcePrefixes(ctx, i, s, view)
	}
	x, err := buildCollectionExecutionAuditIndex(ctx, s, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		return err
	}
	if err := verifyCollectionExecutionResults(ctx, s, x, view); err != nil {
		return err
	}
	if s.Execution == nil {
		return collectionExecutionRetiredRecoveryStats(ctx, s, view)
	}
	r := s.ExecutionRetirement
	checkpoint := r.Checkpoint.Clone()
	for ordinal := checkpoint.Progress.Processed + 1; ordinal <= s.Execution.Processed; ordinal++ {
		record, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, collectionExecutionOutcomeSlot(ordinal))
		if err != nil {
			return err
		}
		row, found := x.row(ordinal)
		if record.Outcome == nil || !found || !collectionExecutionOutcomeOriginal(i, s, row, *record.Outcome) {
			return ErrCollectionInvalid
		}
		outcome := *record.Outcome
		terminal, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, collectionExecutionTerminalSlot(ordinal))
		if err != nil {
			return err
		}
		// A finalized parent's accepted child must already be settled. Missing
		// terminals cannot reopen an ordinary operation or release its reserve.
		if (outcome.Decision == "accepted") != (terminal.Terminal != nil) {
			return ErrCollectionInvalid
		}
		if outcome.Receipt != nil {
			if _, pending := i.Operations[outcome.Receipt.ID]; pending {
				return ErrCollectionInvalid
			}
			if _, reserved := i.OperationReservations[outcome.Receipt.ID]; reserved {
				return ErrCollectionInvalid
			}
		}
		checkpoint, err = checkpoint.append(outcome, terminal.Terminal)
		if err != nil {
			return err
		}
		if observe != nil {
			if err := observe(outcome); err != nil {
				return err
			}
		}
	}
	if !checkpoint.matchesFinal(*s.Execution) {
		return ErrCollectionInvalid
	}
	prepared, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, "prepared")
	if err != nil {
		return err
	}
	if (prepared.Prepared != nil) != (s.Execution.Prepared != nil && !r.PreparedRemoved) {
		return ErrCollectionInvalid
	}
	if candidate := prepared.Prepared; candidate != nil {
		row, found := x.row(candidate.Ordinal)
		if !found || candidate.Binding != s.Execution.Binding || candidate.Ordinal != s.Execution.Processed+1 ||
			candidate.InputOrdinal != row.Row.InputOrdinal || candidate.RowDigest != row.RowDigest || candidate.Record.Key != row.Row.Key ||
			!collectionExecutionDesiredTuple(row.Row, candidate.Record.UID, candidate.Record.Revision, candidate.Record.Generation) {
			return ErrCollectionInvalid
		}
		commitment, err := collectionExecutionPreparedCommitment(*candidate)
		if err != nil {
			return err
		}
		original := *s.Execution.Prepared
		if !commitment.At.Equal(original.At) {
			return ErrCollectionInvalid
		}
		commitment.At = original.At
		if commitment != original {
			return ErrCollectionInvalid
		}
	}
	// Published item re-derivation requires deleted source outcomes. Retirement
	// instead binds the original result/publication state before this audit and
	// reconstructs its execution commitments above. It never invents item rows or
	// establishes current history availability from the retained descriptor.
	return collectionExecutionRetiredRecoveryStats(ctx, s, view)
}

func collectionExecutionRetiredRecoveryHeader(i image, s CollectionState) error {
	if s.ExecutionRetirement == nil || s.validate() != nil || s.ExecutionRetirement.validateState(s) != nil ||
		s.ExecutionRetirement.Sources == nil && !collectionExecutionArtifactsComplete(s) || s.Plan == nil || s.Plan.Header.ObservedIndex > i.Index {
		return ErrCollectionInvalid
	}
	if _, err := collectionExecutionRetiredExpectedStats(s); err != nil {
		return err
	}
	return collectionExecutionRecoveryIdentity(i, s)
}

func collectionExecutionRetiredExpectedStats(s CollectionState) (collectionExecutionStats, error) {
	if s.ExecutionRetirement == nil || s.ExecutionRetirement.validateState(s) != nil {
		return collectionExecutionStats{}, ErrCollectionInvalid
	}
	if s.Execution == nil {
		return collectionExecutionStats{}, nil
	}
	return collectionExecutionRetirementStats(*s.ExecutionRetirement.Checkpoint, *s.Execution, s.ExecutionRetirement.PreparedRemoved)
}

func collectionExecutionRemainingCharge(s CollectionState) (int64, error) {
	if s.ExecutionRetirement != nil {
		stats, err := collectionExecutionRetiredExpectedStats(s)
		return stats.ChargedBytes, err
	}
	if s.Execution == nil {
		return 0, nil
	}
	return s.Execution.ChargedBytes, nil
}

func collectionExecutionRetiredRecoveryStats(ctx context.Context, s CollectionState, view *collectionLedgerView) error {
	expected, err := collectionExecutionRetiredExpectedStats(s)
	if err != nil {
		return err
	}
	return validateCollectionExecutionRetirementNamespace(ctx, view, s.ID, expected)
}

// validateCollectionExecutionRetirementNamespace audits exactly one physical
// execution namespace against expected remaining statistics. It is shared by
// the first retirement prepass and recovery of an already retired prefix.
func validateCollectionExecutionRetirementNamespace(ctx context.Context, view *collectionLedgerView, op string, expected collectionExecutionStats) error {
	if ctx == nil || view == nil {
		return ErrCollectionInvalid
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	actual, err := view.executionStat(op)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrCollectionInvalid
	}
	// Point reconstruction alone cannot reject an extra row hidden behind a
	// fabricated bucket count. Traverse only this parent's physical namespace,
	// independently recomputing its exact remaining accounting and slot ranges.
	observed := collectionExecutionStats{Binding: expected.Binding, RetiredOutcomes: expected.RetiredOutcomes}
	visit := func(slot string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bounded, err := view.encodedExecution(op, slot)
		if err != nil || !bytes.Equal(bounded, raw) {
			return ErrCollectionInvalid
		}
		record, err := decodeExecutionAt(op, slot, raw)
		if err != nil || collectionExecutionBinding(record) != expected.Binding {
			return ErrCollectionInvalid
		}
		switch {
		case record.Prepared != nil:
			if !expected.Prepared || observed.Prepared || record.Prepared.Ordinal != expected.RetiredOutcomes+expected.Outcomes+1 {
				return ErrCollectionInvalid
			}
			observed.Prepared = true
		case record.Outcome != nil:
			if record.Outcome.Ordinal <= expected.RetiredOutcomes || record.Outcome.Ordinal > expected.RetiredOutcomes+expected.Outcomes {
				return ErrCollectionInvalid
			}
			observed.Outcomes++
			if record.Outcome.Receipt != nil {
				observed.TerminalCapacity += collectionChildTerminalReserve
			}
		case record.Terminal != nil:
			if record.Terminal.Ordinal <= expected.RetiredOutcomes || record.Terminal.Ordinal > expected.RetiredOutcomes+expected.Outcomes {
				return ErrCollectionInvalid
			}
			accepted, err := view.encodedExecution(op, collectionExecutionOutcomeSlot(record.Terminal.Ordinal))
			if err != nil {
				return err
			}
			outcome, err := decodeCollectionExecutionRecord(accepted)
			if err != nil || outcome.Outcome == nil || !record.Terminal.matches(*outcome.Outcome) {
				return ErrCollectionInvalid
			}
			observed.Terminals++
			observed.TerminalBytes += int64(len(raw))
		}
		observed.EncodedBytes += int64(len(raw))
		observed.ChargedBytes += collectionExecutionCharge(record, raw)
		if observed.Outcomes > expected.Outcomes || observed.Terminals > expected.Terminals ||
			observed.EncodedBytes > expected.EncodedBytes || observed.ChargedBytes > expected.ChargedBytes {
			return ErrCollectionInvalid
		}
		return nil
	}
	if view.tx == nil {
		order := view.executionOrder[op]
		if len(view.executionRows[op]) != 0 && order == nil {
			return ErrCollectionInvalid
		}
		for slot, raw := range view.executionRows[op] {
			if !order.Has(slot) {
				return ErrCollectionInvalid
			}
			if err := visit(slot, raw); err != nil {
				return err
			}
		}
	} else {
		root := view.tx.Bucket(collectionLedgerExecution)
		if root == nil {
			return ErrCollectionInvalid
		}
		if bucket := root.Bucket([]byte(op)); bucket != nil {
			if err := bucket.ForEach(func(slot, raw []byte) error { return visit(string(slot), raw) }); err != nil {
				return err
			}
		}
	}
	if observed != expected {
		return ErrCollectionInvalid
	}
	return ctx.Err()
}
