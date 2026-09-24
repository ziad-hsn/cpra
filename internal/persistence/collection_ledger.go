package persistence

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/google/btree"
	bolt "go.etcd.io/bbolt"
)

const (
	collectionLedgerFormat     = 1
	collectionLedgerMaxFrame   = 2 << 20
	collectionLedgerPageLimit  = 500
	collectionLedgerPageBytes  = 4 << 20
	collectionLedgerBatchLimit = 256
	collectionLedgerBatchBytes = 4 << 20
)

var (
	errCollectionLedgerConflict = errors.New("collection ledger identity conflicts with committed input")
	errCollectionLedgerCorrupt  = errors.New("collection ledger is corrupt or incompatible")
	errCollectionLedgerQuota    = errors.New("collection ledger exceeds its encoded byte quota")
	errCollectionLedgerClosed   = errors.New("collection ledger is closed")
	collectionLedgerRecords     = []byte("records")
	collectionLedgerKeys        = []byte("keys")
	collectionLedgerMeta        = []byte("metadata")
)

// collectionLedger is a materialization of committed encrypted collection rows.
// It contains no authoritative phase, execution progress, keys or plaintext.
// Recovery always writes a fresh generation from a snapshot and committed logs;
// the selected generation is never used to decide the outcome of log replay.
type collectionLedger struct {
	mu                       sync.RWMutex
	db                       *bolt.DB
	directory, generation    string
	maxBytes, bytes          int64
	closed                   bool
	rows                     map[string]map[uint64][]byte
	keys                     map[string]map[CatalogKey]uint64
	operationBytes           map[string]int64
	planRows                 map[string]map[uint64][]byte
	planOperationBytes       map[string]int64
	planBytes                int64
	validationRows           map[string]map[uint64][]byte
	validationOperationBytes map[string]int64
	validationBytes          int64
	executionRows            map[string]map[string][]byte
	executionStats           map[string]collectionExecutionStats
	executionOrder           map[string]*btree.BTreeG[string]
	executionBytes           int64
}

type collectionLedgerRow struct {
	OperationID string         `json:"operation_id"`
	Item        CollectionItem `json:"item"`
}

// collectionItemEncoding is shared by the FSM's quota and inventory digest.
// It deliberately hashes only the committed ciphertext representation.
func collectionItemEncoding(operationID string, item CollectionItem) ([]byte, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if err := item.validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(collectionLedgerRow{OperationID: operationID, Item: item})
	if err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if len(data) > collectionLedgerMaxFrame {
		return nil, errCollectionLedgerQuota
	}
	return data, nil
}

func collectionItemCost(operationID string, item CollectionItem) (int64, error) {
	data, err := collectionItemEncoding(operationID, item)
	return int64(len(data)), err
}

func newMemoryCollectionLedger(maxBytes int64) (*collectionLedger, error) {
	if maxBytes < 1 || maxBytes > maxCollectionLedgerBytes {
		return nil, errCollectionLedgerQuota
	}
	return &collectionLedger{maxBytes: maxBytes, rows: make(map[string]map[uint64][]byte), keys: make(map[string]map[CatalogKey]uint64), operationBytes: make(map[string]int64),
		planRows: make(map[string]map[uint64][]byte), planOperationBytes: make(map[string]int64), validationRows: make(map[string]map[uint64][]byte), validationOperationBytes: make(map[string]int64), executionRows: make(map[string]map[string][]byte), executionStats: make(map[string]collectionExecutionStats), executionOrder: make(map[string]*btree.BTreeG[string])}, nil
}

func (l *collectionLedger) Append(operationID string, item CollectionItem) error {
	return l.AppendBatch(operationID, []CollectionItem{item})
}

