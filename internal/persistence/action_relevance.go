package persistence

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Membership stays until every older assessment has passed its action fence.
// Removing an entry immediately after a review could move an action behind an
// in-progress cursor and hide a hold that existed at the captured fence.
func relevantActionKey(row actionLedgerRow) string { return row.MonitorID + "\x00" + row.Action.ID }
func releasedActionKey(row actionLedgerRow) string {
	return fmt.Sprintf("%020d/", row.Sequence) + row.Action.ID
}
func reviewFenceKey(r reviewRecord) string { return fmt.Sprintf("%020d/", r.Fact.Fence) + r.Key }

func actionIndexSequence(key []byte) (uint64, error) {
	if len(key) <= 21 || key[20] != '/' {
		return 0, errActionLedger
	}
	sequence, err := strconv.ParseUint(string(key[:20]), 10, 64)
	if err != nil || fmt.Sprintf("%020d", sequence) != string(key[:20]) {
		return 0, errActionLedger
	}
	return sequence, nil
}
func reviewConditionKeys(r reviewRecord) []string {
	a := r.Alert
	if r.Deleted || a == nil || !a.ClosedAt.IsZero() && (a.ClosingStarted || !a.OpeningStarted && (a.CloseReason != "action_reviewed" || !a.RecoveredAt.IsZero())) {
		return nil
	}
	return []string{"monitor/" + identity(r.MonitorID) + "/" + r.Key, "action/" + identity(r.MonitorID) + "/" + a.ActionID + "/" + r.Key}
}
func putActionRelevance(tx *bolt.Tx, old actionLedgerRow, exists bool, row actionLedgerRow) error {
	if exists {
		if err := tx.Bucket(actionReleasedBucket).Delete([]byte(releasedActionKey(old))); err != nil {
			return err
		}
	}
	if row.Action.Kind != "intervention" || row.Action.State != Unknown {
		return nil
	}
	if err := tx.Bucket(actionRelevantBucket).Put([]byte(relevantActionKey(row)), []byte(row.Action.ID)); err != nil {
		return err
	}
	if !row.Action.Held() {
		return tx.Bucket(actionReleasedBucket).Put([]byte(releasedActionKey(row)), []byte(relevantActionKey(row)))
	}
	return nil
}
func (m *actionLedgerMemory) putActionRelevance(row actionLedgerRow) {
	if row.Action.Kind != "intervention" || row.Action.State != Unknown {
		return
	}
	m.byRelevant.ReplaceOrInsert(relevantActionKey(row))
	if !row.Action.Held() {
		m.byReleased.ReplaceOrInsert(releasedActionKey(row))
	}
}
func updateReviewIndexes(tx *bolt.Tx, r reviewRecord) error {
	if data := tx.Bucket(reviewRowsBucket).Get([]byte(r.Key)); data != nil {
		old, err := decodeReviewRecord(data)
		if err != nil {
			return err
		}
		if !old.Deleted && old.Fact != nil {
			if err := tx.Bucket(reviewFenceBucket).Delete([]byte(reviewFenceKey(old))); err != nil {
				return err
			}
		}
		for _, key := range reviewConditionKeys(old) {
			if err := tx.Bucket(reviewConditionBucket).Delete([]byte(key)); err != nil {
				return err
			}
		}
	}
	if !r.Deleted && r.Fact != nil {
		if err := tx.Bucket(reviewFenceBucket).Put([]byte(reviewFenceKey(r)), []byte(r.Key)); err != nil {
			return err
		}
	}
	for _, key := range reviewConditionKeys(r) {
		if err := tx.Bucket(reviewConditionBucket).Put([]byte(key), []byte(r.Key)); err != nil {
			return err
		}
	}
	return nil
}
func (m *actionLedgerMemory) updateReviewIndexes(r reviewRecord) {
	if data := m.reviews[r.Key]; data != nil {
		old, _ := decodeReviewRecord(data)
		if !old.Deleted && old.Fact != nil {
			m.reviewFences.Delete(reviewFenceKey(old))
		}
		for _, key := range reviewConditionKeys(old) {
			m.reviewConditions.Delete(key)
		}
	}
	if !r.Deleted && r.Fact != nil {
		m.reviewFences.ReplaceOrInsert(reviewFenceKey(r))
	}
	for _, key := range reviewConditionKeys(r) {
		m.reviewConditions.ReplaceOrInsert(key)
	}
}

