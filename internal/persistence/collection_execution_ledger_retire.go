package persistence

import bolt "go.etcd.io/bbolt"

// collectionExecutionRetirementBatch reports only the records removed by this
// transaction. Checkpoint is cumulative; the byte fields are exact live quota
// refunds. PreparedRemoved applies only to an original abandoned prepared slot.
type collectionExecutionRetirementBatch struct {
	Checkpoint      CollectionExecutionRetirementCheckpoint
	PreparedRemoved bool
	Records         int
	EncodedBytes    int64
	ChargedBytes    int64
	Complete        bool
}

// collectionExecutionRetirementStats derives live suffix accounting from the
// original immutable progress and cumulative retired commitments. It validates
// their shape, not their historical authority: the caller must certify the
// original final result and checkpoint before allowing physical retirement.
func collectionExecutionRetirementStats(checkpoint CollectionExecutionRetirementCheckpoint, final CollectionExecutionProgress, preparedRemoved bool) (collectionExecutionStats, error) {
	if checkpoint.validate() != nil || final.validate() != nil || final.Accepted != final.ChildTerminals {
		return collectionExecutionStats{}, ErrCollectionInvalid
	}
	p := checkpoint.Progress
	if p.Binding != final.Binding || p.ItemCount != final.ItemCount || !p.StartedAt.Equal(final.StartedAt) || p.LastAt.After(final.LastAt) ||
		p.Processed > final.Processed || p.Accepted > final.Accepted || p.Unchanged > final.Unchanged || p.Conflicts > final.Conflicts ||
		p.DependencyBlocked > final.DependencyBlocked || p.ChildTerminals > final.ChildTerminals || p.ChildApplied > final.ChildApplied ||
		p.ChildFailed > final.ChildFailed || p.ChildSuperseded > final.ChildSuperseded || p.ChildInvalidated > final.ChildInvalidated ||
		p.OutcomeBytes > final.OutcomeBytes || p.TerminalBytes > final.TerminalBytes || p.EncodedBytes > final.EncodedBytes || p.ChargedBytes > final.ChargedBytes ||
		preparedRemoved && (final.Prepared == nil || p.Processed != final.Processed) ||
		p.Processed == final.Processed && !checkpoint.matchesFinal(final) {
		return collectionExecutionStats{}, ErrCollectionInvalid
	}
	s := collectionExecutionStats{Binding: final.Binding, RetiredOutcomes: p.Processed, Prepared: final.Prepared != nil && !preparedRemoved,
		Outcomes: final.Processed - p.Processed, Terminals: final.ChildTerminals - p.ChildTerminals,
		EncodedBytes: final.EncodedBytes - p.EncodedBytes, TerminalBytes: final.TerminalBytes - p.TerminalBytes,
		TerminalCapacity: int64(final.Accepted-p.Accepted) * collectionChildTerminalReserve, ChargedBytes: final.ChargedBytes - p.ChargedBytes}
	if preparedRemoved {
		s.EncodedBytes -= final.Prepared.EncodedBytes
		s.ChargedBytes -= final.Prepared.EncodedBytes
	}
	if !s.Prepared && s.Outcomes == 0 && s.Terminals == 0 && s.EncodedBytes == 0 && s.TerminalBytes == 0 && s.TerminalCapacity == 0 && s.ChargedBytes == 0 {
		return collectionExecutionStats{}, nil
	}
	if s.validate(final.Binding.OperationID) != nil {
		return collectionExecutionStats{}, ErrCollectionInvalid
	}
	return s, nil
}

