package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/btree"
)

func (v *collectionLedgerView) executionTotals() (int64, int64, error) {
	if v.tx == nil {
		if v.executionBytes < 0 || v.executionBytes > v.bytes || v.bytes > maxCollectionLedgerBytes {
			return 0, 0, errCollectionLedgerCorrupt
		}
		return v.bytes, v.executionBytes, nil
	}
	meta := v.tx.Bucket(collectionLedgerMeta)
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return 0, 0, err
	}
	charged, err := collectionLedgerExecutionBytes(meta)
	if err != nil || charged > used {
		return 0, 0, errCollectionLedgerCorrupt
	}
	return used, charged, nil
}

func (v *collectionLedgerView) executionStat(op string) (collectionExecutionStats, error) {
	if _, _, err := ParseOperationHandle(op); err != nil {
		return collectionExecutionStats{}, errCollectionLedgerCorrupt
	}
	_, total, err := v.executionTotals()
	if err != nil {
		return collectionExecutionStats{}, err
	}
	var s collectionExecutionStats
	var count uint64
	var hasRows, hasStats bool
	if v.tx == nil {
		rows, exists := v.executionRows[op]
		hasRows = exists
		count = uint64(len(rows))
		s, hasStats = v.executionStats[op]
	} else {
		rows, meta := v.tx.Bucket(collectionLedgerExecution), v.tx.Bucket(collectionLedgerExecutionMeta)
		if rows == nil || meta == nil {
			return s, errCollectionLedgerCorrupt
		}
		b := rows.Bucket([]byte(op))
		raw := meta.Get([]byte(op))
		hasRows = b != nil
		hasStats = raw != nil
		if b != nil {
			count = b.Sequence()
		}
		if raw != nil {
			if len(raw) > 4096 || json.Unmarshal(raw, &s) != nil {
				return s, errCollectionLedgerCorrupt
			}
			canonical, _ := json.Marshal(s)
			if !bytes.Equal(raw, canonical) {
				return s, errCollectionLedgerCorrupt
			}
		}
	}
	if !hasRows && !hasStats {
		return collectionExecutionStats{}, nil
	}
	if !hasRows || !hasStats || s == (collectionExecutionStats{}) || s.validate(op) != nil || s.ChargedBytes > total {
		return s, errCollectionLedgerCorrupt
	}
	expected := s.Outcomes + s.Terminals
	if s.Prepared {
		expected++
	}
	if v.tx == nil && (v.executionOrder[op] == nil || uint64(v.executionOrder[op].Len()) != count) {
		return s, errCollectionLedgerCorrupt
	}
	if count != expected || count == 0 {
		return s, errCollectionLedgerCorrupt
	}
	prepared, err := v.encodedExecution(op, "prepared")
	if err != nil || s.Prepared != (prepared != nil) {
		return s, errCollectionLedgerCorrupt
	}
	if s.Outcomes > 0 {
		raw, err := v.encodedExecution(op, collectionExecutionOutcomeSlot(s.RetiredOutcomes+s.Outcomes))
		if err != nil || raw == nil {
			return s, errCollectionLedgerCorrupt
		}
	}
	return s, nil
}

// encodedExecution borrows bounded bytes from the enclosing view lock only.
func (v *collectionLedgerView) encodedExecution(op, slot string) ([]byte, error) {
	var raw []byte
	if v.tx == nil {
		raw = v.executionRows[op][slot]
	} else {
		rows := v.tx.Bucket(collectionLedgerExecution)
		if rows == nil {
			return nil, errCollectionLedgerCorrupt
		}
		if b := rows.Bucket([]byte(op)); b != nil {
			raw = b.Get([]byte(slot))
		}
	}
	limit := collectionExecutionMaxFrame
	if strings.HasPrefix(slot, "terminal/") {
		limit = collectionChildTerminalReserve
	}
	if len(raw) > limit {
		return nil, errCollectionLedgerCorrupt
	}
	return raw, nil
}

func decodeExecutionAt(op, slot string, raw []byte) (collectionExecutionRecord, error) {
	r, err := decodeCollectionExecutionRecord(raw)
	if err != nil {
		return collectionExecutionRecord{}, err
	}
	parent, key, err := r.identity()
	if err != nil || parent != op || key != slot {
		return collectionExecutionRecord{}, errCollectionLedgerCorrupt
	}
	return r, nil
}

