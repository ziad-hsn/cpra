package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"slices"
)

func (v *collectionLedgerView) ValidationStats(operationID string) (uint64, int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, 0, errCollectionLedgerClosed
	}
	return v.validationStats(operationID)
}

func (v *collectionLedgerView) validationStats(operationID string) (uint64, int64, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil {
		return 0, 0, errCollectionLedgerCorrupt
	}
	if v.tx == nil {
		rows, rowExists := v.validationRows[operationID]
		size, sizeExists := v.validationOperationBytes[operationID]
		if rowExists != sizeExists || rowExists && (len(rows) == 0 || size <= 0) ||
			len(rows) > maxCollectionItems || size > v.validationBytes || v.validationBytes < 0 || v.validationBytes > v.bytes {
			return 0, 0, errCollectionLedgerCorrupt
		}
		return uint64(len(rows)), size, nil
	}
	records, meta := v.tx.Bucket(collectionLedgerValidation), v.tx.Bucket(collectionLedgerValidationMeta)
	if records == nil || meta == nil {
		return 0, 0, errCollectionLedgerCorrupt
	}
	total, err := collectionLedgerValidationBytes(v.tx.Bucket(collectionLedgerMeta))
	if err != nil {
		return 0, 0, err
	}
	used, err := collectionLedgerBytes(v.tx.Bucket(collectionLedgerMeta))
	if err != nil || total > used {
		return 0, 0, errCollectionLedgerCorrupt
	}
	rows, size := records.Bucket([]byte(operationID)), meta.Get([]byte(operationID))
	if rows == nil && size == nil {
		return 0, 0, nil
	}
	if rows == nil || len(size) != 8 || rows.Sequence() == 0 || rows.Sequence() > maxCollectionItems {
		return 0, 0, errCollectionLedgerCorrupt
	}
	value := binary.BigEndian.Uint64(size)
	if value == 0 || value > uint64(total) {
		return 0, 0, errCollectionLedgerCorrupt
	}
	return rows.Sequence(), int64(value), nil
}

// encodedValidationItem borrows bytes only for the enclosing locked view/transaction.
// It bounds corrupt lengths before any clone or JSON decode. Callers needing an
// owned result must clone inside that scope and decode after releasing locks.
func (v *collectionLedgerView) encodedValidationItem(operationID string, ordinal uint64) ([]byte, error) {
	var raw []byte
	if v.tx == nil {
		raw = v.validationRows[operationID][ordinal]
	} else {
		bucket := v.tx.Bucket(collectionLedgerValidation)
		if bucket == nil {
			return nil, errCollectionLedgerCorrupt
		}
		if rows := bucket.Bucket([]byte(operationID)); rows != nil {
			raw = rows.Get(collectionOrdinal(ordinal))
		}
	}
	if len(raw) > collectionValidationLedgerMaxFrame {
		return nil, errCollectionLedgerCorrupt
	}
	return raw, nil
}

// ValidationPage detaches at most 500 items / 4 MiB. A missing in-range item is
// corruption, never an empty successful page. Continue from the last ordinal
// returned; the byte limit may yield fewer items than the requested count.
func (v *collectionLedgerView) ValidationPage(operationID string, after uint64, limit int) ([]CollectionValidationItem, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errCollectionLedgerClosed
	}
	if limit < 1 || limit > collectionLedgerPageLimit {
		return nil, errCollectionLedgerQuota
	}
	count, _, err := v.validationStats(operationID)
	if err != nil {
		return nil, err
	}
	page := make([]CollectionValidationItem, 0, limit)
	var pageBytes int
	for ordinal := after; ordinal < count && len(page) < limit; {
		ordinal++
		raw, err := v.encodedValidationItem(operationID, ordinal)
		if err != nil || len(raw) == 0 {
			return nil, errCollectionLedgerCorrupt
		}
		if len(raw) > collectionLedgerPageBytes-pageBytes {
			break
		}
		row, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil || row.OperationID != operationID || row.Item.Ordinal != ordinal {
			return nil, errCollectionLedgerCorrupt
		}
		page = append(page, row.Item)
		pageBytes += len(raw)
	}
	return page, nil
}

// WalkValidation streams detached canonical records in operation/input order.
// Callbacks must not reenter this view; cancellation is checked before/after
// every callback. Only per-operation counts and one decoded item are retained.
func (v *collectionLedgerView) WalkValidation(ctx context.Context, visit func(string, CollectionValidationItem) error) error {
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
	return v.walkValidation(ctx, func(raw []byte, row collectionValidationLedgerRow) error { return visit(row.OperationID, row.Item) })
}

func (v *collectionLedgerView) walkValidation(ctx context.Context, visit func([]byte, collectionValidationLedgerRow) error) error {
	var observed int64
	check := func(op string, ordinal uint64, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > collectionValidationLedgerMaxFrame || int64(len(raw)) > v.validationBytes-observed {
			return errCollectionLedgerCorrupt
		}
		row, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil || row.OperationID != op || row.Item.Ordinal != ordinal {
			return errCollectionLedgerCorrupt
		}
		observed += int64(len(raw))
		if err := visit(raw, row); err != nil {
			return err
		}
		return ctx.Err()
	}
	if v.validationBytes < 0 || v.validationBytes > v.bytes {
		return errCollectionLedgerCorrupt
	}
	if v.tx == nil {
		if len(v.validationRows) != len(v.validationOperationBytes) {
			return errCollectionLedgerCorrupt
		}
		operations := make([]string, 0, len(v.validationRows))
		for op := range v.validationRows {
			operations = append(operations, op)
		}
		slices.Sort(operations)
		for _, op := range operations {
			count, size, err := v.validationStats(op)
			if err != nil {
				return err
			}
			before := observed
			for ordinal := uint64(1); ordinal <= count; ordinal++ {
				if err := check(op, ordinal, v.validationRows[op][ordinal]); err != nil {
					return err
				}
			}
			if observed-before != size {
				return errCollectionLedgerCorrupt
			}
		}
	} else {
		records, stats := v.tx.Bucket(collectionLedgerValidation), v.tx.Bucket(collectionLedgerValidationMeta)
		if records == nil || stats == nil {
			return errCollectionLedgerCorrupt
		}
		err := records.ForEach(func(op, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if value != nil {
				return errCollectionLedgerCorrupt
			}
			count, size, err := v.validationStats(string(op))
			if err != nil {
				return err
			}
			before, ordinal := observed, uint64(0)
			err = records.Bucket(op).ForEach(func(key, raw []byte) error {
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
			if records.Bucket(op) == nil || len(value) != 8 {
				return errCollectionLedgerCorrupt
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if observed != v.validationBytes {
		return errCollectionLedgerCorrupt
	}
	return nil
}
