package persistence

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const actionRetention = 90 * 24 * time.Hour

var errActionRetirementChanged = errors.New("action retirement dependencies changed")

type ActionRetirementItem struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}
type ActionRetirementCommand struct {
	Cutoff time.Time              `json:"cutoff"`
	Items  []ActionRetirementItem `json:"items"`
}

func validateActionRetirementCommand(r *ActionRetirementCommand, at time.Time) error {
	if r == nil || len(r.Items) == 0 || len(r.Items) > 64 || at.IsZero() || r.Cutoff.IsZero() || r.Cutoff.Location() != time.UTC || r.Cutoff.After(at.Add(-actionRetention)) {
		return errActionLedger
	}
	previous := ""
	for _, item := range r.Items {
		if !catalogIdentifier(item.ID, 256) || item.Sequence == 0 || item.ID <= previous {
			return errActionLedger
		}
		previous = item.ID
	}
	return nil
}

// A terminal provider fact alone is insufficient: every live execution,
// condition, assessment and receipt dependency is checked again at commit.
func ordinaryTerminalAction(a Action) bool {
	if a.Review != nil || a.LateEvidence != nil || a.ConflictingEvidence != nil || a.ReviewNotice != nil || a.FinishedAt.IsZero() {
		return false
	}
	switch a.State {
	case Succeeded:
		return true
	case Failed:
		return a.Outcome == "rejected" || a.TerminationReason == "permanent_rejection" || a.StartedAt.IsZero() && a.TerminationReason == "execution_deadline"
	case Cancelled:
		return a.StartedAt.IsZero()
	default:
		return false
	}
}
func terminalActionKey(row actionLedgerRow) string {
	if row.Deleted || !ordinaryTerminalAction(row.Action) {
		return ""
	}
	return row.Action.FinishedAt.UTC().Format("20060102150405.000000000") + "\x00" + row.Action.ID
}
func resolutionRetainsAction(v NotificationResolution) bool {
	if v.MigrationReason != "" {
		return true
	}
	if v.State == Succeeded || v.State == Failed || v.State == Unknown {
		return false
	}
	if v.State != Cancelled {
		return true
	}
	if v.Started || !v.ExecutionDeadline.IsZero() {
		return false
	}
	switch v.Reason {
	case "maintenance", "monitor_ignored", "snooze", "dismiss", "notification_superseded", "worker_notification_changed", "worker_not_started", "suppressed":
		return true
	}
	return false
}
func (f *machine) actionRetirementBlocked(m Monitor, a Action) (bool, error) {
	if a.NotificationPermit != nil && a.NotificationFeedbackDigest == "" && a.ExecutorSession == f.image.LocalExecutorSession {
		return true, nil
	}
	if m.ActionProjection == nil || !f.projectionsEnabled() {
		return true, nil
	}
	if _, ok := m.Actions[a.ID]; ok {
		return true, nil
	}
	p := m.ActionProjection
	if p.LatestRecovery != nil && p.LatestRecovery.ID == a.ID || a.State == Failed && a.FinishedAt.Equal(p.LastFailure) {
		return true, nil
	}
	for _, current := range m.Actions {
		if a.DeliveryID != "" && current.DeliveryID == a.DeliveryID {
			return true, nil
		}
		if current.RecoveryFailure != nil && current.RecoveryFailure.ActionID == a.ID {
			return true, nil
		}
	}
	if m.notificationRetainsAction(a.ID) {
		return true, nil
	}
	if m.Notifications != nil && a.Kind == "code" && a.Color == "green" {
		for _, v := range m.Notifications.Resolutions {
			// The summary chooses one deterministic head, but contradictory
			// historical deliveries are all evidence for its migration hold.
			// Match frozen identities, not the monitor's current configuration.
			if v.MigrationReason == "migration_delivery_evidence_conflict" && a.Revision == v.Revision &&
				a.CatalogUID == v.CatalogUID && a.FileIncarnation == v.FileIncarnation &&
				a.NotificationCorrelation == v.Correlation && a.EndpointID == v.EndpointID {
				return true, nil
			}
		}
	}
	// These receipts are bounded pending operations; terminal receipts already
	// moved to history are self-contained and do not reacquire execution rights.
	for _, receipt := range f.image.Operations {
		if receipt.ActionID == a.ID {
			return true, nil
		}
	}
	if workerRetainsAction(f.image, a.ID) {
		return true, nil
	}
	facts, err := f.collections.reviewPage(reviewPrefix("fact", m.ID), "", 1)
	if err != nil {
		return true, err
	}
	if len(facts) != 0 {
		return true, nil
	}
	for _, r := range f.reviewWrites {
		if r.MonitorID == m.ID && !r.Deleted && r.Fact != nil {
			return true, nil
		}
	}
	return false, nil
}
func (f *machine) applyActionRetirement(r *ActionRetirementCommand, at time.Time) Result {
	if err := validateActionRetirementCommand(r, at); err != nil {
		return Result{Err: err}
	}
	if !notificationConditionStorageFormat(f.image.Version) || f.collections == nil || f.image.ActionStorage == nil {
		return Result{Err: errActionRetirementChanged}
	}
	pin, err := f.collections.pinActions()
	if err != nil {
		f.err = err
		return Result{Err: err}
	}
	defer pin.Close()
	rows := make([]actionLedgerRow, 0, len(r.Items))
	var retiredBytes uint64
	for _, item := range r.Items {
		row, found, err := pin.Get(context.Background(), item.ID)
		if err != nil {
			f.err = err
			return Result{Err: err}
		}
		if !found || row.Sequence != item.Sequence || !ordinaryTerminalAction(row.Action) || row.Action.FinishedAt.After(r.Cutoff) {
			continue
		}
		m, exists := f.image.Monitors[row.MonitorID]
		if !exists {
			return Result{Err: errActionLedger}
		}
		blocked, err := f.actionRetirementBlocked(m, row.Action)
		if err != nil {
			f.err = err
			return Result{Err: err}
		}
		if blocked {
			continue
		}
		data, err := encodeActionRow(row)
		if err != nil {
			return Result{Err: err}
		}
		retiredBytes += uint64(len(data))
		rows = append(rows, row)
	}
	if uint64(len(rows)) > math.MaxUint64-f.image.ActionStorage.Retired || retiredBytes > math.MaxUint64-f.image.ActionStorage.RetiredBytes {
		return Result{Err: errActionLedger}
	}
	for _, row := range rows {
		m := f.image.Monitors[row.MonitorID].Clone()
		if m.OwnsAction(row.Action) && row.Action.Kind == "code" {
			m.ActionProjection.Notifications.adjust(row.Action.State, -1)
		}
		f.image.Monitors[m.ID] = m
		f.publishControlChange(m)
		if f.actionWrites == nil {
			f.actionWrites = make(map[string]actionLedgerRow)
		}
		f.actionWrites[row.Action.ID] = actionLedgerRow{MonitorID: row.MonitorID, Action: Action{ID: row.Action.ID}, Deleted: true}
	}
	f.image.ActionStorage.Retired += uint64(len(rows))
	f.image.ActionStorage.RetiredBytes += retiredBytes
	return Result{Allowed: true}
}

