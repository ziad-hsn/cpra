package persistence

import (
	"context"
	"errors"
	"maps"
	"math"
	"reflect"
	"slices"
)

// withAction prepares one exact command dependency without changing the live
// projection. Existing map entries and provider facts remain immutable.
func withAction(m Monitor, a Action) Monitor {
	if _, present := m.Actions[a.ID]; present {
		return m
	}
	m.Actions = maps.Clone(m.Actions)
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[a.ID] = a.Clone()
	return m
}

// actionForMonitor is used by committed commands that address exact retained
// evidence. It does not hydrate history into the live monitor projection.
// The caller holds FSM.mu exclusively: failed ledger reads stop admission.
func (f *machine) actionForMonitor(m Monitor, id string) (Action, bool, error) {
	if a, ok := m.Actions[id]; ok {
		return a, true, nil
	}
	if row, ok := f.actionWrites[id]; ok {
		return row.Action, row.MonitorID == m.ID, nil
	}
	if f.image.ActionStorage == nil || f.image.ActionStorage.Sequence == 0 {
		return Action{}, false, nil
	}
	if f.collections == nil {
		f.err = errActionLedger
		return Action{}, false, f.err
	}
	view, err := f.collections.pinActions()
	if err != nil {
		f.err = err
		return Action{}, false, err
	}
	defer view.Close()
	row, found, err := view.Get(context.Background(), id)
	if err != nil {
		f.err = err
	}
	return row.Action, found && row.MonitorID == m.ID, err
}

// actionStorageState records completion of the committed migration separately
// from the action-policy format. A snapshot can discard old Raft entries only
// when its frozen ledger has reached this exact materialization boundary.
type actionStorageState struct {
	Retired      uint64               `json:"retired,omitempty"`
	RetiredBytes uint64               `json:"retired_bytes,omitempty"`
	Sequence     uint64               `json:"sequence"`
	Position     actionLedgerPosition `json:"position"`
}

func (f *machine) needsActionStorage(commands []Command) bool {
	if len(f.image.Monitors) != 0 {
		return true
	}
	for _, command := range commands {
		if command.Kind == "configure" || command.Kind == "action_project" {
			return true
		}
	}
	return false
}

