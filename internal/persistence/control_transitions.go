package persistence

import (
	"fmt"
	"time"
)

func (m *Monitor) ensureControlIdentity() {
	if m.ControlRevision == "" {
		m.ControlRevision = identity("control/" + m.ID + "/" + m.CatalogUID)
	}
	if m.IncidentID == "" && (m.Incident || m.Recovering) {
		m.beginIncident(time.Time{})
		for id, a := range m.Actions {
			if a.IncidentID != "" || a.CatalogUID != m.CatalogUID {
				continue
			}
			if a.Kind == "intervention" || a.Color != "green" {
				a.IncidentID = m.IncidentID
			} else {
				a.IncidentID = identity("legacy-delivery/" + m.ID + "/" + a.ID)
			}
			m.Actions[id] = a
		}
	}
}

func (m *Monitor) beginIncident(at time.Time) {
	if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
		return
	}
	m.IncidentSequence++
	m.IncidentID = identity(fmt.Sprintf("incident/%s/%s/%d", m.ID, m.CatalogUID, m.IncidentSequence))
	m.IncidentRevision = identity("triage/" + m.IncidentID)
	m.IncidentOpenedAt, m.IncidentClosedAt = at, time.Time{}
	m.AcknowledgedBy, m.AcknowledgedNote, m.DismissedBy, m.DismissedReason = "", "", "", ""
	m.AcknowledgedAt, m.DismissedAt = time.Time{}, time.Time{}
	m.Dismissed = false
}

func (m Monitor) snoozed(at time.Time) bool {
	return !m.SnoozedUntil.IsZero() && at.Before(m.SnoozedUntil)
}
func (m Monitor) suppressedNotification(a Action) bool {
	return a.Kind == "code" && m.Dismissed && a.IncidentID != "" && a.IncidentID == m.IncidentID
}
func (m Monitor) notificationIncident() string {
	if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
		return m.IncidentID
	}
	return ""
}

func (f *machine) applyControl(id string, c ControlCommand, at time.Time, index uint64) Result {
	previous, ok := f.image.Monitors[id]
	current, retained := f.image.Catalog[(CatalogKey{Kind: "Monitor", ID: id}).indexKey()]
	if !ok || previous.Removed || !retained || current.Removed || previous.CatalogUID != c.MonitorUID || current.UID != c.MonitorUID {
		return Result{Err: ErrControlConflict}
	}
	m := previous.Clone()
	m.ensureControlIdentity()
	subject := c.Subject()
	if subject == "incident" {
		if m.IncidentID != c.IncidentID || !m.IncidentClosedAt.IsZero() || (!m.Incident && !m.Recovering) {
			return Result{Err: ErrIncidentNotActive}
		}
		if m.IncidentRevision != c.ExpectedRevision {
			return Result{Err: ErrControlConflict}
		}
	} else if m.ControlRevision != c.ExpectedRevision {
		return Result{Err: ErrControlConflict}
	}
	if _, exists := f.image.Operations[c.OperationID]; exists {
		return Result{Err: ErrControlConflict}
	}
	if f.pendingOperationCount() >= maxPendingCatalogOperations {
		return Result{Err: ErrCatalogBusy}
	}
	if c.Action == "expire_snooze" && (m.SnoozedUntil.IsZero() || !m.SnoozedUntil.Equal(c.Until)) {
		return Result{Err: ErrControlConflict}
	}
	if c.Action == "unsnooze" && m.SnoozedUntil.IsZero() {
		return Result{Err: ErrControlConflict}
	}
	var events []Event
	switch c.Action {
	case "acknowledge":
		m.AcknowledgedBy, m.AcknowledgedAt, m.AcknowledgedNote = c.Actor, at, c.Note
	case "dismiss":
		m.Dismissed, m.DismissedBy, m.DismissedAt, m.DismissedReason = true, c.Actor, at, c.Reason
	case "reopen":
		m.Dismissed, m.DismissedBy, m.DismissedAt, m.DismissedReason = false, "", time.Time{}, ""
	case "snooze":
		m.SnoozedUntil, m.SnoozedBy, m.SnoozeReason = c.Until, c.Actor, c.Reason
	case "unsnooze", "expire_snooze":
		m.SnoozedUntil, m.SnoozedBy, m.SnoozeReason = time.Time{}, "", ""
		// The operational owner replaces its scheduled entry using this new revision.
		// It never revives previously queued actions or elapsed check slots.
		m.NextCheck = at
	}
	if subject == "incident" {
		m.IncidentRevision = c.Revision
	} else {
		m.ControlRevision = c.Revision
	}
	for _, actionID := range sortedActions(m.Actions) {
		a := m.Actions[actionID]
		cancel := c.Action == "snooze" || (c.Action == "dismiss" && a.Kind == "code" && a.IncidentID == c.IncidentID)
		if a.CatalogUID != m.CatalogUID || a.State != Queued || !cancel {
			continue
		}
		a.State, a.Outcome, a.FinishedAt = Cancelled, c.Action, at
		m.Actions[actionID] = a
		events = append(events, Event{MonitorID: id, CatalogUID: a.CatalogUID, Revision: a.Revision, IncidentID: a.IncidentID, At: at, Type: "action_cancelled", ActionID: a.ID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome, Actor: c.Actor, Reason: c.Reason})
	}
	events = append(events, Event{MonitorID: id, CatalogUID: m.CatalogUID, Revision: m.Revision, IncidentID: c.IncidentID, At: at, Type: "control_" + c.Action, Actor: c.Actor, Reason: c.Reason, Note: c.Note, ControlRevision: c.Revision})
	events = append(events, f.installMonitor(previous, m, at)...)
	receipt := OperationReceipt{ID: c.OperationID, Subject: subject, IncidentID: c.IncidentID, Key: current.Key, UID: m.CatalogUID, OldVersion: c.ExpectedRevision, NewVersion: c.Revision, Generation: current.Generation, CommittedIndex: index, Actor: c.Actor, At: at, UpdatedAt: at, State: "committed", Outcome: "committed"}
	if f.image.Operations == nil {
		f.image.Operations = make(map[string]OperationReceipt)
	}
	f.putOperation(receipt)
	f.image.Version = max(f.image.Version, CatalogFormatVersion)
	events = append(events, receiptEvent(receipt))
	return Result{Allowed: true, Monitor: &m, Operation: &receipt, Events: events}
}

// A committed deletion/recreation supersedes controls before the slower owner
// projection catches up. Otherwise an intervening snapshot would retain an
// operation whose subject no longer exists in the authoritative catalog.
func (f *machine) supersedeCatalogControls(record CatalogRecord, at time.Time) []Event {
	if record.Key.Kind != "Monitor" {
		return nil
	}
	m, ok := f.image.Monitors[record.Key.ID]
	if !ok || (!record.Removed && record.UID == m.CatalogUID) {
		return nil
	}
	var events []Event
	events = append(events, f.supersedeActionReceipts(m, at)...)
	for _, subject := range []string{"control", "incident"} {
		revision := m.ControlRevision
		if subject == "incident" {
			revision = m.IncidentRevision
		}
		r, ok := f.operationByVersion(record.Key, m.CatalogUID, subject, revision)
		if !ok || r.Subject == "" {
			continue
		}
		r.State, r.Outcome = "partial", "superseded"
		r.UpdatedAt = at
		if at.Before(r.At) {
			r.UpdatedAt = r.At
		}
		f.deleteOperation(r.ID)
		events = append(events, receiptEvent(r))
	}
	return events
}