// AppendBatch commits rows, uniqueness indexes and accounting in one synchronous
// transaction. A rejected later row cannot leave an admitted earlier prefix.
// Repeated ordinals must contain the original ciphertext, not a new encryption
// of equivalent plaintext. The FSM owns protocol chunk and command size limits.
func (l *collectionLedger) AppendBatch(operationID string, items []CollectionItem) error {
	if len(items) == 0 {
		return errCollectionLedgerCorrupt
	}
	encoded := make([][]byte, len(items))
	for i, item := range items {
		var err error
		encoded[i], err = collectionItemEncoding(operationID, item)
		if err != nil {
			return err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db != nil {
		return l.db.Update(func(tx *bolt.Tx) error { return l.appendDisk(tx, operationID, items, encoded) })
	}
	rows := make(map[uint64][]byte, len(items))
	keys := make(map[CatalogKey]uint64, len(items))
	previousRows, previousKeys := l.rows[operationID], l.keys[operationID]
	used := l.bytes
	for i, item := range items {
		previous, exists := previousRows[item.Ordinal]
		if !exists {
			previous, exists = rows[item.Ordinal]
		}
		if exists {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			continue
		}
		if item.Ordinal != uint64(len(previousRows)+len(rows))+1 {
			return errCollectionLedgerConflict
		}
		if _, exists := previousKeys[item.Key]; exists {
			return errCollectionLedgerConflict
		}
		if _, exists := keys[item.Key]; exists {
			return errCollectionLedgerConflict
		}
		if int64(len(encoded[i])) > l.maxBytes-used {
			return errCollectionLedgerQuota
		}
		rows[item.Ordinal], keys[item.Key] = encoded[i], item.Ordinal
		used += int64(len(encoded[i]))
	}
	if previousRows == nil {
		previousRows = make(map[uint64][]byte)
	}
	if previousKeys == nil {
		previousKeys = make(map[CatalogKey]uint64)
	}
	for ordinal, data := range rows {
		previousRows[ordinal] = data
	}
	for key, ordinal := range keys {
		previousKeys[key] = ordinal
	}
	l.operationBytes[operationID] += used - l.bytes
	l.rows[operationID], l.keys[operationID], l.bytes = previousRows, previousKeys, used
	return nil
}

func (l *collectionLedger) appendDisk(tx *bolt.Tx, operationID string, items []CollectionItem, encoded [][]byte) error {
	records, index, meta := tx.Bucket(collectionLedgerRecords), tx.Bucket(collectionLedgerKeys), tx.Bucket(collectionLedgerMeta)
	if records == nil || index == nil || meta == nil {
		return errCollectionLedgerCorrupt
	}
	rows, err := records.CreateBucketIfNotExists([]byte(operationID))
	if err != nil {
		return err
	}
	keys, err := index.CreateBucketIfNotExists([]byte(operationID))
	if err != nil {
		return err
	}
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return err
	}
	// The key-index bucket's otherwise unused sequence stores the derived
	// per-operation encoded-byte total. Fresh reconstruction computes it from
	// authoritative rows; deletion need not scan a whole operation for totals.
	operationBytes := keys.Sequence()
	if operationBytes > uint64(used) {
		return errCollectionLedgerCorrupt
	}
	originalUsed := used
	for i, item := range items {
		ordinal := collectionOrdinal(item.Ordinal)
		if previous := rows.Get(ordinal); previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			if value := keys.Get([]byte(item.Key.indexKey())); !bytes.Equal(value, ordinal) {
				return errCollectionLedgerCorrupt
			}
			continue
		}
		if rows.Sequence() == math.MaxUint64 || item.Ordinal != rows.Sequence()+1 {
			return errCollectionLedgerConflict
		}
		if keys.Get([]byte(item.Key.indexKey())) != nil {
			return errCollectionLedgerConflict
		}
		if int64(len(encoded[i])) > l.maxBytes-used {
			return errCollectionLedgerQuota
		}
		if err = rows.Put(ordinal, encoded[i]); err != nil {
			return err
		}
		if err = keys.Put([]byte(item.Key.indexKey()), ordinal); err != nil {
			return err
		}
		if err = rows.SetSequence(item.Ordinal); err != nil {
			return err
		}
		used += int64(len(encoded[i]))
	}
	if err := keys.SetSequence(operationBytes + uint64(used-originalUsed)); err != nil {
		return err
	}
	return meta.Put([]byte("bytes"), collectionOrdinal(uint64(used)))
}

