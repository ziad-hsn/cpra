package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"

	bolt "go.etcd.io/bbolt"
)

const (
	collectionPlanLedgerMaxFrame = CollectionPlanMaxFragmentBytes + 1024
	collectionPlanLedgerMaxParts = CollectionPlanMaxBytes / 4
)

var (
	collectionLedgerPlans    = []byte("plans")
	collectionLedgerPlanMeta = []byte("plan_metadata")
)

// CollectionPlanLedgerFragment stores one canonical typed codec fragment. Its
// ordinal is the fragment position, not the resource's activation ordinal.
// Header/footer are ordinary numbered fragments. These rows are materialized
// from committed logs/snapshots; their presence is never an execution grant.
type CollectionPlanLedgerFragment struct {
	Ordinal  uint64                 `json:"ordinal"`
	Fragment CollectionPlanFragment `json:"fragment"`
}

type collectionPlanLedgerRow struct {
	OperationID string                       `json:"operation_id"`
	Part        CollectionPlanLedgerFragment `json:"part"`
}

// Encoding validates a single fragment only. Complete descriptor, row ordering,
// predecessor and footer validation belong to the artifact verifier/future FSM.
func collectionPlanLedgerEncoding(operationID string, part CollectionPlanLedgerFragment) ([]byte, error) {
	if _, _, err := ParseOperationHandle(operationID); err != nil || part.Ordinal == 0 || part.Ordinal > collectionPlanLedgerMaxParts {
		return nil, errCollectionLedgerCorrupt
	}
	if err := part.Fragment.validate(); err != nil {
		return nil, err
	}
	if (part.Ordinal == 1) != (part.Fragment.Kind == "header") || part.Fragment.Header != nil && part.Fragment.Header.OperationID != operationID {
		return nil, errCollectionLedgerCorrupt
	}
	fragment, err := json.Marshal(part.Fragment)
	if err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if len(fragment) > CollectionPlanMaxFragmentBytes {
		return nil, errCollectionLedgerQuota
	}
	raw, err := json.Marshal(collectionPlanLedgerRow{OperationID: operationID, Part: part})
	if err != nil {
		return nil, errCollectionLedgerCorrupt
	}
	if len(raw) > collectionPlanLedgerMaxFrame {
		return nil, errCollectionLedgerQuota
	}
	return raw, nil
}

func collectionPlanLedgerCost(operationID string, part CollectionPlanLedgerFragment) (int64, error) {
	raw, err := collectionPlanLedgerEncoding(operationID, part)
	return int64(len(raw)), err
}

func decodeCollectionPlanLedgerRow(raw []byte) (collectionPlanLedgerRow, error) {
	var row collectionPlanLedgerRow
	if len(raw) == 0 || len(raw) > collectionPlanLedgerMaxFrame || collectionPlanJSON(context.Background(), raw) != nil {
		return row, errCollectionLedgerCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&row) != nil {
		return collectionPlanLedgerRow{}, errCollectionLedgerCorrupt
	}
	canonical, err := collectionPlanLedgerEncoding(row.OperationID, row.Part)
	if err != nil || !bytes.Equal(raw, canonical) {
		return collectionPlanLedgerRow{}, errCollectionLedgerCorrupt
	}
	return row, nil
}

func collectionLedgerPlanBytes(meta *bolt.Bucket) (int64, error) {
	if meta == nil {
		return 0, errCollectionLedgerCorrupt
	}
	raw := meta.Get([]byte("plan_bytes"))
	if len(raw) != 8 || binary.BigEndian.Uint64(raw) > math.MaxInt64 {
		return 0, errCollectionLedgerCorrupt
	}
	return int64(binary.BigEndian.Uint64(raw)), nil
}

