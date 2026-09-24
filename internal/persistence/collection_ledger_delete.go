package persistence

import (
	"bytes"
	"encoding/binary"

	bolt "go.etcd.io/bbolt"
)

type collectionLedgerDeletion struct {
	Rows         uint64
	EncodedBytes int64
	More         bool
}

type collectionDeletionRow struct {
	ordinal uint64
	key     CatalogKey
}

// DeletePage removes a bounded highest-ordinal tail, leaving a contiguous prefix
// compatible with the existing logical snapshot stream. The FSM must prohibit
// further uploads before cleanup begins; this primitive cannot decide terminal
// state or preserve a deleted item's replay receipt. Expected totals come from
// the authoritative FSM header and match atomically maintained derived counters.
func (l *collectionLedger) DeletePage(operationID string, expectedRows uint64, expectedBytes int64) (collectionLedgerDeletion, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil || expectedBytes < 0 || expectedRows > maxCollectionItems || expectedBytes > l.maxBytes || (expectedRows == 0) != (expectedBytes == 0) {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return collectionLedgerDeletion{}, errCollectionLedgerClosed
	}
	if l.db != nil {
		var result collectionLedgerDeletion
		transaction := l.db.Update
		if expectedRows == 0 {
			transaction = l.db.View
		}
		err := transaction(func(tx *bolt.Tx) error {
			var err error
			result, err = deleteCollectionDisk(tx, operationID, expectedRows, expectedBytes)
			return err
		})
		if err != nil {
			return collectionLedgerDeletion{}, err
		}
		return result, nil
	}
	rows, keys := l.rows[operationID], l.keys[operationID]
	if uint64(len(rows)) != expectedRows || l.operationBytes[operationID] != expectedBytes {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	if len(keys) != len(rows) || expectedBytes > l.bytes {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	if expectedRows == 0 {
		return collectionLedgerDeletion{}, nil
	}
	v := &collectionLedgerView{rows: l.rows, keys: l.keys}
	selected, result, err := v.planDelete(operationID, expectedRows, expectedBytes)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	for _, row := range selected {
		delete(rows, row.ordinal)
		delete(keys, row.key)
	}
	l.bytes -= result.EncodedBytes
	if result.More {
		l.operationBytes[operationID] -= result.EncodedBytes
	} else {
		delete(l.rows, operationID)
		delete(l.keys, operationID)
		delete(l.operationBytes, operationID)
	}
	return result, nil
}

func deleteCollectionDisk(tx *bolt.Tx, operationID string, expectedRows uint64, expectedBytes int64) (collectionLedgerDeletion, error) {
	records, index, meta := tx.Bucket(collectionLedgerRecords), tx.Bucket(collectionLedgerKeys), tx.Bucket(collectionLedgerMeta)
	if records == nil || index == nil {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	rows, keys := records.Bucket([]byte(operationID)), index.Bucket([]byte(operationID))
	if rows == nil && keys == nil {
		if expectedRows != 0 || expectedBytes != 0 {
			return collectionLedgerDeletion{}, errCollectionLedgerConflict
		}
		return collectionLedgerDeletion{}, nil
	}
	if rows == nil || keys == nil {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	if rows.Sequence() != expectedRows || keys.Sequence() != uint64(expectedBytes) {
		return collectionLedgerDeletion{}, errCollectionLedgerConflict
	}
	if expectedBytes > used || expectedRows == 0 {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	last, _ := rows.Cursor().Last()
	if !bytes.Equal(last, collectionOrdinal(expectedRows)) {
		return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
	}
	v := &collectionLedgerView{tx: tx}
	selected, result, err := v.planDelete(operationID, expectedRows, expectedBytes)
	if err != nil {
		return collectionLedgerDeletion{}, err
	}
	remaining := expectedRows - result.Rows
	if remaining == 0 {
		// The selected tail contains the entire bounded operation. Check for
		// orphan index entries before mutating, then drop the buckets directly.
		// Cursor.Last on a just-emptied multi-page bucket can loop in bbolt 1.4.3
		// before commit-time rebalance; never traverse that transient shape.
		var count uint64
		cursor := keys.Cursor()
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			count++
			if count > expectedRows {
				return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
			}
		}
		if count != expectedRows {
			return collectionLedgerDeletion{}, errCollectionLedgerCorrupt
		}
		if err := records.DeleteBucket([]byte(operationID)); err != nil {
			return collectionLedgerDeletion{}, err
		}
		if err := index.DeleteBucket([]byte(operationID)); err != nil {
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
		for _, row := range selected {
			if err := rows.Delete(collectionOrdinal(row.ordinal)); err != nil {
				return collectionLedgerDeletion{}, err
			}
			if err := keys.Delete([]byte(row.key.indexKey())); err != nil {
				return collectionLedgerDeletion{}, err
			}
		}
		if err := rows.SetSequence(remaining); err != nil {
			return collectionLedgerDeletion{}, err
		}
		if err := keys.SetSequence(uint64(expectedBytes - result.EncodedBytes)); err != nil {
			return collectionLedgerDeletion{}, err
		}
	}
	if err := meta.Put([]byte("bytes"), collectionOrdinal(uint64(used-result.EncodedBytes))); err != nil {
		return collectionLedgerDeletion{}, err
	}
	return result, nil
}

func (v *collectionLedgerView) planDelete(operationID string, expectedRows uint64, expectedBytes int64) ([]collectionDeletionRow, collectionLedgerDeletion, error) {
	selected := make([]collectionDeletionRow, 0, collectionLedgerBatchLimit)
	var result collectionLedgerDeletion
	var cursor *bolt.Cursor
	if v.tx != nil {
		cursor = v.tx.Bucket(collectionLedgerRecords).Bucket([]byte(operationID)).Cursor()
	}
	for ordinal := expectedRows; ordinal > 0 && len(selected) < collectionLedgerBatchLimit; ordinal-- {
		var data []byte
		if cursor == nil {
			var err error
			data, err = v.encodedItem(operationID, ordinal)
			if err != nil {
				return nil, result, err
			}
		} else {
			var key []byte
			if ordinal == expectedRows {
				key, data = cursor.Last()
			} else {
				key, data = cursor.Prev()
			}
			if !bytes.Equal(key, collectionOrdinal(ordinal)) || len(data) > collectionLedgerMaxFrame {
				return nil, result, errCollectionLedgerCorrupt
			}
		}
		if data == nil {
			return nil, result, errCollectionLedgerCorrupt
		}
		if int64(len(data)) > collectionLedgerBatchBytes-result.EncodedBytes {
			break
		}
		row, err := decodeCollectionLedgerRow(data)
		if err != nil || row.OperationID != operationID || row.Item.Ordinal != ordinal {
			return nil, result, errCollectionLedgerCorrupt
		}
		if v.tx == nil {
			if v.keys[operationID][row.Item.Key] != ordinal {
				return nil, result, errCollectionLedgerCorrupt
			}
		} else {
			keys := v.tx.Bucket(collectionLedgerKeys).Bucket([]byte(operationID))
			index := keys.Get([]byte(row.Item.Key.indexKey()))
			if len(index) != 8 || binary.BigEndian.Uint64(index) != ordinal {
				return nil, result, errCollectionLedgerCorrupt
			}
		}
		selected = append(selected, collectionDeletionRow{ordinal: ordinal, key: row.Item.Key})
		result.Rows++
		result.EncodedBytes += int64(len(data))
	}
	result.More = result.Rows < expectedRows
	if result.Rows == 0 || result.EncodedBytes > expectedBytes || result.More && result.EncodedBytes == expectedBytes || !result.More && result.EncodedBytes != expectedBytes {
		return nil, result, errCollectionLedgerCorrupt
	}
	if !result.More && cursor != nil {
		if key, _ := cursor.Prev(); key != nil {
			return nil, result, errCollectionLedgerCorrupt
		}
	}
	return selected, result, nil
}
