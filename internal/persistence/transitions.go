package persistence

import (
	"fmt"
	"slices"
	"time"
)

// transition is pure. All timestamps and identities derive from the command
// and the preceding committed record. No clock, random source or I/O is used.
func transition(previous Monitor, c Command) Result {
	if c.Kind == "late_result" {
		return applyLateResult(previous, c)
	}
	m := previous.Clone()
	if m.ID != "" {
		m.ensureControlIdentity()
	}
	r := Result{Monitor: &m}
	emit := func(kind string, a *Action) {
		e := Event{MonitorID: m.ID, CatalogUID: m.CatalogUID, Revision: m.Revision, At: c.At, Type: kind, IncidentID: m.notificationIncident()}
		if a != nil {
			e.ActionID, e.Kind, e.Color, e.Endpoint, e.Outcome = a.ID, a.Kind, a.Color, a.Endpoint, a.Outcome
			e.Revision = a.Revision
			e.CatalogUID = a.CatalogUID
			e.IncidentID = a.IncidentID
		}
		r.Events = append(r.Events, e)
	}
	if c.Kind == "configure" {
		if m.ID == "" {
			m = c.Config.Clone()
			m.NextCheck = c.At
			m.Actions = make(map[string]Action)
			m.Cooldowns = make(map[string]time.Time)
		} else {
			recreated := m.CatalogUID != "" && m.CatalogUID != c.Config.CatalogUID
			if m.CatalogUID == "" && c.Config.CatalogUID != "" {
				// Initial manifest migration adopts the existing incident and action
				// identities once. Existing ambiguous actions stay conservatively held.
				m.CatalogUID = c.Config.CatalogUID
				for id, action := range m.Actions {
					action.CatalogUID = m.CatalogUID
					m.Actions[id] = action
				}
			}
			if m.Revision != c.Revision || m.Removed || recreated {
				for _, id := range sortedActions(m.Actions) {
					a := m.Actions[id]
					if a.State == Queued {
						a.State, a.Outcome, a.FinishedAt = Cancelled, "configuration_changed", c.At
						emit("action_cancelled", &a)
					}
					if a.State == Started {
						a.State, a.Outcome, a.FinishedAt = Unknown, "configuration_changed_while_started", c.At
						emit("action_unknown", &a)
					}
					m.Actions[id] = a
				}
				m.Revision = c.Revision
				m.NextCheck = c.At
			}
			if recreated {
				// A reused public ID names a new incarnation. Preserve old outcomes
				// for audit, but never transfer its incident ownership or queued work.
				if m.Incident {
					emit("incident_closed_configuration_removed", nil)
				}
				actions, sequence, manualRequests := m.Actions, m.Sequence, m.ManualRequests
				m = c.Config.Clone()
				m.Actions, m.Sequence, m.NextCheck = actions, sequence, c.At
				m.ManualRequests = manualRequests
				m.Cooldowns = make(map[string]time.Time)
			}
			m.Name, m.Removed, m.CatalogRevision = c.Config.Name, false, c.Config.CatalogRevision
			m.DependencyRevision = c.Config.DependencyRevision
			// A resource/metadata edit can change operational policy without
			// changing executable content. Adopt those options while retaining
			// incident and action identities when the execution revision matches.
			if m.Policy.Interval != c.Config.Policy.Interval {
				m.NextCheck = c.At
			}
			m.Policy = c.Config.Policy.Clone()
		}
		m.ensureControlIdentity()
		if !m.Policy.Enabled {
			for _, id := range sortedActions(m.Actions) {
				a := m.Actions[id]
				if a.CatalogUID == m.CatalogUID && a.State == Queued {
					a.State, a.Outcome, a.FinishedAt = Cancelled, "monitor_disabled", c.At
					m.Actions[id] = a
					emit("action_cancelled", &a)
				}
			}
		}
		r.Allowed = true
		return r
	}
	if c.Kind == "remove" {
		if m.ID == "" || m.Revision != c.Revision {
			return Result{}
		}
		m.Removed, m.Policy.Enabled = true, false
		if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
			m.IncidentClosedAt = c.At
			emit("incident_closed_configuration_removed", nil)
		}
		for _, id := range sortedActions(m.Actions) {
			a := m.Actions[id]
			if a.State == Queued {
				a.State, a.Outcome, a.FinishedAt = Cancelled, "configuration_removed", c.At
				emit("action_cancelled", &a)
			}
			if a.State == Started {
				a.State, a.Outcome, a.FinishedAt = Unknown, "configuration_removed_while_started", c.At
				emit("action_unknown", &a)
			}
			m.Actions[id] = a
		}
		return r
	}
	if c.Kind == "recover" {
		for _, id := range sortedActions(m.Actions) {
			a := m.Actions[id]
			if a.State == Started {
				a.State, a.Outcome, a.FinishedAt = Unknown, "interrupted_after_start", c.At
				m.Actions[id] = a
				emit("action_unknown", &a)
			}
		}
		return r
	}
	if m.ID == "" || m.Removed || m.Revision != c.Revision {
		return Result{}
	}
	if c.Kind == "start" || c.Kind == "result" {
		a, ok := m.Actions[c.ActionID]
		if !ok || a.Revision != c.Revision {
			return Result{}
		}
		if c.Kind == "start" {
			if a.State != Queued || a.CatalogUID != m.CatalogUID || c.At.Before(a.NotBefore) || !m.Policy.Enabled ||
				c.Maintenance || m.Policy.InMaintenance(c.At) || m.snoozed(c.At) || m.suppressedNotification(a) {
				return Result{}
			}
			if a.Kind == "intervention" && (!m.Policy.Intervention || a.Attempt > m.Policy.recoveryLimit() ||
				m.VerifyRemaining > 0 || m.recoveryDue(c.At).After(c.At) || (a.Manual && (!m.observedUnhealthy() || m.manualRecoveryDue(c.At).After(c.At))) || m.otherActiveRecovery(a.ID)) {
				return Result{}
			}
			a.State, a.StartedAt = Started, c.At
			if c.ExecutorSession != "" {
				a.ExecutorKind, a.ExecutorSession = "local", c.ExecutorSession
			}
			m.Actions[a.ID] = a
			emit("action_started", &a)
			r.Allowed = true
			return r
		}
		if a.State != Started {
			return Result{}
		}
		a.State, a.Outcome, a.FinishedAt = Succeeded, "accepted", c.At
		if c.Outcome != "success" {
			a.State, a.Outcome = Failed, "rejected"
			if c.Ambiguous {
				a.State, a.Outcome = Unknown, "external_outcome_unknown"
			}
		}
		m.Actions[a.ID] = a
		emit("action_"+string(a.State), &a)
		if a.Kind == "intervention" {
			if a.State == Succeeded {
				m.VerifyRemaining = max(1, m.Policy.Healthy)
				m.VerificationAfter = max(m.Generation, c.Generation)
				m.RecoveryStreak = 0
				m.queueColor("cyan", c.At, emit)
			} else {
				m.InterventionFailures++
				if a.State == Failed && c.At.After(m.LastInterventionFailure) {
					m.LastInterventionFailure = c.At
				}
				m.openIncident(c.At, emit)
			}
		} else if a.State == Failed && c.Retryable && a.Attempt < 3 && m.Policy.Enabled && !m.snoozed(c.At) && !m.suppressedNotification(a) {
			delete(m.Actions, a.ID)
			a.Attempt++
			a.ID = identity(fmt.Sprintf("%s/retry/%d", a.ID, a.Attempt))
			a.State, a.Outcome, a.NotBefore = Queued, "", c.At.Add(time.Second*time.Duration(1<<a.Attempt))
			a.StartedAt, a.FinishedAt, a.ExecutorFinishedAt = time.Time{}, time.Time{}, time.Time{}
			a.ExecutorKind, a.ExecutorSession = "", ""
			m.Actions[a.ID] = a
			emit("action_queued", &a)
		}
		return r
	}
	paused := !m.Policy.Enabled || m.snoozed(c.At) || (c.CheckControlRevision != "" && c.CheckControlRevision != m.ControlRevision)
	if c.Generation <= m.Generation || (paused && c.ExecutionStart.IsZero()) {
		return Result{}
	}
	c.Maintenance = c.Maintenance || m.Policy.InMaintenance(c.At)
	m.TotalChecks++
	if c.Outcome == "success" {
		m.SuccessfulChecks++
	}
	m.Generation = c.Generation
	m.LastCheck, m.LastOutcome = c.At, c.Outcome
	if !c.ExecutionStart.IsZero() && !c.ExecutionEnd.Before(c.ExecutionStart) {
		m.LastLatency, m.LatencyAvailable = c.ExecutionEnd.Sub(c.ExecutionStart), true
	}
	if paused {
		if c.Outcome == "success" {
			m.LastSuccess = c.At
		}
		return r // Actual in-flight observation retained; no new incident/action/schedule.
	}
	previousWarning := m.Warning
	m.Warning = c.Warning
	if c.Outcome != "success" {
		m.beginIncident(c.At)
		m.ConsecutiveFailures++
		m.PulseFailures++
		m.Recovering, m.Warning, m.RecoveryStreak = true, false, 0
		if m.VerifyRemaining > 0 && c.Generation > m.VerificationAfter {
			m.VerifyRemaining = 0
			m.openIncident(c.At, emit)
		} else if m.VerifyRemaining == 0 {
			if m.PulseFailures == 1 && !m.Incident {
				m.queueColor("yellow", c.At, emit)
			}
			if m.PulseFailures >= max(1, m.Policy.Unhealthy) && !c.Maintenance {
				if m.mayRetryRecovery() {
					if m.recoveryAttempts() > 0 {
						m.LastInterventionFailure = m.lastRecoveryFailure()
					}
					m.InterventionAttempts = m.recoveryAttempts() + 1
					m.InterventionAttempted = true
					m.queueAction("intervention", "", 0, m.recoveryDue(c.At), emit)
				} else if !m.pendingIntervention() {
					m.openIncident(c.At, emit)
				}
				m.PulseFailures = 0
			}
		}
	} else {
		m.ConsecutiveFailures, m.PulseFailures = 0, 0
		m.LastSuccess = c.At
		if m.Warning && !previousWarning {
			m.queueColor("yellow", c.At, emit)
		}
		if previousWarning && !m.Warning && !m.Recovering && !m.Incident {
			m.queueColor("green", c.At, emit)
		}
		if m.VerifyRemaining > 0 {
			if c.Generation > m.VerificationAfter {
				m.VerifyRemaining--
				if m.VerifyRemaining == 0 {
					m.recovered(c.At, emit)
				}
			}
		} else if m.Recovering && !m.unresolvedIntervention() {
			m.RecoveryStreak++
			if m.RecoveryStreak >= max(1, m.Policy.Healthy) {
				m.recovered(c.At, emit)
			}
		}
	}
	next := c.Scheduled.Add(m.Policy.Interval)
	if c.Scheduled.IsZero() {
		next = c.At.Add(m.Policy.Interval)
	}
	if !next.After(c.At) {
		next = next.Add((c.At.Sub(next)/m.Policy.Interval + 1) * m.Policy.Interval)
	}
	m.NextCheck = next
	return r
}