func collectionOrdinal(value uint64) []byte {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	return data[:]
}

func collectionLedgerBytes(meta *bolt.Bucket) (int64, error) {
	if meta == nil {
		return 0, errCollectionLedgerCorrupt
	}
	if !bytes.Equal(meta.Get([]byte("format")), collectionOrdinal(collectionLedgerFormat)) {
		return 0, errCollectionLedgerCorrupt
	}
	data := meta.Get([]byte("bytes"))
	if len(data) != 8 || binary.BigEndian.Uint64(data) > math.MaxInt64 {
		return 0, errCollectionLedgerCorrupt
	}
	return int64(binary.BigEndian.Uint64(data)), nil
}

func decodeCollectionLedgerRow(data []byte) (collectionLedgerRow, error) {
	var row collectionLedgerRow
	if len(data) == 0 || len(data) > collectionLedgerMaxFrame {
		return row, errCollectionLedgerCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&row); err != nil {
		return row, errCollectionLedgerCorrupt
	}
	canonical, err := collectionItemEncoding(row.OperationID, row.Item)
	if err != nil || !bytes.Equal(data, canonical) {
		return row, errCollectionLedgerCorrupt
	}
	return row, nil
}

func (l *collectionLedger) Item(operationID string, ordinal uint64) (CollectionItem, bool, error) {
	var item CollectionItem
	var found bool
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		item, found, err = v.item(operationID, ordinal)
		return err
	})
	return item, found, err
}

func (l *collectionLedger) Find(operationID string, key CatalogKey) (uint64, bool, error) {
	var ordinal uint64
	var found bool
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		ordinal, found, err = v.Find(operationID, key)
		return err
	})
	return ordinal, found, err
}

func (l *collectionLedger) Page(operationID string, after uint64, limit int) ([]CollectionItem, error) {
	var page []CollectionItem
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		page, err = v.Page(operationID, after, limit)
		return err
	})
	return page, err
}

func (l *collectionLedger) read(read func(*collectionLedgerView) error) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db == nil {
		return read(&collectionLedgerView{rows: l.rows, keys: l.keys, operationBytes: l.operationBytes, bytes: l.bytes,
			planRows: l.planRows, planOperationBytes: l.planOperationBytes, planBytes: l.planBytes, validationRows: l.validationRows, validationOperationBytes: l.validationOperationBytes, validationBytes: l.validationBytes, executionRows: l.executionRows, executionStats: l.executionStats, executionOrder: l.executionOrder, executionBytes: l.executionBytes})
	}
	return l.db.View(func(tx *bolt.Tx) error { return read(&collectionLedgerView{tx: tx}) })
}

func (l *collectionLedger) Bytes() (int64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return 0, errCollectionLedgerClosed
	}
	if l.db == nil {
		return l.bytes, nil
	}
	var size int64
	err := l.db.View(func(tx *bolt.Tx) error {
		var err error
		size, err = collectionLedgerBytes(tx.Bucket(collectionLedgerMeta))
		return err
	})
	return size, err
}

// Close requires every frozen view to be released. bbolt deliberately waits for
// read transactions before unmapping its database. Snapshot.Release owns this.
func (l *collectionLedger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.rows, l.keys, l.operationBytes = nil, nil, nil
	l.planRows, l.planOperationBytes = nil, nil
	l.validationRows, l.validationOperationBytes = nil, nil
	l.executionRows, l.executionStats, l.executionOrder = nil, nil, nil
	if l.db != nil {
		return l.db.Close()
	}
	return nil
}

