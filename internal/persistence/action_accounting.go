package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bytes are encoded current action rows, not allocated bbolt pages. Generation
// storage accounting separately reports apparent and physical file allocation.
type ActionStorageAccounting struct {
	ObservedAt          time.Time `json:"observed_at,omitzero"`
	Active              uint64    `json:"active"`
	OrdinaryRetained    uint64    `json:"ordinary_retained"`
	Protected           uint64    `json:"protected"`
	Tombstones          uint64    `json:"tombstones"`
	ActiveBytes         uint64    `json:"active_bytes"`
	OrdinaryBytes       uint64    `json:"ordinary_bytes"`
	ProtectedBytes      uint64    `json:"protected_bytes"`
	TombstoneBytes      uint64    `json:"tombstone_bytes"`
	Retired             uint64    `json:"retired"`
	RetiredBytes        uint64    `json:"retired_bytes"`
	CleanupBlocked      uint64    `json:"cleanup_blocked"`
	CleanupBlockedBytes uint64    `json:"cleanup_blocked_bytes"`
	CleanupCoverage     string    `json:"cleanup_coverage"`
	CompactionCondition string    `json:"compaction_condition,omitempty"`
}

func (s *ActionStorageAccounting) adjust(row actionLedgerRow, add bool) error {
	data, err := encodeActionRow(row)
	if err != nil {
		return err
	}
	count, size := &s.Protected, &s.ProtectedBytes
	if row.Deleted {
		count, size = &s.Tombstones, &s.TombstoneBytes
	} else if row.Action.State == Queued || row.Action.State == Started {
		count, size = &s.Active, &s.ActiveBytes
	} else if ordinaryTerminalAction(row.Action) {
		count, size = &s.OrdinaryRetained, &s.OrdinaryBytes
	}
	if add {
		if *count == math.MaxUint64 || uint64(len(data)) > math.MaxUint64-*size {
			return errActionLedger
		}
		*count++
		*size += uint64(len(data))
	} else {
		if *count == 0 || *size < uint64(len(data)) {
			return errActionLedger
		}
		*count--
		*size -= uint64(len(data))
	}
	return nil
}
func actionDiskAccounting(tx *bolt.Tx) (ActionStorageAccounting, error) {
	var state ActionStorageAccounting
	if tx.Bucket(actionMetaBucket) == nil {
		return state, nil
	}
	data := tx.Bucket(actionMetaBucket).Get([]byte("accounting"))
	if data == nil {
		if current := tx.Bucket(actionCurrentBucket); current != nil {
			if key, _ := current.Cursor().First(); key != nil {
				return state, errActionLedger
			}
		}
		return state, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil {
		return state, errActionLedger
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(data, canonical) || !state.ObservedAt.IsZero() || state.Retired != 0 || state.RetiredBytes != 0 || state.CleanupBlocked != 0 || state.CleanupBlockedBytes != 0 || state.CleanupCoverage != "" || state.CompactionCondition != "" {
		return state, errActionLedger
	}
	return state, nil
}
func updateActionAccounting(tx *bolt.Tx, old actionLedgerRow, exists bool, row actionLedgerRow) error {
	state, err := actionDiskAccounting(tx)
	if err != nil {
		return err
	}
	if exists {
		if err := state.adjust(old, false); err != nil {
			return err
		}
	}
	if err := state.adjust(row, true); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return tx.Bucket(actionMetaBucket).Put([]byte("accounting"), data)
}
func (l *collectionLedger) actionAccounting() (ActionStorageAccounting, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return ActionStorageAccounting{}, errCollectionLedgerClosed
	}
	if l.db == nil {
		if l.actions == nil {
			return ActionStorageAccounting{}, nil
		}
		return l.actions.accounting, nil
	}
	var state ActionStorageAccounting
	err := l.db.View(func(tx *bolt.Tx) error { var err error; state, err = actionDiskAccounting(tx); return err })
	return state, err
}
func (s *Store) sampleActionAccounting(blocked, blockedBytes uint64, condition string) error {
	s.fsm.mu.RLock()
	ledger := s.fsm.collections
	if ledger == nil {
		s.fsm.mu.RUnlock()
		return nil
	}
	pin, err := ledger.pinActions()
	var retired, retiredBytes uint64
	if s.fsm.image.ActionStorage != nil {
		retired, retiredBytes = s.fsm.image.ActionStorage.Retired, s.fsm.image.ActionStorage.RetiredBytes
	}
	s.fsm.mu.RUnlock()
	if err != nil {
		return err
	}
	defer pin.Close()
	state, err := ledger.actionAccounting()
	if err != nil {
		if errors.Is(err, errCollectionLedgerClosed) {
			return nil
		}
		return err
	}
	state.Retired, state.RetiredBytes = retired, retiredBytes
	state.CleanupBlocked, state.CleanupBlockedBytes, state.CleanupCoverage = blocked, blockedBytes, "last_selection_page"
	state.CompactionCondition = condition
	state.ObservedAt = time.Now().UTC()
	s.actionAccounting.Store(&state)
	return nil
}