func sortedActions(actions map[string]Action) []string {
	keys := make([]string, 0, len(actions))
	for k := range actions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (m *Monitor) pendingIntervention() bool {
	for _, a := range m.Actions {
		if a.CatalogUID == m.CatalogUID && a.Kind == "intervention" && (a.State == Queued || a.State == Started || a.Held()) {
			return true
		}
	}
	return false
}

func (m *Monitor) unresolvedIntervention() bool {
	for _, a := range m.Actions {
		if a.CatalogUID == m.CatalogUID && a.Kind == "intervention" && (a.State == Started || a.Held()) {
			return true
		}
	}
	return false
}

func (m *Monitor) openIncident(at time.Time, emit func(string, *Action)) {
	if m.Incident {
		return
	}
	m.beginIncident(at)
	m.Incident = true
	emit("incident_opened", nil)
	m.queueColor("red", at, emit)
}

func (m *Monitor) recovered(at time.Time, emit func(string, *Action)) {
	if m.Incident {
		emit("incident_closed", nil)
	}
	m.Incident, m.Recovering, m.InterventionAttempted, m.RecoveryStreak = false, false, false, 0
	m.InterventionAttempts, m.LastInterventionFailure = 0, time.Time{}
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.CatalogUID == m.CatalogUID && a.Kind == "intervention" && a.State == Queued {
			a.State, a.Outcome, a.FinishedAt = Cancelled, "health_recovered", at
			m.Actions[id] = a
			emit("action_cancelled", &a)
		}
	}
	m.queueColor("green", at, emit)
	if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
		m.IncidentClosedAt = at
	}
}