func (f *machine) migrateActionStorage(index uint64) error {
	if f.image.ActionStorage != nil {
		return nil
	}
	f.actionPosition = actionLedgerPosition{Index: index}
	f.actionWrites = make(map[string]actionLedgerRow)
	ids := make([]string, 0, len(f.image.Monitors))
	for id := range f.image.Monitors {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// The old image already owns the retained actions. Migration writes bounded
	// batches in deterministic order and never invokes or grants execution.
	bytes := 0
	for _, id := range ids {
		m := f.image.Monitors[id]
		for _, actionID := range sortedActions(m.Actions) {
			row := actionLedgerRow{Sequence: 1, MonitorID: id, Action: m.Actions[actionID]}
			encoded, err := encodeActionRow(row)
			if err != nil {
				return err
			}
			if len(f.actionWrites) == collectionLedgerBatchLimit || bytes+len(encoded) > collectionLedgerBatchBytes {
				if err := f.flushActionWrites(); err != nil {
					return err
				}
				if f.actionPosition.Ordinal == math.MaxUint32 {
					return errActionLedger
				}
				f.actionPosition.Ordinal++
				bytes = 0
			}
			f.actionWrites[actionID] = row
			bytes += len(encoded)
		}
	}
	if err := f.flushActionWrites(); err != nil {
		return err
	}
	if f.image.ActionStorage == nil {
		f.image.ActionStorage = &actionStorageState{}
	}
	if f.actionPosition.Ordinal == math.MaxUint32 {
		return errActionLedger
	}
	f.actionPosition.Ordinal++
	return nil
}

func (f *machine) recordActionWrites(previous, current Monitor) {
	if f.image.ActionStorage == nil {
		return
	}
	if f.actionWrites == nil {
		f.actionWrites = make(map[string]actionLedgerRow)
	}
	for id, action := range current.retainedActions {
		f.actionWrites[id] = actionLedgerRow{MonitorID: current.ID, Action: action}
	}
	for id, action := range current.Actions {
		if old, ok := previous.Actions[id]; ok && reflect.DeepEqual(old, action) {
			continue
		}
		f.actionWrites[id] = actionLedgerRow{MonitorID: current.ID, Action: action}
	}
}

// retireAction removes an execution reference while preserving the last facts
// produced by this transition. In particular, a retry has a different action
// identity and cannot stand in for the completed attempt it replaces.
func (m *Monitor) retireAction(id string) {
	if action, ok := m.Actions[id]; ok && actionStorageFormat(m.ActionVersion) {
		if m.retainedActions == nil {
			m.retainedActions = make(map[string]Action)
		}
		m.retainedActions[id] = action.Clone()
	}
	delete(m.Actions, id)
}

func (f *machine) flushActionWrites() error {
	if f.err != nil {
		return f.err
	}
	if len(f.actionWrites)+len(f.reviewWrites) == 0 {
		return nil
	}
	if err := f.ensureCollectionLedger(); err != nil {
		return err
	}
	rows := make([]actionLedgerRow, 0, len(f.actionWrites))
	for _, row := range f.actionWrites {
		rows = append(rows, row)
	}
	reviews := make([]reviewRecord, 0, len(f.reviewWrites))
	for _, r := range f.reviewWrites {
		reviews = append(reviews, r)
	}
	sequence, err := f.collections.CommitActionState(f.actionPosition, rows, reviews)
	if err != nil {
		f.err = errors.Join(errors.New("durable action commit failed"), err)
		return f.err
	}
	retired, retiredBytes := uint64(0), uint64(0)
	if f.image.ActionStorage != nil {
		retired, retiredBytes = f.image.ActionStorage.Retired, f.image.ActionStorage.RetiredBytes
	}
	f.image.ActionStorage = &actionStorageState{Sequence: sequence, Position: f.actionPosition, Retired: retired, RetiredBytes: retiredBytes}
	clear(f.actionWrites)
	clear(f.reviewWrites)
	return nil
}

func validateActionStorageHeader(i image) error {
	if i.InstallationID != "" && (!actionStorageFormat(i.Version) || !catalogIdentifier(i.InstallationID, 256)) {
		return errInstallationIdentity
	}
	s := i.ActionStorage
	if s == nil {
		return nil
	}
	if (s.Retired != 0 || s.RetiredBytes != 0) && !notificationConditionStorageFormat(i.Version) {
		return errActionLedger
	}
	if (s.Retired == 0) != (s.RetiredBytes == 0) || s.Retired != 0 && s.Sequence == 0 {
		return errActionLedger
	}
	if !actionStorageFormat(i.Version) || s.Position.Index > i.Index || s.Sequence == 0 && s.Position != (actionLedgerPosition{}) || s.Sequence != 0 && s.Position.Index == 0 {
		return errActionLedger
	}
	return nil
}

func validateActionStorageRows(i image, view *collectionLedgerView) error {
	if err := validateActionStorageHeader(i); err != nil {
		return err
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	progress, err := view.actionProgress()
	if err != nil {
		return err
	}
	if i.ActionStorage == nil {
		if progress.Sequence != 0 {
			return errActionLedger
		}
		return nil
	}
	if progress.Sequence != i.ActionStorage.Sequence || progress.Position != i.ActionStorage.Position {
		return errActionLedger
	}
	worker, err := newWorkerActionValidation(i)
	if err != nil {
		return err
	}
	if progress.Sequence != 0 {
		if err := view.validateActionInventory(progress); err != nil {
			return err
		}
	}
	for _, monitor := range i.Monitors {
		for id, action := range monitor.Actions {
			var data []byte
			if view.tx != nil {
				bucket := view.tx.Bucket(actionCurrentBucket)
				if bucket == nil {
					return errActionLedger
				}
				data = bucket.Get([]byte(id))
			} else if view.actions != nil {
				data = view.actions.current[id]
			}
			row, err := decodeActionRow(data)
			if err != nil || row.MonitorID != monitor.ID || !reflect.DeepEqual(row.Action, action) {
				return errActionLedger
			}
		}
	}
	read := func(m Monitor, id string) (Action, bool) {
		var data []byte
		if view.tx != nil {
			data = view.tx.Bucket(actionCurrentBucket).Get([]byte(id))
		} else if view.actions != nil {
			data = view.actions.current[id]
		}
		row, err := decodeActionRow(data)
		return row.Action, err == nil && !row.Deleted && row.MonitorID == m.ID
	}
	for _, receipt := range i.Operations {
		if deferredActionReceipt(i, receipt) && !operationCurrentWithAction(i, receipt, read) {
			return errActionLedger
		}
	}
	if err := validateWorkerActionDependencies(i, view); err != nil {
		return err
	}
	if err := validateActionProjectionMigration(i, view); err != nil {
		return err
	}
	if err := validateProjectionInventory(i, view, worker); err != nil {
		return err
	}
	reservations := notificationReservationInventory{}
	if err := view.walkActions(func(_ []byte, row actionLedgerRow) error {
		monitor, exists := i.Monitors[row.MonitorID]
		if row.Deleted {
			if !notificationConditionStorageFormat(i.Version) || !exists {
				return errActionLedger
			}
			if _, live := monitor.Actions[row.Action.ID]; live {
				return errActionLedger
			}
			return nil
		}
		if !exists || row.Sequence > progress.Sequence {
			return errActionLedger
		}
		if current, exists := monitor.Actions[row.Action.ID]; exists && !reflect.DeepEqual(current, row.Action) {
			return errActionLedger
		}
		_, current := monitor.Actions[row.Action.ID]
		if row.Action.ReviewNotice != nil && monitor.ActionProjection != nil {
			r, ok, err := view.readReviewHead(reviewAlertKey(monitor.ID, row.Action.ReviewNotice.AlertID))
			if err != nil || !ok || r.Alert == nil {
				return errActionLedger
			}
			monitor, err = view.reviewValidationMonitor(monitor, *r.Alert)
			if err != nil {
				return err
			}
		}
		if err := validateRetainedAction(i, monitor, row.Action, !current, worker); err != nil {
			return err
		}
		return reservations.add(i, row.Action)
	}); err != nil {
		return err
	}
	return reservations.validate(i)
}

func validateRetainedAction(i image, monitor Monitor, a Action, archived bool, worker workerActionValidation) error {
	if err := validateNotificationActionLimits(a, i.Version); err != nil {
		return err
	}
	if err := validateWorkerRetainedAction(i, a); err != nil {
		return err
	}
	if a.Revision == "" || (a.Kind != "code" && a.Kind != "intervention") || a.Attempt < 1 ||
		archived && (a.State == Queued || a.State == Started) {
		return errActionLedger
	}
	switch a.State {
	case Queued, Started, Succeeded, Failed, Cancelled, Unknown:
	default:
		return errActionLedger
	}
	if err := validateActionEvidence(i, a, archived, worker); err != nil {
		return err
	}
	if err := validateActionIncarnation(monitor, a); err != nil {
		return err
	}
	if err := validateNotificationAction(a, i.Version, DeliveryFormatVersion); err != nil {
		return err
	}
	if err := validateReviewNotice(a, i.Version); err != nil {
		return err
	}
	if a.ReviewNotice != nil {
		if monitor.Notifications == nil {
			return errActionLedger
		}
		alert, exists := monitor.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]
		if !exists || alert.ActionID != a.ReviewNotice.ActionID || a.ReviewNotice.Phase == "close" && a.ReviewBinding != alert.Binding {
			return errActionLedger
		}
	}
	if a.Retryable && (a.State != Failed || a.FinishedAt.IsZero()) {
		return errActionLedger
	}
	if a.LateEvidence != nil {
		return a.LateEvidence.validateAtFormat(a, i.Version)
	}
	return nil
}