func (l *collectionLedger) ExecutionStats(op string) (collectionExecutionStats, error) {
	var result collectionExecutionStats
	err := l.read(func(v *collectionLedgerView) error { var err error; result, err = v.ExecutionStats(op); return err })
	return result, err
}
func (v *collectionLedgerView) ExecutionStats(op string) (collectionExecutionStats, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return collectionExecutionStats{}, errCollectionLedgerClosed
	}
	return v.executionStat(op)
}

func (l *collectionLedger) ExecutionRecord(op, slot string) (collectionExecutionRecord, bool, error) {
	var r collectionExecutionRecord
	var found bool
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		r, found, err = v.ExecutionRecord(op, slot)
		return err
	})
	return r, found, err
}
func (v *collectionLedgerView) ExecutionRecord(op, slot string) (collectionExecutionRecord, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return collectionExecutionRecord{}, false, errCollectionLedgerClosed
	}
	stat, err := v.executionStat(op)
	if err != nil {
		return collectionExecutionRecord{}, false, err
	}
	if !collectionExecutionSlotValid(slot) {
		return collectionExecutionRecord{}, false, errCollectionLedgerCorrupt
	}
	raw, err := v.encodedExecution(op, slot)
	if err != nil {
		return collectionExecutionRecord{}, false, err
	}
	if raw == nil {
		if strings.HasPrefix(slot, "outcome/") && collectionExecutionSlotOrdinal(slot) > stat.RetiredOutcomes && collectionExecutionSlotOrdinal(slot) <= stat.RetiredOutcomes+stat.Outcomes {
			return collectionExecutionRecord{}, false, errCollectionLedgerCorrupt
		}
		return collectionExecutionRecord{}, false, nil
	}
	r, err := decodeExecutionAt(op, slot, raw)
	if err != nil {
		return collectionExecutionRecord{}, false, err
	}
	if err := v.checkExecutionRecord(stat, r); err != nil {
		return collectionExecutionRecord{}, false, err
	}
	return r, true, nil
}

// ExecutionPage uses the last returned record's identity slot as its cursor.
// Sparse terminal ordinals are ordered lexically after outcomes/preparation.
// At most500 records /4MiB are decoded; raw size is checked before decoding.
func (l *collectionLedger) ExecutionPage(op, after string, limit int) ([]collectionExecutionRecord, error) {
	var page []collectionExecutionRecord
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		page, err = v.ExecutionPage(op, after, limit)
		return err
	})
	return page, err
}
func (v *collectionLedgerView) ExecutionPage(op, after string, limit int) ([]collectionExecutionRecord, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errCollectionLedgerClosed
	}
	if limit < 1 || limit > collectionLedgerPageLimit {
		return nil, errCollectionLedgerQuota
	}
	stat, err := v.executionStat(op)
	if err != nil {
		return nil, err
	}
	if after != "" && !collectionExecutionSlotValid(after) {
		return nil, errCollectionLedgerCorrupt
	}
	page := make([]collectionExecutionRecord, 0, limit)
	used := 0
	lastOutcome := stat.RetiredOutcomes + stat.Outcomes
	nextOutcome := stat.RetiredOutcomes + 1
	if strings.HasPrefix(after, "outcome/") {
		nextOutcome = max(nextOutcome, collectionExecutionSlotOrdinal(after)+1)
	} else if after != "" {
		nextOutcome = lastOutcome + 1
	}
	exhausted := true
	visit := func(slot string, raw []byte) (bool, error) {
		if len(page) == limit || len(raw) > collectionLedgerPageBytes-used {
			return false, nil
		}
		if strings.HasPrefix(slot, "outcome/") {
			if collectionExecutionSlotOrdinal(slot) != nextOutcome {
				return false, errCollectionLedgerCorrupt
			}
			nextOutcome++
		} else if nextOutcome <= lastOutcome {
			return false, errCollectionLedgerCorrupt
		}
		r, err := decodeExecutionAt(op, slot, raw)
		if err != nil {
			return false, err
		}
		if err := v.checkExecutionRecord(stat, r); err != nil {
			return false, err
		}
		page = append(page, r)
		used += len(raw)
		return true, nil
	}
	if v.tx == nil {
		var walkErr error
		if order := v.executionOrder[op]; order != nil {
			order.AscendGreaterOrEqual(after, func(slot string) bool {
				if slot == after {
					return true
				}
				raw, err := v.encodedExecution(op, slot)
				if err != nil {
					walkErr = err
					return false
				}
				more, err := visit(slot, raw)
				if err != nil {
					walkErr = err
					return false
				}
				if !more {
					exhausted = false
				}
				return more
			})
		}
		if walkErr != nil {
			return nil, walkErr
		}
	} else if b := v.tx.Bucket(collectionLedgerExecution).Bucket([]byte(op)); b != nil {
		c := b.Cursor()
		k, raw := c.Seek([]byte(after))
		if string(k) == after {
			k, raw = c.Next()
		}
		for ; k != nil; k, raw = c.Next() {
			bounded, err := v.encodedExecution(op, string(k))
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(bounded, raw) {
				return nil, errCollectionLedgerCorrupt
			}
			more, err := visit(string(k), raw)
			if err != nil {
				return nil, err
			}
			if !more {
				exhausted = false
				break
			}
		}
	}
	if exhausted && nextOutcome <= lastOutcome {
		return nil, errCollectionLedgerCorrupt
	}
	return page, nil
}

