package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"slices"
)

func (v *collectionLedgerView) PlanStats(operationID string) (uint64, int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, 0, errCollectionLedgerClosed
	}
	return v.planStats(operationID)
}

func (v *collectionLedgerView) planStats(operationID string) (uint64, int64, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil {
		return 0, 0, errCollectionLedgerCorrupt
	}
	if v.tx == nil {
		rows, rowExists := v.planRows[operationID]
		size, sizeExists := v.planOperationBytes[operationID]
		if rowExists != sizeExists || rowExists && (len(rows) == 0 || size <= 0) ||
			len(rows) > collectionPlanLedgerMaxParts || size > v.planBytes || v.planBytes < 0 || v.planBytes > v.bytes {
			return 0, 0, errCollectionLedgerCorrupt
		}
		return uint64(len(rows)), size, nil
	}
	plans, meta := v.tx.Bucket(collectionLedgerPlans), v.tx.Bucket(collectionLedgerPlanMeta)
	if plans == nil || meta == nil {
		return 0, 0, errCollectionLedgerCorrupt
	}
	total, err := collectionLedgerPlanBytes(v.tx.Bucket(collectionLedgerMeta))
	if err != nil {
		return 0, 0, err
	}
	used, err := collectionLedgerBytes(v.tx.Bucket(collectionLedgerMeta))
	if err != nil || total > used {
		return 0, 0, errCollectionLedgerCorrupt
	}
	rows, size := plans.Bucket([]byte(operationID)), meta.Get([]byte(operationID))
	if rows == nil && size == nil {
		return 0, 0, nil
	}
	if rows == nil || len(size) != 8 || rows.Sequence() == 0 || rows.Sequence() > collectionPlanLedgerMaxParts {
		return 0, 0, errCollectionLedgerCorrupt
	}
	value := binary.BigEndian.Uint64(size)
	if value == 0 || value > uint64(total) {
		return 0, 0, errCollectionLedgerCorrupt
	}
	return rows.Sequence(), int64(value), nil
}

// encodedPlanPart borrows bytes only for the enclosing locked view/transaction.
// It bounds corrupt lengths before any clone or JSON decode. Callers needing an
// owned result must clone inside that scope and decode after releasing locks.
func (v *collectionLedgerView) encodedPlanPart(operationID string, ordinal uint64) ([]byte, error) {
	var raw []byte
	if v.tx == nil {
		raw = v.planRows[operationID][ordinal]
	} else {
		bucket := v.tx.Bucket(collectionLedgerPlans)
		if bucket == nil {
			return nil, errCollectionLedgerCorrupt
		}
		if rows := bucket.Bucket([]byte(operationID)); rows != nil {
			raw = rows.Get(collectionOrdinal(ordinal))
		}
	}
	if len(raw) > collectionPlanLedgerMaxFrame {
		return nil, errCollectionLedgerCorrupt
	}
	return raw, nil
}

// PlanPage detaches at most 256 parts / 4 MiB. A missing in-range part is
// corruption, never an empty successful page. Continue from the last ordinal
// returned; the byte limit may yield fewer parts than the requested count.
func (v *collectionLedgerView) PlanPage(operationID string, after uint64, limit int) ([]CollectionPlanLedgerFragment, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errCollectionLedgerClosed
	}
	if limit < 1 || limit > collectionLedgerBatchLimit {
		return nil, errCollectionLedgerQuota
	}
	count, _, err := v.planStats(operationID)
	if err != nil {
		return nil, err
	}
	page := make([]CollectionPlanLedgerFragment, 0, limit)
	var pageBytes int
	for ordinal := after; ordinal < count && len(page) < limit; {
		ordinal++
		raw, err := v.encodedPlanPart(operationID, ordinal)
		if err != nil || len(raw) == 0 {
			return nil, errCollectionLedgerCorrupt
		}
		if len(raw) > collectionLedgerPageBytes-pageBytes {
			break
		}
		row, err := decodeCollectionPlanLedgerRow(raw)
		if err != nil || row.OperationID != operationID || row.Part.Ordinal != ordinal {
			return nil, errCollectionLedgerCorrupt
		}
		page = append(page, row.Part)
		pageBytes += len(raw)
	}
	return page, nil
}

// WalkPlans streams detached canonical records in operation/fragment order.
// Callbacks must not reenter this view; cancellation is checked before/after
// every callback. Only per-operation counts and one decoded part are retained.
func (v *collectionLedgerView) WalkPlans(ctx context.Context, visit func(string, CollectionPlanLedgerFragment) error) error {
	if ctx == nil || visit == nil {
		return errCollectionLedgerCorrupt
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errCollectionLedgerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.walkPlans(ctx, func(raw []byte, row collectionPlanLedgerRow) error { return visit(row.OperationID, row.Part) })
}

func (v *collectionLedgerView) walkPlans(ctx context.Context, visit func([]byte, collectionPlanLedgerRow) error) error {
	var observed int64
	check := func(op string, ordinal uint64, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > collectionPlanLedgerMaxFrame || int64(len(raw)) > v.planBytes-observed {
			return errCollectionLedgerCorrupt
		}
		row, err := decodeCollectionPlanLedgerRow(raw)
		if err != nil || row.OperationID != op || row.Part.Ordinal != ordinal {
			return errCollectionLedgerCorrupt
		}
		observed += int64(len(raw))
		if err := visit(raw, row); err != nil {
			return err
		}
		return ctx.Err()
	}
	if v.planBytes < 0 || v.planBytes > v.bytes {
		return errCollectionLedgerCorrupt
	}
	if v.tx == nil {
		if len(v.planRows) != len(v.planOperationBytes) {
			return errCollectionLedgerCorrupt
		}
		operations := make([]string, 0, len(v.planRows))
		for op := range v.planRows {
			operations = append(operations, op)
		}
		slices.Sort(operations)
		for _, op := range operations {
			count, size, err := v.planStats(op)
			if err != nil {
				return err
			}
			before := observed
			for ordinal := uint64(1); ordinal <= count; ordinal++ {
				if err := check(op, ordinal, v.planRows[op][ordinal]); err != nil {
					return err
				}
			}
			if observed-before != size {
				return errCollectionLedgerCorrupt
			}
		}
	} else {
		plans, stats := v.tx.Bucket(collectionLedgerPlans), v.tx.Bucket(collectionLedgerPlanMeta)
		if plans == nil || stats == nil {
			return errCollectionLedgerCorrupt
		}
		err := plans.ForEach(func(op, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if value != nil {
				return errCollectionLedgerCorrupt
			}
			count, size, err := v.planStats(string(op))
			if err != nil {
				return err
			}
			before, ordinal := observed, uint64(0)
			err = plans.Bucket(op).ForEach(func(key, raw []byte) error {
				if ordinal == math.MaxUint64 {
					return errCollectionLedgerCorrupt
				}
				ordinal++
				if !bytes.Equal(key, collectionOrdinal(ordinal)) {
					return errCollectionLedgerCorrupt
				}
				return check(string(op), ordinal, raw)
			})
			if err != nil {
				return err
			}
			if ordinal != count || observed-before != size {
				return errCollectionLedgerCorrupt
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := stats.ForEach(func(op, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if plans.Bucket(op) == nil || len(value) != 8 {
				return errCollectionLedgerCorrupt
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if observed != v.planBytes {
		return errCollectionLedgerCorrupt
	}
	return nil
}