func (v *actionLedgerPin) terminalPage(ctx context.Context, cutoff time.Time, after string) ([]actionLedgerRow, string, error) {
	var rows []actionLedgerRow
	next := ""
	upper := cutoff.UTC().Format("20060102150405.000000000") + "\x00\xff"
	err := v.read(ctx, func(tx *bolt.Tx, m *actionLedgerMemory) error {
		visit := func(key string) (bool, error) {
			if key <= after {
				return true, nil
			}
			if key > upper {
				return false, nil
			}
			if len(rows) == 64 {
				return false, nil
			}
			separator := strings.LastIndexByte(key, 0)
			if separator < 0 {
				return false, errActionLedger
			}
			row, found, err := readActionVersion(tx, m, key[separator+1:], v.sequence)
			if err != nil || !found || terminalActionKey(row) != key {
				return false, errActionLedger
			}
			rows = append(rows, row)
			next = key
			return true, ctx.Err()
		}
		if tx != nil {
			b := tx.Bucket(actionTerminalBucket)
			if b == nil {
				if v.sequence == 0 {
					return nil
				}
				return errActionLedger
			}
			c := b.Cursor()
			for key, _ := c.Seek([]byte(after)); key != nil; key, _ = c.Next() {
				more, err := visit(string(key))
				if err != nil {
					return err
				}
				if !more {
					break
				}
			}
			return nil
		}
		if m == nil {
			return nil
		}
		var err error
		m.byTerminal.AscendGreaterOrEqual(after, func(key string) bool { more, e := visit(key); err = e; return more && err == nil })
		return err
	})
	return rows, next, err
}

type actionRetirementSelection struct {
	command      *ActionRetirementCommand
	after        string
	blocked      uint64
	blockedBytes uint64
}

// Maintenance selects only a bounded terminal-time page. Read-only dependency
// filtering avoids no-op Raft writes for protected rows. The replicated command
// still verifies the exact heads and all dependencies before retiring anything.
func (s *Store) actionRetirementCandidates(at time.Time, after string) (actionRetirementSelection, error) {
	var selection actionRetirementSelection
	s.fsm.mu.RLock()
	f := s.fsm
	if f.collections == nil || !notificationConditionStorageFormat(f.image.Version) || !f.projectionsEnabled() {
		f.mu.RUnlock()
		return selection, nil
	}
	pin, err := f.collections.pinActions()
	f.mu.RUnlock()
	if err != nil {
		return selection, err
	}
	defer pin.Close()
	cutoff := at.UTC().Add(-actionRetention)
	rows, next, err := pin.terminalPage(s.maintenanceContext, cutoff, after)
	if err != nil {
		return selection, err
	}
	selection.after = next
	r := &ActionRetirementCommand{Cutoff: cutoff}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, row := range rows {
		m, exists := f.image.Monitors[row.MonitorID]
		if !exists {
			return selection, errActionLedger
		}
		blocked, err := f.actionRetirementBlocked(m, row.Action)
		if err != nil {
			return selection, err
		}
		if blocked {
			data, err := encodeActionRow(row)
			if err != nil {
				return selection, err
			}
			selection.blocked++
			selection.blockedBytes += uint64(len(data))
			continue
		}
		r.Items = append(r.Items, ActionRetirementItem{ID: row.Action.ID, Sequence: row.Sequence})
	}
	slices.SortFunc(r.Items, func(a, b ActionRetirementItem) int { return strings.Compare(a.ID, b.ID) })
	if len(r.Items) == 0 {
		return selection, nil
	}
	selection.command = r
	return selection, nil
}

// Used at the publication boundary: immutable collection envelopes and action
// progress identify every ledger namespace copied into the replacement.
func sameLedgerState(a, b image) bool {
	return a.Version == b.Version && reflect.DeepEqual(a.ActionStorage, b.ActionStorage) && reflect.DeepEqual(a.Collections, b.Collections)
}