func (v *collectionLedgerView) freezeExecution(l *collectionLedger) {
	v.executionRows = make(map[string]map[string][]byte, len(l.executionRows))
	v.executionStats = make(map[string]collectionExecutionStats, len(l.executionStats))
	v.executionOrder = make(map[string]*btree.BTreeG[string], len(l.executionOrder))
	for op, rows := range l.executionRows {
		copy := make(map[string][]byte, len(rows))
		for slot, raw := range rows {
			copy[slot] = raw
		}
		v.executionRows[op] = copy
	}
	for op, s := range l.executionStats {
		v.executionStats[op] = s
		if order := l.executionOrder[op]; order != nil {
			v.executionOrder[op] = order.Clone()
		}
	}
}

// WalkExecution validates every record, sparse terminal linkage and charged/raw
// accounting in parent/slot order. Callbacks cannot reenter this frozen view.
func (v *collectionLedgerView) WalkExecution(ctx context.Context, visit func(collectionExecutionRecord) error) error {
	if ctx == nil || visit == nil {
		return errCollectionLedgerCorrupt
	}
	if err := collectionExecutionViewLock(ctx, &v.mu); err != nil {
		return err
	}
	defer v.mu.Unlock()
	if v.closed {
		return errCollectionLedgerClosed
	}
	_, total, err := v.executionTotals()
	if err != nil {
		return err
	}
	var observed int64
	walkOp := func(op string, walk func(func(string, []byte) error) error) error {
		expected, err := v.executionStat(op)
		if err != nil {
			return err
		}
		actual := collectionExecutionStats{Binding: expected.Binding, RetiredOutcomes: expected.RetiredOutcomes}
		err = walk(func(slot string, raw []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			bounded, err := v.encodedExecution(op, slot)
			if err != nil || !bytes.Equal(bounded, raw) {
				return errCollectionLedgerCorrupt
			}
			r, err := decodeExecutionAt(op, slot, raw)
			if err != nil {
				return err
			}
			if collectionExecutionBinding(r) != expected.Binding {
				return errCollectionLedgerCorrupt
			}
			switch {
			case r.Prepared != nil:
				if actual.Prepared || r.Prepared.Ordinal != expected.RetiredOutcomes+expected.Outcomes+1 {
					return errCollectionLedgerCorrupt
				}
				actual.Prepared = true
			case r.Outcome != nil:
				if r.Outcome.Ordinal != actual.RetiredOutcomes+actual.Outcomes+1 {
					return errCollectionLedgerCorrupt
				}
				actual.Outcomes++
				if r.Outcome.Receipt != nil {
					actual.TerminalCapacity += collectionChildTerminalReserve
				}
			case r.Terminal != nil:
				raw, err := v.encodedExecution(op, collectionExecutionOutcomeSlot(r.Terminal.Ordinal))
				if err != nil {
					return err
				}
				outcome, err := decodeCollectionExecutionRecord(raw)
				if err != nil || outcome.Outcome == nil || !r.Terminal.matches(*outcome.Outcome) {
					return errCollectionLedgerCorrupt
				}
				actual.Terminals++
				actual.TerminalBytes += int64(len(bounded))
			}
			actual.EncodedBytes += int64(len(bounded))
			actual.ChargedBytes += collectionExecutionCharge(r, bounded)
			if err := visit(r); err != nil {
				return err
			}
			return ctx.Err()
		})
		if err != nil {
			return err
		}
		if actual != expected {
			return errCollectionLedgerCorrupt
		}
		observed += actual.ChargedBytes
		if observed > total {
			return errCollectionLedgerCorrupt
		}
		return nil
	}
	if v.tx == nil {
		if len(v.executionRows) != len(v.executionStats) {
			return errCollectionLedgerCorrupt
		}
		ops := make([]string, 0, len(v.executionRows))
		for op := range v.executionRows {
			ops = append(ops, op)
		}
		slices.Sort(ops)
		for _, op := range ops {
			if err := walkOp(op, func(visit func(string, []byte) error) error {
				var walkErr error
				v.executionOrder[op].Ascend(func(slot string) bool {
					walkErr = visit(slot, v.executionRows[op][slot])
					return walkErr == nil
				})
				return walkErr
			}); err != nil {
				return err
			}
		}
	} else {
		rows, meta := v.tx.Bucket(collectionLedgerExecution), v.tx.Bucket(collectionLedgerExecutionMeta)
		if rows == nil || meta == nil {
			return errCollectionLedgerCorrupt
		}
		if err := rows.ForEach(func(op, raw []byte) error {
			if raw != nil {
				return errCollectionLedgerCorrupt
			}
			return walkOp(string(op), func(visit func(string, []byte) error) error {
				return rows.Bucket(op).ForEach(func(slot, value []byte) error { return visit(string(slot), value) })
			})
		}); err != nil {
			return err
		}
		if err := meta.ForEach(func(op, raw []byte) error {
			if raw == nil || rows.Bucket(op) == nil {
				return errCollectionLedgerCorrupt
			}
			return ctx.Err()
		}); err != nil {
			return err
		}
	}
	if observed != total {
		return errCollectionLedgerCorrupt
	}
	return ctx.Err()
}