// RetireExecutionPrefix atomically removes at most 256 records / 4 MiB in
// increasing decision order. Accepted outcomes and their exact child terminals
// form one indivisible pair; only deleting that pair refunds terminal capacity.
// The abandoned prepared record is removed last and shares the same bounds.
//
// This is a storage primitive, not a cleanup admission API. Its caller supplies
// the committed checkpoint and immutable settled final progress, and must keep
// the returned checkpoint with the enclosing replicated transition. Stale
// checkpoints fail rather than inferring a new commitment from missing rows.
func (l *collectionLedger) RetireExecutionPrefix(checkpoint CollectionExecutionRetirementCheckpoint, final CollectionExecutionProgress, preparedRemoved bool) (collectionExecutionRetirementBatch, error) {
	if _, err := collectionExecutionRetirementStats(checkpoint, final, preparedRemoved); err != nil {
		return collectionExecutionRetirementBatch{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return collectionExecutionRetirementBatch{}, errCollectionLedgerClosed
	}
	var result collectionExecutionRetirementBatch
	if l.db != nil {
		err := l.db.Update(func(tx *bolt.Tx) error {
			p, next, err := planCollectionExecutionRetirement(&collectionLedgerView{tx: tx}, checkpoint, final, preparedRemoved)
			if err != nil {
				return err
			}
			if p != nil {
				if err := installCollectionExecutionDisk(tx, p); err != nil {
					return err
				}
			}
			result = next
			return nil
		})
		if err != nil {
			return collectionExecutionRetirementBatch{}, err
		}
		return result, nil
	}
	v := &collectionLedgerView{executionRows: l.executionRows, executionStats: l.executionStats, executionOrder: l.executionOrder, executionBytes: l.executionBytes, bytes: l.bytes}
	p, result, err := planCollectionExecutionRetirement(v, checkpoint, final, preparedRemoved)
	if err != nil {
		return collectionExecutionRetirementBatch{}, err
	}
	if p != nil {
		l.installCollectionExecutionMemory(p)
	}
	return result, nil
}

func planCollectionExecutionRetirement(v *collectionLedgerView, checkpoint CollectionExecutionRetirementCheckpoint, final CollectionExecutionProgress, preparedRemoved bool) (*collectionExecutionBatch, collectionExecutionRetirementBatch, error) {
	fail := func(err error) (*collectionExecutionBatch, collectionExecutionRetirementBatch, error) {
		return nil, collectionExecutionRetirementBatch{}, err
	}
	expected, err := collectionExecutionRetirementStats(checkpoint, final, preparedRemoved)
	if err != nil {
		return fail(err)
	}
	op := final.Binding.OperationID
	actual, err := v.executionStat(op)
	if err != nil {
		return fail(err)
	}
	if actual != expected {
		return fail(errCollectionLedgerCorrupt)
	}
	used, charged, err := v.executionTotals()
	if err != nil {
		return fail(err)
	}
	result := collectionExecutionRetirementBatch{Checkpoint: checkpoint.Clone(), PreparedRemoved: preparedRemoved}
	updates := make(map[string][]byte)
	for result.Checkpoint.Progress.Processed < final.Processed && result.Records < collectionLedgerBatchLimit {
		ordinal := result.Checkpoint.Progress.Processed + 1
		slot := collectionExecutionOutcomeSlot(ordinal)
		raw, err := v.encodedExecution(op, slot)
		if err != nil || raw == nil {
			return fail(errCollectionLedgerCorrupt)
		}
		if int64(len(raw)) > collectionLedgerBatchBytes-result.EncodedBytes {
			break
		}
		record, err := decodeExecutionAt(op, slot, raw)
		if err != nil || record.Outcome == nil {
			return fail(errCollectionLedgerCorrupt)
		}
		var terminal *CollectionChildObservation
		var terminalRaw []byte
		terminalSlot := collectionExecutionTerminalSlot(ordinal)
		if record.Outcome.Decision == "accepted" && result.Records+2 > collectionLedgerBatchLimit {
			break
		}
		terminalRaw, err = v.encodedExecution(op, terminalSlot)
		if err != nil {
			return fail(err)
		}
		if record.Outcome.Decision == "accepted" {
			if terminalRaw == nil {
				return fail(errCollectionLedgerCorrupt)
			}
			if int64(len(raw)+len(terminalRaw)) > collectionLedgerBatchBytes-result.EncodedBytes {
				break
			}
			r, err := decodeExecutionAt(op, terminalSlot, terminalRaw)
			if err != nil || r.Terminal == nil || !r.Terminal.matches(*record.Outcome) {
				return fail(errCollectionLedgerCorrupt)
			}
			terminal = r.Terminal
		} else if terminalRaw != nil {
			return fail(errCollectionLedgerCorrupt)
		}
		next, err := result.Checkpoint.append(*record.Outcome, terminal)
		if err != nil {
			return fail(errCollectionLedgerCorrupt)
		}
		result.Checkpoint = next
		updates[slot] = nil
		result.Records++
		if terminal != nil {
			updates[terminalSlot] = nil
			result.Records++
		}
		result.EncodedBytes += int64(len(raw) + len(terminalRaw))
		result.ChargedBytes += collectionExecutionCharge(record, raw)
	}
	if result.Checkpoint.Progress.Processed == final.Processed {
		if !result.Checkpoint.matchesFinal(final) {
			return fail(errCollectionLedgerCorrupt)
		}
		if final.Prepared != nil && !result.PreparedRemoved && result.Records < collectionLedgerBatchLimit {
			raw, err := v.encodedExecution(op, "prepared")
			if err != nil || raw == nil {
				return fail(errCollectionLedgerCorrupt)
			}
			if int64(len(raw)) <= collectionLedgerBatchBytes-result.EncodedBytes {
				r, err := decodeExecutionAt(op, "prepared", raw)
				if err != nil || r.Prepared == nil || r.Prepared.Binding != final.Binding {
					return fail(errCollectionLedgerCorrupt)
				}
				commitment, err := collectionExecutionPreparedCommitment(*r.Prepared)
				if err != nil || !commitment.At.Equal(final.Prepared.At) {
					return fail(errCollectionLedgerCorrupt)
				}
				commitment.At = final.Prepared.At
				if commitment != *final.Prepared {
					return fail(errCollectionLedgerCorrupt)
				}
				updates["prepared"] = nil
				result.PreparedRemoved = true
				result.Records++
				result.EncodedBytes += int64(len(raw))
				result.ChargedBytes += int64(len(raw))
			}
		}
	}
	next, err := collectionExecutionRetirementStats(result.Checkpoint, final, result.PreparedRemoved)
	if err != nil || actual.EncodedBytes-result.EncodedBytes != next.EncodedBytes || actual.ChargedBytes-result.ChargedBytes != next.ChargedBytes ||
		result.ChargedBytes > charged || result.ChargedBytes > used {
		return fail(errCollectionLedgerCorrupt)
	}
	result.Complete = next == (collectionExecutionStats{})
	if result.Records == 0 {
		if !result.Complete {
			return fail(errCollectionLedgerQuota)
		}
		return nil, result, nil
	}
	if result.Complete {
		if err := v.checkExecutionRetirementEmpty(op, updates); err != nil {
			return fail(err)
		}
	}
	p := &collectionExecutionBatch{rows: map[string]map[string][]byte{op: updates}, stats: map[string]collectionExecutionStats{op: next},
		used: used - result.ChargedBytes, charged: charged - result.ChargedBytes}
	return p, result, nil
}

// Before deleting the namespace, ensure every physical row is in this bounded
// deletion set. In particular, a forged native bucket sequence cannot conceal
// an extra key that would otherwise be silently discarded with the bucket.
func (v *collectionLedgerView) checkExecutionRetirementEmpty(op string, updates map[string][]byte) error {
	seen := 0
	check := func(slot string, raw []byte) error {
		if _, exists := updates[slot]; !exists || raw == nil || seen >= collectionLedgerBatchLimit {
			return errCollectionLedgerCorrupt
		}
		seen++
		return nil
	}
	if v.tx == nil {
		for slot, raw := range v.executionRows[op] {
			if err := check(slot, raw); err != nil {
				return err
			}
		}
	} else {
		bucket := v.tx.Bucket(collectionLedgerExecution).Bucket([]byte(op))
		if bucket == nil {
			return errCollectionLedgerCorrupt
		}
		if err := bucket.ForEach(func(slot, raw []byte) error { return check(string(slot), raw) }); err != nil {
			return err
		}
	}
	if seen != len(updates) {
		return errCollectionLedgerCorrupt
	}
	return nil
}