type collectionLedgerView struct {
	mu                       sync.Mutex // bbolt read transactions are not safe for concurrent access.
	tx                       *bolt.Tx
	rows                     map[string]map[uint64][]byte
	keys                     map[string]map[CatalogKey]uint64
	operationBytes           map[string]int64
	bytes                    int64
	closed                   bool
	planRows                 map[string]map[uint64][]byte
	planOperationBytes       map[string]int64
	planBytes                int64
	validationRows           map[string]map[uint64][]byte
	validationOperationBytes map[string]int64
	validationBytes          int64
	executionRows            map[string]map[string][]byte
	executionStats           map[string]collectionExecutionStats
	executionOrder           map[string]*btree.BTreeG[string]
	executionBytes           int64
}

func (l *collectionLedger) Freeze() (*collectionLedgerView, error) {
	// BTree.Clone installs a new COW context in the original memory index.
	// Serialize concurrent freezes as well as writes while cloning it.
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.freezeLocked()
}

// freezeLocked requires exclusive ledger ownership: BTree.Clone mutates its COW context.
func (l *collectionLedger) freezeLocked() (*collectionLedgerView, error) {
	if l.closed {
		return nil, errCollectionLedgerClosed
	}
	v := &collectionLedgerView{bytes: l.bytes, planBytes: l.planBytes, validationBytes: l.validationBytes, executionBytes: l.executionBytes}
	if l.db != nil {
		var err error
		v.tx, err = l.db.Begin(false)
		if err != nil {
			return nil, err
		}
		v.bytes, err = collectionLedgerBytes(v.tx.Bucket(collectionLedgerMeta))
		if err == nil {
			v.planBytes, err = collectionLedgerPlanBytes(v.tx.Bucket(collectionLedgerMeta))
		}
		if err == nil {
			v.validationBytes, err = collectionLedgerValidationBytes(v.tx.Bucket(collectionLedgerMeta))
		}
		if err == nil {
			v.executionBytes, err = collectionLedgerExecutionBytes(v.tx.Bucket(collectionLedgerMeta))
		}
		if err == nil && (v.planBytes > v.bytes || v.validationBytes > v.bytes-v.planBytes || v.executionBytes > v.bytes-v.planBytes-v.validationBytes) {
			err = errCollectionLedgerCorrupt
		}
		if err != nil {
			_ = v.tx.Rollback()
			return nil, err
		}
		return v, nil
	}
	// Encoded rows are immutable. Copy only the bounded index maps; reads decode
	// fresh slices, so neither a caller nor a subsequent append can alter a view.
	v.rows = make(map[string]map[uint64][]byte, len(l.rows))
	v.keys = make(map[string]map[CatalogKey]uint64, len(l.keys))
	v.operationBytes = make(map[string]int64, len(l.operationBytes))
	for operation, size := range l.operationBytes {
		v.operationBytes[operation] = size
	}
	for operation, rows := range l.rows {
		copy := make(map[uint64][]byte, len(rows))
		for ordinal, data := range rows {
			copy[ordinal] = data
		}
		v.rows[operation] = copy
	}
	for operation, keys := range l.keys {
		copy := make(map[CatalogKey]uint64, len(keys))
		for key, ordinal := range keys {
			copy[key] = ordinal
		}
		v.keys[operation] = copy
	}
	v.freezePlans(l)
	v.freezeValidation(l)
	v.freezeExecution(l)
	return v, nil
}

func (v *collectionLedgerView) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	v.rows, v.keys, v.operationBytes = nil, nil, nil
	v.planRows, v.planOperationBytes = nil, nil
	v.validationRows, v.validationOperationBytes = nil, nil
	v.executionRows, v.executionStats, v.executionOrder = nil, nil, nil
	if v.tx != nil {
		return v.tx.Rollback()
	}
	return nil
}

func (v *collectionLedgerView) Bytes() (int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, errCollectionLedgerClosed
	}
	return v.bytes, nil
}

