package persistence

import (
	"errors"
	"slices"
	"time"
)

// AutoIgnorePolicy counts distinct threshold-reaching outages in (now-window,
// now]. It is optional: neither the count nor the window has an implicit default.
type AutoIgnorePolicy struct {
	Incidents int           `json:"incidents"`
	Window    time.Duration `json:"window"`
}

func (p *AutoIgnorePolicy) validate() error {
	if p != nil && (p.Incidents < 2 || p.Incidents > 1000 || p.Window <= 0 || p.Window > 30*24*time.Hour) {
		return errors.New("automatic Ignore requires 2 to 1000 incidents and a positive window of at most 30 days")
	}
	return nil
}

func (m Monitor) validateIgnore() error {
	if m.IgnoreHealthyChecks < 0 || m.IgnoreHealthyChecks > max(1, m.Policy.Healthy) || len(m.IgnoreIncidents) > 1000 ||
		!controlText(m.IgnoreReason) || m.IgnoredBy != "" && !catalogIdentifier(m.IgnoredBy, 128) ||
		m.IgnoreCountedIncident != "" && !catalogIdentifier(m.IgnoreCountedIncident, 256) {
		return ErrControlInvalid
	}
	if m.Ignored {
		if m.IgnoredAt.IsZero() || m.IgnoredBy == "" || (m.IgnoreCause != "operator" && m.IgnoreCause != "incident_frequency") {
			return ErrControlInvalid
		}
	} else if !m.IgnoredAt.IsZero() || m.IgnoredBy != "" || m.IgnoreReason != "" || m.IgnoreCause != "" || m.IgnoreHealthyChecks != 0 {
		return ErrControlInvalid
	}
	for i, at := range m.IgnoreIncidents {
		if at.IsZero() || at.Year() < 1 || at.Year() > 9999 || i > 0 && at.Before(m.IgnoreIncidents[i-1]) {
			return ErrControlInvalid
		}
	}
	return m.Policy.AutoIgnore.validate()
}

func (m *Monitor) countIgnoreIncident(at time.Time, emit func(string, *Action)) {
	p := m.Policy.AutoIgnore
	if m.LifecycleVersion != LifecycleFormatVersion || p == nil || m.Ignored || m.IgnoreCountedIncident == m.IncidentID {
		return
	}
	m.IgnoreCountedIncident = m.IncidentID
	cutoff := at.Add(-p.Window)
	// A clock rollback excludes timestamps that are now in the future. Do not
	// count them in this window or retain an out-of-order snapshot record.
	m.IgnoreIncidents = slices.DeleteFunc(m.IgnoreIncidents, func(t time.Time) bool { return !t.After(cutoff) || t.After(at) })
	m.IgnoreIncidents = append(m.IgnoreIncidents, at)
	if len(m.IgnoreIncidents) >= p.Incidents {
		m.IgnoreIncidents = slices.Clone(m.IgnoreIncidents[len(m.IgnoreIncidents)-p.Incidents:])
		m.enterIgnore(at, "system", "incident_frequency", "Configured incident count reached within the rolling window.", emit)
	}
}

// A potential triggering outage must not send its first yellow before reaching
// the unhealthy threshold. Reserve no incident count until that threshold; an
// earlier recovery remains an observation, not a counted outage.
func (m Monitor) pendingAutomaticIgnore(at time.Time) bool {
	p := m.Policy.AutoIgnore
	if m.LifecycleVersion != LifecycleFormatVersion || p == nil || m.Ignored || !m.Recovering || m.IgnoreCountedIncident == m.IncidentID {
		return false
	}
	count := 0
	for _, t := range m.IgnoreIncidents {
		if t.After(at.Add(-p.Window)) && !t.After(at) {
			count++
		}
	}
	return count >= p.Incidents-1
}

func (m *Monitor) enterIgnore(at time.Time, actor, cause, reason string, emit func(string, *Action)) {
	m.Ignored, m.IgnoredAt, m.IgnoredBy = true, at, actor
	m.IgnoreCause, m.IgnoreReason, m.IgnoreHealthyChecks = cause, reason, 0
	m.ControlRevision = identity("ignore/" + m.ControlRevision + "/" + m.IncidentID)
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.State == Queued && (a.Kind == "intervention" || a.Kind == "code" && !m.ControlNotification(a)) {
			a.State, a.Outcome, a.FinishedAt = Cancelled, "monitor_ignored", at
			m.Actions[id] = a
			emit("action_cancelled", &a)
		}
	}
	emit("monitor_ignored", nil)
}

func (m *Monitor) leaveIgnore(at time.Time, recovered bool, emit func(string, *Action)) {
	previous := m.ControlRevision
	m.Ignored, m.IgnoredAt, m.IgnoredBy = false, time.Time{}, ""
	m.IgnoreCause, m.IgnoreReason, m.IgnoreHealthyChecks = "", "", 0
	m.IgnoreIncidents, m.IgnoreCountedIncident = nil, ""
	m.ControlRevision = identity("unignore/" + previous)
	emit("monitor_unignored", nil)
	if recovered {
		m.changeNotificationEpisode(identity("ignore_recovered/"+previous), at, emit)
		// This one green reports verified recovery from Ignore even when no
		// earlier delivery was attempted. Other suppression rules still apply.
		m.queueColor("green", at, emit)
	}
}

func (m *Monitor) observeIgnoreRecovery(c Command, emit func(string, *Action)) {
	if !m.Ignored {
		return
	}
	if c.Outcome != "success" || c.Warning {
		m.IgnoreHealthyChecks = 0
		return
	}
	m.IgnoreHealthyChecks++
	if m.IgnoreHealthyChecks >= max(1, m.Policy.Healthy) {
		m.leaveIgnore(c.At, true, emit)
	}
}
