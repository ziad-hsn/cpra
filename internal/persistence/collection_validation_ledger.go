package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"

	bolt "go.etcd.io/bbolt"
)

const collectionValidationLedgerMaxFrame = CollectionValidationItemMaxBytes + 1024

var (
	collectionLedgerValidation     = []byte("validation_rows")
	collectionLedgerValidationMeta = []byte("validation_metadata")
)

type collectionValidationLedgerRow struct {
	OperationID string                   `json:"operation_id"`
	Item        CollectionValidationItem `json:"item"`
}

// Canonical wrappers bind an allowlisted result item to its original operation.
// Source/descriptor/phase fences remain authoritative FSM responsibilities.
func collectionValidationLedgerEncoding(operationID string, item CollectionValidationItem) ([]byte, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if _, err := CollectionValidationItemEncoding(item); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(collectionValidationLedgerRow{OperationID: operationID, Item: item})
	if err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if len(raw) > collectionValidationLedgerMaxFrame {
		return nil, errCollectionLedgerQuota
	}
	return raw, nil
}

func collectionValidationLedgerCost(operationID string, item CollectionValidationItem) (int64, error) {
	raw, err := collectionValidationLedgerEncoding(operationID, item)
	return int64(len(raw)), err
}

func decodeCollectionValidationLedgerRow(raw []byte) (collectionValidationLedgerRow, error) {
	var row collectionValidationLedgerRow
	if len(raw) == 0 || len(raw) > collectionValidationLedgerMaxFrame || collectionPlanJSON(context.Background(), raw) != nil {
		return row, errCollectionLedgerCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&row) != nil {
		return collectionValidationLedgerRow{}, errCollectionLedgerCorrupt
	}
	canonical, err := collectionValidationLedgerEncoding(row.OperationID, row.Item)
	if err != nil || !bytes.Equal(raw, canonical) {
		return collectionValidationLedgerRow{}, errCollectionLedgerCorrupt
	}
	return row, nil
}

func collectionLedgerValidationBytes(meta *bolt.Bucket) (int64, error) {
	if meta == nil {
		return 0, errCollectionLedgerCorrupt
	}
	raw := meta.Get([]byte("validation_bytes"))
	if len(raw) != 8 || binary.BigEndian.Uint64(raw) > math.MaxInt64 {
		return 0, errCollectionLedgerCorrupt
	}
	return int64(binary.BigEndian.Uint64(raw)), nil
}

// AppendValidationItems commits at most 256 items / 4 MiB of canonical wrappers
// atomically. Exact retries consume no extra quota. All namespaces share one
// quota; callers fence phase, source identity and descriptor in the FSM.
func (l *collectionLedger) AppendValidationItems(operationID string, items []CollectionValidationItem) error {
	if len(items) == 0 || len(items) > collectionLedgerBatchLimit {
		return errCollectionLedgerQuota
	}
	encoded := make([][]byte, len(items))
	var batchBytes int64
	for i, item := range items {
		var err error
		encoded[i], err = collectionValidationLedgerEncoding(operationID, item)
		if err != nil {
			return err
		}
		if int64(len(encoded[i])) > collectionLedgerBatchBytes-batchBytes {
			return errCollectionLedgerQuota
		}
		batchBytes += int64(len(encoded[i]))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db != nil {
		return l.db.Update(func(tx *bolt.Tx) error {
			return appendCollectionValidationDisk(tx, l.maxBytes, operationID, items, encoded)
		})
	}
	if l.validationBytes < 0 || l.validationBytes > l.bytes || l.bytes > l.maxBytes {
		return errCollectionLedgerCorrupt
	}
	prior := l.validationRows[operationID]
	v := &collectionLedgerView{validationRows: l.validationRows, validationOperationBytes: l.validationOperationBytes, validationBytes: l.validationBytes, bytes: l.bytes}
	if _, _, err := v.validationStats(operationID); err != nil {
		return err
	}
	pending := make(map[uint64][]byte, len(items))
	used := l.bytes
	for i, item := range items {
		previous := prior[item.Ordinal]
		if previous == nil {
			previous = pending[item.Ordinal]
		}
		if previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			continue
		}
		last := uint64(len(prior) + len(pending))
		if item.Ordinal != last+1 {
			return errCollectionLedgerConflict
		}
		if last > 0 {
			lastRaw := pending[last]
			if lastRaw == nil {
				lastRaw = prior[last]
			}
			row, err := decodeCollectionValidationLedgerRow(lastRaw)
			if err != nil || row.OperationID != operationID || row.Item.Ordinal != last {
				return errCollectionLedgerCorrupt
			}
		}
		if int64(len(encoded[i])) > l.maxBytes-used {
			return errCollectionLedgerQuota
		}
		pending[item.Ordinal] = encoded[i]
		used += int64(len(encoded[i]))
	}
	if prior == nil {
		prior = make(map[uint64][]byte)
	}
	for ordinal, raw := range pending {
		prior[ordinal] = raw
	}
	l.validationRows[operationID] = prior
	l.validationOperationBytes[operationID] += used - l.bytes
	l.validationBytes += used - l.bytes
	l.bytes = used
	return nil
}