// encodedItem borrows a row only for the lifetime of the locked view/transaction.
// Inspecting its size must not decode or clone its potentially large payload.
func (v *collectionLedgerView) encodedItem(operationID string, ordinal uint64) ([]byte, error) {
	var data []byte
	if v.tx == nil {
		data = v.rows[operationID][ordinal]
	} else {
		bucket := v.tx.Bucket(collectionLedgerRecords)
		if bucket == nil {
			return nil, errCollectionLedgerCorrupt
		}
		if rows := bucket.Bucket([]byte(operationID)); rows != nil {
			data = rows.Get(collectionOrdinal(ordinal))
		}
	}
	if len(data) > collectionLedgerMaxFrame {
		return nil, errCollectionLedgerCorrupt
	}
	return data, nil
}

func (v *collectionLedgerView) item(operationID string, ordinal uint64) (CollectionItem, bool, error) {
	data, err := v.encodedItem(operationID, ordinal)
	if err != nil {
		return CollectionItem{}, false, err
	}
	if data == nil {
		return CollectionItem{}, false, nil
	}
	row, err := decodeCollectionLedgerRow(data)
	if err != nil || row.OperationID != operationID || row.Item.Ordinal != ordinal {
		return CollectionItem{}, false, errCollectionLedgerCorrupt
	}
	return row.Item, true, nil
}

func (v *collectionLedgerView) Item(operationID string, ordinal uint64) (CollectionItem, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return CollectionItem{}, false, errCollectionLedgerClosed
	}
	return v.item(operationID, ordinal)
}

func (v *collectionLedgerView) Find(operationID string, key CatalogKey) (uint64, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, false, errCollectionLedgerClosed
	}
	var ordinal uint64
	if v.tx == nil {
		ordinal = v.keys[operationID][key]
	} else {
		bucket := v.tx.Bucket(collectionLedgerKeys)
		if bucket == nil {
			return 0, false, errCollectionLedgerCorrupt
		}
		keys := bucket.Bucket([]byte(operationID))
		if keys == nil {
			return 0, false, nil
		}
		data := keys.Get([]byte(key.indexKey()))
		if data == nil {
			return 0, false, nil
		}
		if len(data) != 8 {
			return 0, false, errCollectionLedgerCorrupt
		}
		ordinal = binary.BigEndian.Uint64(data)
	}
	if ordinal == 0 {
		return 0, false, nil
	}
	item, found, err := v.item(operationID, ordinal)
	if err != nil || !found || item.Key != key {
		return 0, false, errCollectionLedgerCorrupt
	}
	return ordinal, true, nil
}

// Page returns at most limit rows and 4 MiB of encoded row data. Continue using
// the last returned ordinal; a byte-limited page can be shorter than limit.
func (v *collectionLedgerView) Page(operationID string, after uint64, limit int) ([]CollectionItem, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errCollectionLedgerClosed
	}
	if limit < 1 || limit > collectionLedgerPageLimit {
		return nil, errors.New("invalid collection ledger page limit")
	}
	page := make([]CollectionItem, 0, limit)
	var encodedBytes int
	for ordinal := after; ordinal != math.MaxUint64 && len(page) < limit; {
		ordinal++
		data, err := v.encodedItem(operationID, ordinal)
		if err != nil {
			return nil, err
		}
		if data == nil || len(data) > collectionLedgerPageBytes-encodedBytes {
			break
		}
		row, err := decodeCollectionLedgerRow(data)
		if err != nil || row.OperationID != operationID || row.Item.Ordinal != ordinal {
			return nil, errCollectionLedgerCorrupt
		}
		page = append(page, row.Item)
		encodedBytes += len(data)
	}
	return page, nil
}

// Walk visits detached rows in operation-handle/ordinal order, retaining one
// decoded row at a time. The callback must not call methods on the same view.
func (v *collectionLedgerView) Walk(visit func(string, CollectionItem) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errCollectionLedgerClosed
	}
	return v.walk(func(_ []byte, row collectionLedgerRow) error { return visit(row.OperationID, row.Item) })
}