// The released index is ordered by the commit that ended a hold. Pruning is
// bounded and touches only obsolete index entries, never the evidence rows.
func pruneActionRelevance(tx *bolt.Tx, m *actionLedgerMemory, limit int) error {
	upper := uint64(math.MaxUint64)
	if tx != nil {
		if key, _ := tx.Bucket(reviewFenceBucket).Cursor().First(); key != nil {
			var err error
			upper, err = actionIndexSequence(key)
			if err != nil {
				return err
			}
		}
		c := tx.Bucket(actionReleasedBucket).Cursor()
		for key, relevant := c.First(); key != nil && limit > 0; key, relevant = c.Next() {
			sequence, err := actionIndexSequence(key)
			if err != nil {
				return err
			}
			if sequence > upper {
				break
			}
			row, err := decodeActionRow(tx.Bucket(actionCurrentBucket).Get(key[21:]))
			if err != nil || row.Action.Held() || releasedActionKey(row) != string(key) || relevantActionKey(row) != string(relevant) {
				return errActionLedger
			}
			if err := tx.Bucket(actionRelevantBucket).Delete(relevant); err != nil {
				return err
			}
			if err := c.Delete(); err != nil {
				return err
			}
			limit--
		}
		return nil
	}
	if key, ok := m.reviewFences.Min(); ok {
		var err error
		upper, err = actionIndexSequence([]byte(key))
		if err != nil {
			return err
		}
	}
	var keys []string
	var scanErr error
	m.byReleased.Ascend(func(key string) bool {
		sequence, err := actionIndexSequence([]byte(key))
		if err != nil {
			scanErr = err
			return false
		}
		if sequence > upper || len(keys) == limit {
			return false
		}
		keys = append(keys, key)
		return true
	})
	if scanErr != nil {
		return scanErr
	}
	for _, key := range keys {
		row, err := decodeActionRow(m.current[key[21:]])
		if err != nil {
			return err
		}
		m.byRelevant.Delete(relevantActionKey(row))
		m.byReleased.Delete(key)
	}
	return nil
}

// relevantPage selects only unknown interventions and the small set of former
// holds still visible to an older fact. It reads their actual version at the
// captured fence; a new hold cannot retroactively enter an older assessment.
func (v *actionLedgerPin) relevantPage(ctx context.Context, monitor, after string, visits, maxBytes int) ([]actionLedgerRow, string, error) {
	var rows []actionLedgerRow
	next, decoded, visited := "", 0, 0
	err := v.read(ctx, func(tx *bolt.Tx, m *actionLedgerMemory) error {
		prefix := monitor + "\x00"
		visit := func(key string) (bool, error) {
			if !strings.HasPrefix(key, prefix) {
				return false, nil
			}
			id := strings.TrimPrefix(key, prefix)
			if id <= after {
				return true, nil
			}
			if visited >= visits || visited > 0 && decoded >= maxBytes {
				return false, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			visited++
			next = id
			row, found, err := readActionVersionDecoded(tx, m, id, v.sequence, func(data []byte) (actionLedgerRow, error) { decoded += len(data); return decodeActionRow(data) })
			if err != nil {
				return false, err
			}
			if found && row.MonitorID != monitor {
				return false, errActionLedger
			}
			if found && row.Action.Kind == "intervention" && row.Action.Held() {
				rows = append(rows, row)
				return false, nil
			}
			return true, nil
		}
		exhausted := true
		if tx != nil {
			b := tx.Bucket(actionRelevantBucket)
			if b == nil {
				if v.sequence == 0 {
					return nil
				}
				return errActionLedger
			}
			c := b.Cursor()
			for key, _ := c.Seek([]byte(prefix + after)); key != nil; key, _ = c.Next() {
				more, err := visit(string(key))
				if err != nil {
					return err
				}
				if !more {
					exhausted = !strings.HasPrefix(string(key), prefix)
					break
				}
			}
		} else if m != nil {
			var err error
			m.byRelevant.AscendGreaterOrEqual(prefix+after, func(key string) bool {
				more, e := visit(key)
				err = e
				if !more {
					exhausted = !strings.HasPrefix(key, prefix)
				}
				return more && err == nil
			})
			if err != nil {
				return err
			}
		}
		if exhausted && len(rows) == 0 {
			next = ""
		}
		return nil
	})
	return rows, next, err
}

func (l *collectionLedger) reviewConditions(monitor, action, after string) ([]reviewRecord, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return nil, errCollectionLedgerClosed
	}
	prefix := "monitor/" + identity(monitor) + "/"
	if action != "" {
		prefix = "action/" + identity(monitor) + "/" + action + "/"
	}
	var rows []reviewRecord
	visit := func(key string, data []byte) (bool, error) {
		if !strings.HasPrefix(key, prefix) {
			return false, nil
		}
		name := strings.TrimPrefix(key, prefix)
		if name <= after {
			return true, nil
		}
		r, err := decodeReviewRecord(data)
		if err != nil || r.Key != name || r.Alert == nil || r.Deleted {
			return false, errActionLedger
		}
		rows = append(rows, r)
		return false, nil
	}
	if l.db != nil {
		err := l.db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket(reviewConditionBucket)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			for k, v := c.Seek([]byte(prefix + after)); k != nil; k, v = c.Next() {
				more, err := visit(string(k), tx.Bucket(reviewRowsBucket).Get(v))
				if err != nil {
					return err
				}
				if !more {
					break
				}
			}
			return nil
		})
		return rows, err
	}
	if l.actions == nil {
		return nil, nil
	}
	var err error
	l.actions.reviewConditions.AscendGreaterOrEqual(prefix+after, func(key string) bool {
		more, e := visit(key, l.actions.reviews[strings.TrimPrefix(key, prefix)])
		err = e
		return more && err == nil
	})
	return rows, err
}