func (m *Monitor) queueColor(color string, at time.Time, emit func(string, *Action)) {
	if !m.Policy.Enabled || m.snoozed(at) || (m.Dismissed && m.notificationIncident() != "") {
		return
	}
	if color == "green" && m.Warning {
		return
	}
	count := m.Policy.Endpoints[color]
	if count == 0 {
		return
	}
	due := at
	if last := m.Cooldowns[color]; !(color == "green" && m.Policy.RecoveryBypass) && due.Before(last.Add(m.Policy.Cooldown)) {
		due = last.Add(m.Policy.Cooldown)
	}
	for n := 0; n < count; n++ {
		m.queueAction("code", color, n, due, emit)
	}
	if m.Cooldowns == nil {
		m.Cooldowns = make(map[string]time.Time)
	}
	m.Cooldowns[color] = due
}

func (m *Monitor) queueAction(kind, color string, endpoint int, at time.Time, emit func(string, *Action)) {
	for id, a := range m.Actions {
		if a.Kind != kind || a.Color != color || a.Endpoint != endpoint || a.CatalogUID != m.CatalogUID {
			continue
		}
		if a.State == Queued || a.State == Started || a.Held() {
			return
		}
		if a.State != Unknown && a.Review == nil {
			delete(m.Actions, id)
		}
	}
	m.Sequence++
	id := identity(fmt.Sprintf("%s/%s/%d/%s/%s/%d", m.ID, m.Revision, m.Sequence, kind, color, endpoint))
	if m.CatalogUID != "" {
		id = identity(m.CatalogUID + "/" + id)
	}
	a := Action{ID: id, CatalogUID: m.CatalogUID, IncidentID: m.notificationIncident(), Revision: m.Revision, Kind: kind, Color: color, Endpoint: endpoint, Attempt: 1, State: Queued, NotBefore: at}
	if kind == "intervention" {
		a.Attempt = max(1, m.recoveryAttempts())
	}
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[id] = a
	emit("action_queued", &a)
}
