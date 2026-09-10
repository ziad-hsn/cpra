package durable

import (
	"fmt"
	"slices"
	"time"
)

// transition is pure. All timestamps and identities derive from the command
// and the preceding committed record. No clock, random source or I/O is used.
func transition(previous Monitor, c Command) Result {
	m := previous.Clone()
	r := Result{Monitor: &m}
	emit := func(kind string, a *Action) {
		e := Event{MonitorID: m.ID, Revision: m.Revision, At: c.At, Type: kind}
		if a != nil {
			e.ActionID, e.Kind, e.Color, e.Endpoint, e.Outcome = a.ID, a.Kind, a.Color, a.Endpoint, a.Outcome
			e.Revision = a.Revision
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
			if m.Revision != c.Revision || m.Removed {
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
				m.Revision, m.Policy = c.Revision, c.Config.Clone().Policy
				m.NextCheck = c.At
			}
			m.Name, m.Removed = c.Config.Name, false
		}
		r.Allowed = true
		return r
	}
	if c.Kind == "remove" {
		if m.ID == "" || m.Revision != c.Revision {
			return Result{}
		}
		m.Removed, m.Policy.Enabled = true, false
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
			if a.State != Queued || c.At.Before(a.NotBefore) || !m.Policy.Enabled {
				return Result{}
			}
			a.State, a.StartedAt = Started, c.At
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
				m.openIncident(c.At, emit)
			}
		} else if a.State == Failed && c.Retryable && a.Attempt < 3 {
			delete(m.Actions, a.ID)
			a.Attempt++
			a.ID = identity(fmt.Sprintf("%s/retry/%d", a.ID, a.Attempt))
			a.State, a.Outcome, a.NotBefore = Queued, "", c.At.Add(time.Second*time.Duration(1<<a.Attempt))
			a.StartedAt, a.FinishedAt = time.Time{}, time.Time{}
			m.Actions[a.ID] = a
			emit("action_queued", &a)
		}
		return r
	}
	if c.Generation <= m.Generation {
		return Result{}
	}
	m.TotalChecks++
	if c.Outcome == "success" {
		m.SuccessfulChecks++
	}
	m.Generation = c.Generation
	m.LastCheck, m.LastOutcome = c.At, c.Outcome
	if !c.ExecutionStart.IsZero() && !c.ExecutionEnd.Before(c.ExecutionStart) {
		m.LastLatency, m.LatencyAvailable = c.ExecutionEnd.Sub(c.ExecutionStart), true
	}
	previousWarning := m.Warning
	m.Warning = c.Warning
	if c.Outcome != "success" {
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
				if m.Policy.Intervention && !m.InterventionAttempted && !m.pendingIntervention() {
					m.InterventionAttempted = true
					m.queueAction("intervention", "", 0, c.At, emit)
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
		if a.Kind == "intervention" && (a.State == Queued || a.State == Started || a.State == Unknown) {
			return true
		}
	}
	return false
}

func (m *Monitor) unresolvedIntervention() bool {
	for _, a := range m.Actions {
		if a.Kind == "intervention" && (a.State == Started || a.State == Unknown) {
			return true
		}
	}
	return false
}

func (m *Monitor) openIncident(at time.Time, emit func(string, *Action)) {
	if m.Incident {
		return
	}
	m.Incident = true
	emit("incident_opened", nil)
	m.queueColor("red", at, emit)
}

func (m *Monitor) recovered(at time.Time, emit func(string, *Action)) {
	if m.Incident {
		emit("incident_closed", nil)
	}
	m.Incident, m.Recovering, m.InterventionAttempted, m.RecoveryStreak = false, false, false, 0
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.Kind == "intervention" && a.State == Queued {
			a.State, a.Outcome, a.FinishedAt = Cancelled, "health_recovered", at
			m.Actions[id] = a
			emit("action_cancelled", &a)
		}
	}
	m.queueColor("green", at, emit)
}

func (m *Monitor) queueColor(color string, at time.Time, emit func(string, *Action)) {
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
		if a.Kind != kind || a.Color != color || a.Endpoint != endpoint {
			continue
		}
		if a.State == Queued || a.State == Started || a.State == Unknown {
			return
		}
		delete(m.Actions, id)
	}
	m.Sequence++
	id := identity(fmt.Sprintf("%s/%s/%d/%s/%s/%d", m.ID, m.Revision, m.Sequence, kind, color, endpoint))
	a := Action{ID: id, Revision: m.Revision, Kind: kind, Color: color, Endpoint: endpoint, Attempt: 1, State: Queued, NotBefore: at}
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[id] = a
	emit("action_queued", &a)
}