func (v *collectionLedgerView) walk(visit func([]byte, collectionLedgerRow) error) error {
	var observed int64
	var operationBytes int64
	check := func(operation string, ordinal uint64, data []byte) error {
		row, err := decodeCollectionLedgerRow(data)
		if err != nil || row.OperationID != operation || row.Item.Ordinal != ordinal {
			return errCollectionLedgerCorrupt
		}
		if v.tx == nil {
			if v.keys[operation][row.Item.Key] != ordinal {
				return errCollectionLedgerCorrupt
			}
		} else {
			index := v.tx.Bucket(collectionLedgerKeys)
			if index == nil {
				return errCollectionLedgerCorrupt
			}
			keys := index.Bucket([]byte(operation))
			if keys == nil || !bytes.Equal(keys.Get([]byte(row.Item.Key.indexKey())), collectionOrdinal(ordinal)) {
				return errCollectionLedgerCorrupt
			}
		}
		observed += int64(len(data))
		operationBytes += int64(len(data))
		return visit(data, row)
	}
	if v.tx == nil {
		operations := make([]string, 0, len(v.rows))
		for operation := range v.rows {
			operations = append(operations, operation)
		}
		slices.Sort(operations)
		for _, operation := range operations {
			operationBytes = 0
			rows := v.rows[operation]
			if len(v.keys[operation]) != len(rows) {
				return errCollectionLedgerCorrupt
			}
			for ordinal := uint64(1); ordinal <= uint64(len(rows)); ordinal++ {
				if err := check(operation, ordinal, rows[ordinal]); err != nil {
					return err
				}
			}
			if operationBytes != v.operationBytes[operation] {
				return errCollectionLedgerCorrupt
			}
		}
		if len(v.keys) != len(v.rows) || len(v.operationBytes) != len(v.rows) {
			return errCollectionLedgerCorrupt
		}
	} else {
		bucket := v.tx.Bucket(collectionLedgerRecords)
		if bucket == nil {
			return errCollectionLedgerCorrupt
		}
		err := bucket.ForEach(func(operation, value []byte) error {
			operationBytes = 0
			if value != nil {
				return errCollectionLedgerCorrupt
			}
			rows := bucket.Bucket(operation)
			var ordinal uint64
			err := rows.ForEach(func(key, data []byte) error {
				ordinal++
				if len(key) != 8 || binary.BigEndian.Uint64(key) != ordinal {
					return errCollectionLedgerCorrupt
				}
				return check(string(operation), ordinal, data)
			})
			if err != nil {
				return err
			}
			if ordinal == 0 || ordinal != rows.Sequence() {
				return errCollectionLedgerCorrupt
			}
			keys := v.tx.Bucket(collectionLedgerKeys).Bucket(operation)
			if keys.Sequence() != uint64(operationBytes) {
				return errCollectionLedgerCorrupt
			}
			return nil
		})
		if err != nil {
			return err
		}
		index := v.tx.Bucket(collectionLedgerKeys)
		if index == nil {
			return errCollectionLedgerCorrupt
		}
		err = index.ForEach(func(operation, value []byte) error {
			rows := bucket.Bucket(operation)
			if value != nil || rows == nil {
				return errCollectionLedgerCorrupt
			}
			keys := index.Bucket(operation)
			var count uint64
			err := keys.ForEach(func(_, ordinal []byte) error {
				if len(ordinal) != 8 {
					return errCollectionLedgerCorrupt
				}
				count++
				return nil
			})
			if err != nil {
				return err
			}
			if count != rows.Sequence() {
				return errCollectionLedgerCorrupt
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if v.planBytes < 0 || v.planBytes > v.bytes || v.validationBytes < 0 || v.validationBytes > v.bytes-v.planBytes || v.executionBytes < 0 || v.executionBytes > v.bytes-v.planBytes-v.validationBytes || observed != v.bytes-v.planBytes-v.validationBytes-v.executionBytes {
		return fmt.Errorf("%w: encoded byte accounting", errCollectionLedgerCorrupt)
	}
	return nil
}