func appendCollectionValidationDisk(tx *bolt.Tx, maxBytes int64, operationID string, items []CollectionValidationItem, encoded [][]byte) error {
	records, stats, meta := tx.Bucket(collectionLedgerValidation), tx.Bucket(collectionLedgerValidationMeta), tx.Bucket(collectionLedgerMeta)
	if records == nil || stats == nil {
		return errCollectionLedgerCorrupt
	}
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return err
	}
	validationBytes, err := collectionLedgerValidationBytes(meta)
	if err != nil || validationBytes > used || used > maxBytes {
		return errCollectionLedgerCorrupt
	}
	v := &collectionLedgerView{tx: tx}
	count, operationBytes, err := v.validationStats(operationID)
	if err != nil {
		return err
	}
	rows := records.Bucket([]byte(operationID))
	if rows == nil {
		rows, err = records.CreateBucket([]byte(operationID))
		if err != nil {
			return err
		}
	}
	before := used
	for i, item := range items {
		key := collectionOrdinal(item.Ordinal)
		if previous := rows.Get(key); previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			continue
		}
		if item.Ordinal != count+1 {
			return errCollectionLedgerConflict
		}
		if count > 0 {
			last := rows.Get(collectionOrdinal(count))
			row, err := decodeCollectionValidationLedgerRow(last)
			if err != nil || row.OperationID != operationID || row.Item.Ordinal != count {
				return errCollectionLedgerCorrupt
			}
		}
		if int64(len(encoded[i])) > maxBytes-used {
			return errCollectionLedgerQuota
		}
		if err := rows.Put(key, encoded[i]); err != nil {
			return err
		}
		count++
		used += int64(len(encoded[i]))
	}
	if err := rows.SetSequence(count); err != nil {
		return err
	}
	if err := stats.Put([]byte(operationID), collectionOrdinal(uint64(operationBytes+used-before))); err != nil {
		return err
	}
	if err := meta.Put([]byte("validation_bytes"), collectionOrdinal(uint64(validationBytes+used-before))); err != nil {
		return err
	}
	return meta.Put([]byte("bytes"), collectionOrdinal(uint64(used)))
}

func (l *collectionLedger) ValidationStats(operationID string) (uint64, int64, error) {
	var count uint64
	var size int64
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		count, size, err = v.ValidationStats(operationID)
		return err
	})
	return count, size, err
}

func (l *collectionLedger) ValidationPage(operationID string, after uint64, limit int) ([]CollectionValidationItem, error) {
	var page []CollectionValidationItem
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		page, err = v.ValidationPage(operationID, after, limit)
		return err
	})
	return page, err
}

func (v *collectionLedgerView) freezeValidation(l *collectionLedger) {
	v.validationRows = make(map[string]map[uint64][]byte, len(l.validationRows))
	v.validationOperationBytes = make(map[string]int64, len(l.validationOperationBytes))
	for operation, rows := range l.validationRows {
		copy := make(map[uint64][]byte, len(rows))
		for ordinal, raw := range rows {
			copy[ordinal] = raw
		} // Encoded rows are immutable.
		v.validationRows[operation] = copy
	}
	for operation, size := range l.validationOperationBytes {
		v.validationOperationBytes[operation] = size
	}
}
