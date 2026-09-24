package persistence

import (
	"bytes"

	bolt "go.etcd.io/bbolt"
)

// DeleteValidationPage retires one bounded highest-ordinal tail. Expected totals must
// come from authoritative state. The caller must prohibit new validation writes before
// cleanup starts; deleting materialization does not cancel or forget a validation result.
// Logical quota is reclaimed, not physical bbolt allocation.
func (l *collectionLedger) DeleteValidationPage(operationID string, expectedRows uint64, expectedBytes int64) (collectionLedgerDeletion, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil || expectedRows > maxCollectionItems ||
		expectedBytes < 0 || expectedBytes > l.maxBytes || (expectedRows == 0) != (expectedBytes == 0) {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return collectionLedgerDeletion{}, errCollectionLedgerClosed
	}
	if l.db != nil {
		transaction := l.db.Update
		if expectedRows == 0 {
			transaction = l.db.View
		}
		var result collectionLedgerDeletion
		err := transaction(func(tx *bolt.Tx) error {
			var err error
			result, err = deleteCollectionValidationDisk(tx, operationID, expectedRows, expectedBytes)
			return err
		})
		if err != nil {
			return collectionLedgerDeletion{}, err
		}
		return result, nil
	}
	v := &collectionLedgerView{validationRows: l.validationRows, validationOperationBytes: l.validationOperationBytes, validationBytes: l.validationBytes, bytes: l.bytes}
	count, size, err := v.validationStats(operationID)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	if count != expectedRows || size != expectedBytes {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	if count == 0 {
		return collectionLedgerDeletion{}, nil
	}
	result, err := v.selectValidationTail(operationID, expectedRows, expectedBytes)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	remaining := expectedRows - result.Rows
	if remaining > 0 {
		if raw := l.validationRows[operationID][remaining]; len(raw) == 0 {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
	}
	for ordinal := remaining + 1; ordinal <= expectedRows; ordinal++ {
		delete(l.validationRows[operationID], ordinal)
	}
	l.bytes -= result.EncodedBytes
	l.validationBytes -= result.EncodedBytes
	if result.More {
		l.validationOperationBytes[operationID] -= result.EncodedBytes
	} else {
		delete(l.validationRows, operationID)
		delete(l.validationOperationBytes, operationID)
	}
	return result, nil
}

func deleteCollectionValidationDisk(tx *bolt.Tx, operationID string, expectedRows uint64, expectedBytes int64) (collectionLedgerDeletion, error) {
	v := &collectionLedgerView{tx: tx}
	count, size, err := v.validationStats(operationID)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	if count != expectedRows || size != expectedBytes {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	if count == 0 {
		return collectionLedgerDeletion{}, nil
	}
	records, stats, meta := tx.Bucket(collectionLedgerValidation), tx.Bucket(collectionLedgerValidationMeta), tx.Bucket(collectionLedgerMeta)
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	validationBytes, err := collectionLedgerValidationBytes(meta)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	rows := records.Bucket([]byte(operationID))
	last, _ := rows.Cursor().Last()
	if !bytes.Equal(last, collectionOrdinal(expectedRows)) {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	result, err := v.selectValidationTail(operationID, expectedRows, expectedBytes)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	remaining := expectedRows - result.Rows
	if remaining == 0 {
		// Delete the bucket directly. Never call Cursor.Last after emptying a
		// multi-page bucket before bbolt's commit-time rebalance.
		if err := records.DeleteBucket([]byte(operationID)); err != nil {
			return collectionLedgerDeletion{}, err
		}
		if err := stats.Delete([]byte(operationID)); err != nil {
			return collectionLedgerDeletion{}, err
		}
	} else {
		cursor := rows.Cursor()
		boundary, _ := cursor.Seek(collectionOrdinal(remaining))
		if !bytes.Equal(boundary, collectionOrdinal(remaining)) {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
		next, _ := cursor.Next()
		if !bytes.Equal(next, collectionOrdinal(remaining+1)) {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
		for ordinal := remaining + 1; ordinal <= expectedRows; ordinal++ {
			if err := rows.Delete(collectionOrdinal(ordinal)); err != nil {
				return collectionLedgerDeletion{}, err
			}
		}
		if err := rows.SetSequence(remaining); err != nil {
			return collectionLedgerDeletion{}, err
		}
		if err := stats.Put([]byte(operationID), collectionOrdinal(uint64(expectedBytes-result.EncodedBytes))); err != nil {
			return collectionLedgerDeletion{}, err
		}
	}
	if err := meta.Put([]byte("validation_bytes"), collectionOrdinal(uint64(validationBytes-result.EncodedBytes))); err != nil {
		return collectionLedgerDeletion{}, err
	}
	if err := meta.Put([]byte("bytes"), collectionOrdinal(uint64(used-result.EncodedBytes))); err != nil {
		return collectionLedgerDeletion{}, err
	}
	return result, nil
}

func (v *collectionLedgerView) selectValidationTail(operationID string, count uint64, expectedBytes int64) (collectionLedgerDeletion, error) {
	var result collectionLedgerDeletion
	var cursor *bolt.Cursor
	if v.tx != nil {
		cursor = v.tx.Bucket(collectionLedgerValidation).Bucket([]byte(operationID)).Cursor()
	}
	for ordinal := count; ordinal > 0 && result.Rows < collectionLedgerBatchLimit; ordinal-- {
		var raw []byte
		if cursor == nil {
			var err error
			raw, err = v.encodedValidationItem(operationID, ordinal)
			if err != nil {
				return collectionLedgerDeletion{}, err
			}
		} else {
			var key []byte
			if ordinal == count {
				key, raw = cursor.Last()
			} else {
				key, raw = cursor.Prev()
			}
			if !bytes.Equal(key, collectionOrdinal(ordinal)) {
				return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
			}
		}
		if len(raw) == 0 || len(raw) > collectionValidationLedgerMaxFrame {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
		if int64(len(raw)) > collectionLedgerBatchBytes-result.EncodedBytes {
			break
		}
		row, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil || row.OperationID != operationID || row.Item.Ordinal != ordinal {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
		result.Rows++
		result.EncodedBytes += int64(len(raw))
	}
	result.More = result.Rows < count
	if result.Rows == 0 || result.EncodedBytes > expectedBytes || result.More && result.EncodedBytes == expectedBytes || !result.More && result.EncodedBytes != expectedBytes {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	if !result.More && cursor != nil {
		if key, _ := cursor.Prev(); key != nil {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
	}
	return result, nil
}