func validateRelevanceIndexes(tx *bolt.Tx) error {
	for _, bucket := range [][]byte{actionRelevantBucket, actionReleasedBucket, reviewFenceBucket, reviewConditionBucket} {
		if tx.Bucket(bucket) == nil {
			return errActionLedger
		}
	}
	oldest := uint64(math.MaxUint64)
	if key, _ := tx.Bucket(reviewFenceBucket).Cursor().First(); key != nil {
		var err error
		oldest, err = actionIndexSequence(key)
		if err != nil {
			return err
		}
	}
	if err := tx.Bucket(actionRelevantBucket).ForEach(func(key, id []byte) error {
		row, err := decodeActionRow(tx.Bucket(actionCurrentBucket).Get(id))
		if err != nil || row.Action.Kind != "intervention" || row.Action.State != Unknown || string(key) != relevantActionKey(row) || string(id) != row.Action.ID {
			return errActionLedger
		}
		if !row.Action.Held() && tx.Bucket(actionReleasedBucket).Get([]byte(releasedActionKey(row))) == nil {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	if err := tx.Bucket(actionReleasedBucket).ForEach(func(key, relevant []byte) error {
		id := tx.Bucket(actionRelevantBucket).Get(relevant)
		row, err := decodeActionRow(tx.Bucket(actionCurrentBucket).Get(id))
		if err != nil || row.Action.Held() || string(key) != releasedActionKey(row) || string(relevant) != relevantActionKey(row) {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	if err := tx.Bucket(reviewFenceBucket).ForEach(func(key, name []byte) error {
		r, err := decodeReviewRecord(tx.Bucket(reviewRowsBucket).Get(name))
		if err != nil || r.Deleted || r.Fact == nil || string(key) != reviewFenceKey(r) || r.Key != string(name) {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	if err := tx.Bucket(reviewConditionBucket).ForEach(func(key, name []byte) error {
		r, err := decodeReviewRecord(tx.Bucket(reviewRowsBucket).Get(name))
		if err != nil || r.Key != string(name) {
			return errActionLedger
		}
		for _, candidate := range reviewConditionKeys(r) {
			if candidate == string(key) {
				return nil
			}
		}
		return errActionLedger
	}); err != nil {
		return err
	}
	if err := tx.Bucket(actionCurrentBucket).ForEach(func(_ []byte, data []byte) error {
		row, err := decodeActionRow(data)
		if err != nil {
			return err
		}
		if row.Action.Kind == "intervention" && row.Action.State == Unknown && (row.Action.Held() || row.Sequence > oldest) && tx.Bucket(actionRelevantBucket).Get([]byte(relevantActionKey(row))) == nil {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	return tx.Bucket(reviewRowsBucket).ForEach(func(_ []byte, data []byte) error {
		r, err := decodeReviewRecord(data)
		if err != nil {
			return err
		}
		if !r.Deleted && r.Fact != nil && tx.Bucket(reviewFenceBucket).Get([]byte(reviewFenceKey(r))) == nil {
			return errActionLedger
		}
		for _, key := range reviewConditionKeys(r) {
			if string(tx.Bucket(reviewConditionBucket).Get([]byte(key))) != r.Key {
				return errActionLedger
			}
		}
		return nil
	})
}
