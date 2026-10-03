package persistence

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"github.com/google/btree"
	bolt "go.etcd.io/bbolt"
)

var (
	collectionLedgerExecution     = []byte("execution_rows")
	collectionLedgerExecutionMeta = []byte("execution_metadata")
)

// ChargedBytes counts encoded prepared/outcome rows plus permanent terminal
// capacity. TerminalBytes is already covered by that capacity, not extra quota.
type collectionExecutionStats struct {
	Binding          CollectionExecutionBinding `json:"binding"`
	Prepared         bool                       `json:"prepared"`
	Outcomes         uint64                     `json:"outcomes"`
	RetiredOutcomes  uint64                     `json:"retired_outcomes,omitempty"`
	Terminals        uint64                     `json:"terminals"`
	EncodedBytes     int64                      `json:"encoded_bytes"`
	TerminalBytes    int64                      `json:"terminal_bytes"`
	TerminalCapacity int64                      `json:"terminal_capacity"`
	ChargedBytes     int64                      `json:"charged_bytes"`
}

func (s collectionExecutionStats) validate(op string) error {
	if s == (collectionExecutionStats{}) {
		return nil
	}
	if s.Binding.validate() != nil || s.Binding.OperationID != op || s.Outcomes > maxCollectionItems || s.RetiredOutcomes > maxCollectionItems-s.Outcomes || s.Terminals > s.Outcomes ||
		s.TerminalCapacity < 0 || s.TerminalCapacity%collectionChildTerminalReserve != 0 ||
		s.TerminalCapacity > int64(s.Outcomes)*collectionChildTerminalReserve || int64(s.Terminals) > s.TerminalCapacity/collectionChildTerminalReserve ||
		s.TerminalBytes < 0 || s.TerminalBytes > int64(s.Terminals)*collectionChildTerminalReserve || s.TerminalBytes > s.TerminalCapacity ||
		s.EncodedBytes <= 0 || s.EncodedBytes < s.TerminalBytes || s.EncodedBytes > maxCollectionLedgerBytes ||
		s.ChargedBytes != s.EncodedBytes-s.TerminalBytes+s.TerminalCapacity || s.ChargedBytes > maxCollectionLedgerBytes ||
		!s.Prepared && s.Outcomes == 0 || s.Terminals == 0 && s.TerminalBytes != 0 || s.Terminals > 0 && s.TerminalBytes == 0 {
		return errCollectionLedgerCorrupt
	}
	return nil
}

func collectionExecutionBinding(r collectionExecutionRecord) CollectionExecutionBinding {
	if r.Prepared != nil {
		return r.Prepared.Binding
	}
	if r.Outcome != nil {
		return r.Outcome.Binding
	}
	return r.Terminal.Binding
}

func collectionLedgerExecutionBytes(meta *bolt.Bucket) (int64, error) {
	if meta == nil {
		return 0, errCollectionLedgerCorrupt
	}
	raw := meta.Get([]byte("execution_bytes"))
	if len(raw) != 8 || binary.BigEndian.Uint64(raw) > math.MaxInt64 {
		return 0, errCollectionLedgerCorrupt
	}
	return int64(binary.BigEndian.Uint64(raw)), nil
}

type collectionExecutionBatch struct {
	rows          map[string]map[string][]byte
	stats         map[string]collectionExecutionStats
	used, charged int64
}

// ApplyExecutionBatch is one synchronous transaction across all affected
// parents. An accepted outcome consumes its exact prepared record; unchanged
// and pre-preparation rejections require no prepared slot. Immutable retries
// compare exact canonical bytes. No catalog/header/provider work happens here.
func (l *collectionLedger) ApplyExecutionBatch(records []collectionExecutionRecord) error {
	return l.applyExecutionBatch(records, false)
}

// importExecutionBatch is solely for a fresh, unpublished recovery generation.
// Snapshots omit consumed preparation, so accepted outcomes do not require it.
// Outcomes precede the optional current prepared slot and sparse terminals.
// Root snapshot validation must verify the complete authoritative inventory
// before Publish. This is not a normal execution/acceptance API.
func (l *collectionLedger) importExecutionBatch(records []collectionExecutionRecord) error {
	return l.applyExecutionBatch(records, true)
}

func (l *collectionLedger) applyExecutionBatch(records []collectionExecutionRecord, importing bool) error {
	return l.applyExecutionBatchWithRetirement(records, importing, nil)
}

// importRetiredExecutionBatch accepts offsets only from the recovery image's
// retirement headers. The stream retains original ordinals; it never supplies
// authority to infer or discard a missing prefix.
func (l *collectionLedger) importRetiredExecutionBatch(records []collectionExecutionRecord, offsets map[string]uint64) error {
	return l.applyExecutionBatchWithRetirement(records, true, offsets)
}