// AppendPlanFragments atomically appends at most 256 fragments / 4 MiB of
// canonical storage records. Exact retries compare original bytes and consume
// no additional quota. Input and plan records share the ledger's global quota.
// Callers fence phase, descriptor and ownership in the authoritative FSM.
func (l *collectionLedger) AppendPlanFragments(operationID string, parts []CollectionPlanLedgerFragment) error {
	if len(parts) == 0 || len(parts) > collectionLedgerBatchLimit {
		return errCollectionLedgerQuota
	}
	encoded := make([][]byte, len(parts))
	var batchBytes int64
	for i, part := range parts {
		var err error
		encoded[i], err = collectionPlanLedgerEncoding(operationID, part)
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
		return l.db.Update(func(tx *bolt.Tx) error { return appendCollectionPlanDisk(tx, l.maxBytes, operationID, parts, encoded) })
	}
	if l.planBytes < 0 || l.planBytes > l.bytes || l.bytes > l.maxBytes {
		return errCollectionLedgerCorrupt
	}
	prior := l.planRows[operationID]
	if (len(prior) == 0) != (l.planOperationBytes[operationID] == 0) || l.planOperationBytes[operationID] > l.planBytes {
		return errCollectionLedgerCorrupt
	}
	pending := make(map[uint64][]byte, len(parts))
	used := l.bytes
	for i, part := range parts {
		previous := prior[part.Ordinal]
		if previous == nil {
			previous = pending[part.Ordinal]
		}
		if previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			continue
		}
		last := uint64(len(prior) + len(pending))
		if part.Ordinal != last+1 {
			return errCollectionLedgerConflict
		}
		if last > 0 {
			lastRaw := pending[last]
			if lastRaw == nil {
				lastRaw = prior[last]
			}
			row, err := decodeCollectionPlanLedgerRow(lastRaw)
			if err != nil || row.OperationID != operationID || row.Part.Ordinal != last {
				return errCollectionLedgerCorrupt
			}
			if row.Part.Fragment.Kind == "footer" {
				return errCollectionLedgerConflict
			}
		}
		if int64(len(encoded[i])) > l.maxBytes-used {
			return errCollectionLedgerQuota
		}
		pending[part.Ordinal] = encoded[i]
		used += int64(len(encoded[i]))
	}
	if prior == nil {
		prior = make(map[uint64][]byte)
	}
	for ordinal, raw := range pending {
		prior[ordinal] = raw
	}
	l.planRows[operationID] = prior
	l.planOperationBytes[operationID] += used - l.bytes
	l.planBytes += used - l.bytes
	l.bytes = used
	return nil
}

func appendCollectionPlanDisk(tx *bolt.Tx, maxBytes int64, operationID string, parts []CollectionPlanLedgerFragment, encoded [][]byte) error {
	plans, stats, meta := tx.Bucket(collectionLedgerPlans), tx.Bucket(collectionLedgerPlanMeta), tx.Bucket(collectionLedgerMeta)
	if plans == nil || stats == nil {
		return errCollectionLedgerCorrupt
	}
	used, err := collectionLedgerBytes(meta)
	if err != nil {
		return err
	}
	planBytes, err := collectionLedgerPlanBytes(meta)
	if err != nil || planBytes > used || used > maxBytes {
		return errCollectionLedgerCorrupt
	}
	v := &collectionLedgerView{tx: tx}
	count, operationBytes, err := v.planStats(operationID)
	if err != nil {
		return err
	}
	rows := plans.Bucket([]byte(operationID))
	if rows == nil {
		rows, err = plans.CreateBucket([]byte(operationID))
		if err != nil {
			return err
		}
	}
	before := used
	for i, part := range parts {
		key := collectionOrdinal(part.Ordinal)
		if previous := rows.Get(key); previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return errCollectionLedgerConflict
			}
			continue
		}
		if part.Ordinal != count+1 {
			return errCollectionLedgerConflict
		}
		if count > 0 {
			last := rows.Get(collectionOrdinal(count))
			row, err := decodeCollectionPlanLedgerRow(last)
			if err != nil || row.OperationID != operationID || row.Part.Ordinal != count {
				return errCollectionLedgerCorrupt
			}
			if row.Part.Fragment.Kind == "footer" {
				return errCollectionLedgerConflict
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
	if err := meta.Put([]byte("plan_bytes"), collectionOrdinal(uint64(planBytes+used-before))); err != nil {
		return err
	}
	return meta.Put([]byte("bytes"), collectionOrdinal(uint64(used)))
}

func (l *collectionLedger) PlanStats(operationID string) (uint64, int64, error) {
	var count uint64
	var size int64
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		count, size, err = v.PlanStats(operationID)
		return err
	})
	return count, size, err
}

func (l *collectionLedger) PlanPage(operationID string, after uint64, limit int) ([]CollectionPlanLedgerFragment, error) {
	var page []CollectionPlanLedgerFragment
	err := l.read(func(v *collectionLedgerView) error {
		var err error
		page, err = v.PlanPage(operationID, after, limit)
		return err
	})
	return page, err
}

func (v *collectionLedgerView) freezePlans(l *collectionLedger) {
	v.planRows = make(map[string]map[uint64][]byte, len(l.planRows))
	v.planOperationBytes = make(map[string]int64, len(l.planOperationBytes))
	for operation, rows := range l.planRows {
		copy := make(map[uint64][]byte, len(rows))
		for ordinal, raw := range rows {
			copy[ordinal] = raw
		} // Encoded rows are immutable.
		v.planRows[operation] = copy
	}
	for operation, size := range l.planOperationBytes {
		v.planOperationBytes[operation] = size
	}
}