func collectionExecutionSlotValid(slot string) bool {
	if slot == "prepared" {
		return true
	}
	prefix := "outcome/"
	if strings.HasPrefix(slot, "terminal/") {
		prefix = "terminal/"
	}
	if !strings.HasPrefix(slot, prefix) || len(slot) != len(prefix)+16 {
		return false
	}
	ordinal, err := strconv.ParseUint(strings.TrimPrefix(slot, prefix), 16, 64)
	return err == nil && ordinal > 0 && ordinal <= maxCollectionItems && slot == fmt.Sprintf("%s%016x", prefix, ordinal)
}

func (v *collectionLedgerView) checkExecutionRecord(stat collectionExecutionStats, r collectionExecutionRecord) error {
	if collectionExecutionBinding(r) != stat.Binding {
		return errCollectionLedgerCorrupt
	}
	switch {
	case r.Prepared != nil:
		if !stat.Prepared || r.Prepared.Ordinal != stat.RetiredOutcomes+stat.Outcomes+1 {
			return errCollectionLedgerCorrupt
		}
	case r.Outcome != nil:
		if r.Outcome.Ordinal <= stat.RetiredOutcomes || r.Outcome.Ordinal > stat.RetiredOutcomes+stat.Outcomes {
			return errCollectionLedgerCorrupt
		}
	case r.Terminal != nil:
		if r.Terminal.Ordinal <= stat.RetiredOutcomes || r.Terminal.Ordinal > stat.RetiredOutcomes+stat.Outcomes {
			return errCollectionLedgerCorrupt
		}
		raw, err := v.encodedExecution(stat.Binding.OperationID, collectionExecutionOutcomeSlot(r.Terminal.Ordinal))
		if err != nil {
			return err
		}
		accepted, err := decodeCollectionExecutionRecord(raw)
		if err != nil || accepted.Outcome == nil || !r.Terminal.matches(*accepted.Outcome) {
			return errCollectionLedgerCorrupt
		}
	}
	return nil
}

func collectionExecutionSlotOrdinal(slot string) uint64 {
	_, hex, _ := strings.Cut(slot, "/")
	ordinal, _ := strconv.ParseUint(hex, 16, 64)
	return ordinal
}