func (l *collectionLedger) applyExecutionBatchWithRetirement(records []collectionExecutionRecord, importing bool, offsets map[string]uint64) error {
	if len(records) == 0 || len(records) > collectionLedgerBatchLimit {
		return errCollectionLedgerQuota
	}
	encoded := make([][]byte, len(records))
	var total int64
	for i, r := range records {
		raw, err := collectionExecutionEncoding(r)
		if err != nil {
			return err
		}
		if int64(len(raw)) > collectionLedgerBatchBytes-total {
			return errCollectionLedgerQuota
		}
		encoded[i] = raw
		total += int64(len(raw))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db != nil {
		return l.db.Update(func(tx *bolt.Tx) error {
			p, err := planCollectionExecutionBatchWithRetirement(&collectionLedgerView{tx: tx}, records, encoded, l.maxBytes, importing, offsets)
			if err != nil {
				return err
			}
			return installCollectionExecutionDisk(tx, p)
		})
	}
	v := &collectionLedgerView{executionRows: l.executionRows, executionStats: l.executionStats, executionOrder: l.executionOrder, executionBytes: l.executionBytes, bytes: l.bytes}
	p, err := planCollectionExecutionBatchWithRetirement(v, records, encoded, l.maxBytes, importing, offsets)
	if err != nil {
		return err
	}
	l.installCollectionExecutionMemory(p)
	return nil
}

func (l *collectionLedger) installCollectionExecutionMemory(p *collectionExecutionBatch) {
	for op, updates := range p.rows {
		if p.stats[op] == (collectionExecutionStats{}) {
			delete(l.executionRows, op)
			delete(l.executionOrder, op)
			delete(l.executionStats, op)
			continue
		}
		rows := l.executionRows[op]
		if rows == nil {
			rows = make(map[string][]byte)
		}
		order := l.executionOrder[op]
		if order == nil {
			order = btree.NewOrderedG[string](16)
		}
		for slot, raw := range updates {
			if raw == nil {
				order.Delete(slot)
			} else {
				order.ReplaceOrInsert(slot)
			}
		}
		l.executionOrder[op] = order
		for slot, raw := range updates {
			if raw == nil {
				delete(rows, slot)
			} else {
				rows[slot] = raw
			}
		}
		l.executionRows[op] = rows
		l.executionStats[op] = p.stats[op]
	}
	l.bytes, l.executionBytes = p.used, p.charged
}

func planCollectionExecutionBatch(v *collectionLedgerView, records []collectionExecutionRecord, encoded [][]byte, maxBytes int64, importing bool) (*collectionExecutionBatch, error) {
	return planCollectionExecutionBatchWithRetirement(v, records, encoded, maxBytes, importing, nil)
}

func planCollectionExecutionBatchWithRetirement(v *collectionLedgerView, records []collectionExecutionRecord, encoded [][]byte, maxBytes int64, importing bool, offsets map[string]uint64) (*collectionExecutionBatch, error) {
	used, total, err := v.executionTotals()
	if err != nil {
		return nil, err
	}
	p := &collectionExecutionBatch{rows: make(map[string]map[string][]byte), stats: make(map[string]collectionExecutionStats), used: used, charged: total}
	get := func(op, slot string) ([]byte, error) {
		if raw, exists := p.rows[op][slot]; exists {
			return raw, nil
		}
		return v.encodedExecution(op, slot)
	}
	for i, record := range records {
		op, slot, err := record.identity()
		if err != nil {
			return nil, err
		}
		stat, loaded := p.stats[op]
		if !loaded {
			stat, err = v.executionStat(op)
			if err != nil {
				return nil, err
			}
			p.rows[op] = make(map[string][]byte)
		}
		if stat.Binding.OperationID == "" {
			stat.Binding = collectionExecutionBinding(record)
			if importing {
				stat.RetiredOutcomes = offsets[op]
			}
		}
		if stat.Binding != collectionExecutionBinding(record) || stat.RetiredOutcomes > maxCollectionItems ||
			importing && stat.RetiredOutcomes != offsets[op] || !importing && stat.RetiredOutcomes != 0 {
			return nil, errCollectionLedgerConflict
		}
		previous, err := get(op, slot)
		if err != nil {
			return nil, err
		}
		if previous != nil {
			if !bytes.Equal(previous, encoded[i]) {
				return nil, errCollectionLedgerConflict
			}
			p.stats[op] = stat
			continue
		}
		before := stat.ChargedBytes
		switch {
		case record.Prepared != nil:
			if stat.Prepared || record.Prepared.Ordinal != stat.RetiredOutcomes+stat.Outcomes+1 {
				return nil, errCollectionLedgerConflict
			}
			stat.Prepared = true
		case record.Outcome != nil:
			o := record.Outcome
			if o.Ordinal != stat.RetiredOutcomes+stat.Outcomes+1 {
				return nil, errCollectionLedgerConflict
			}
			preparedRaw, err := get(op, "prepared")
			if err != nil {
				return nil, err
			}
			if stat.Prepared != (preparedRaw != nil) {
				return nil, errCollectionLedgerCorrupt
			}
			if preparedRaw != nil {
				if importing {
					return nil, errCollectionLedgerConflict
				}
				prepared, err := decodeCollectionExecutionRecord(preparedRaw)
				if err != nil || prepared.Prepared == nil {
					return nil, errCollectionLedgerCorrupt
				}
				if !prepared.Prepared.matchesOutcome(*o) {
					return nil, errCollectionLedgerConflict
				}
				p.rows[op]["prepared"] = nil
				stat.Prepared = false
				stat.EncodedBytes -= int64(len(preparedRaw))
				stat.ChargedBytes -= int64(len(preparedRaw))
			} else if !importing && o.PreparedID != "" {
				return nil, errCollectionLedgerConflict
			}
			stat.Outcomes++
			if o.Receipt != nil {
				stat.TerminalCapacity += collectionChildTerminalReserve
			}
		case record.Terminal != nil:
			t := record.Terminal
			if t.Ordinal <= stat.RetiredOutcomes || t.Ordinal > stat.RetiredOutcomes+stat.Outcomes {
				return nil, errCollectionLedgerConflict
			}
			outcomeSlot := collectionExecutionOutcomeSlot(t.Ordinal)
			raw, err := get(op, outcomeSlot)
			if err != nil {
				return nil, err
			}
			if raw == nil {
				return nil, errCollectionLedgerCorrupt
			}
			accepted, err := decodeCollectionExecutionRecord(raw)
			if err != nil || accepted.Outcome == nil {
				return nil, errCollectionLedgerCorrupt
			}
			if !t.matches(*accepted.Outcome) {
				return nil, errCollectionLedgerConflict
			}
			stat.Terminals++
			stat.TerminalBytes += int64(len(encoded[i]))
		}
		stat.EncodedBytes += int64(len(encoded[i]))
		stat.ChargedBytes += collectionExecutionCharge(record, encoded[i])
		delta := stat.ChargedBytes - before
		if delta > 0 && (p.used > maxBytes || delta > maxBytes-p.used) {
			return nil, errCollectionLedgerQuota
		}
		if err := stat.validate(op); err != nil {
			return nil, err
		}
		p.used += delta
		p.charged += delta
		p.rows[op][slot] = encoded[i]
		p.stats[op] = stat
	}
	return p, nil
}

func collectionExecutionOutcomeSlot(ordinal uint64) string {
	return fmt.Sprintf("outcome/%016x", ordinal)
}

func installCollectionExecutionDisk(tx *bolt.Tx, p *collectionExecutionBatch) error {
	rows, stats, meta := tx.Bucket(collectionLedgerExecution), tx.Bucket(collectionLedgerExecutionMeta), tx.Bucket(collectionLedgerMeta)
	if rows == nil || stats == nil || meta == nil {
		return errCollectionLedgerCorrupt
	}
	for op, updates := range p.rows {
		bucket := rows.Bucket([]byte(op))
		if p.stats[op] == (collectionExecutionStats{}) {
			if bucket == nil {
				return errCollectionLedgerCorrupt
			}
			if err := rows.DeleteBucket([]byte(op)); err != nil {
				return err
			}
			if err := stats.Delete([]byte(op)); err != nil {
				return err
			}
			continue
		}
		var err error
		if bucket == nil {
			bucket, err = rows.CreateBucket([]byte(op))
			if err != nil {
				return err
			}
		}
		for slot, raw := range updates {
			if raw == nil {
				err = bucket.Delete([]byte(slot))
			} else {
				err = bucket.Put([]byte(slot), raw)
			}
			if err != nil {
				return err
			}
		}
		s := p.stats[op]
		count := s.Outcomes + s.Terminals
		if s.Prepared {
			count++
		}
		if err = bucket.SetSequence(count); err != nil {
			return err
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if err = stats.Put([]byte(op), raw); err != nil {
			return err
		}
	}
	if err := meta.Put([]byte("execution_bytes"), collectionOrdinal(uint64(p.charged))); err != nil {
		return err
	}
	return meta.Put([]byte("bytes"), collectionOrdinal(uint64(p.used)))
}
