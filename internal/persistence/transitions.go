package persistence

import (
	"fmt"
	"slices"
	"time"
)

// transition is pure. All timestamps and identities derive from the command
// and the preceding committed record. No clock, random source or I/O is used.
func transition(previous Monitor, c Command) Result {
	return transitionAtFormat(previous, c, LifecycleFormatVersion)
}

func transitionAtFormat(previous Monitor, c Command, version int) Result {
	if c.Kind == "late_result" {
		return applyLateResult(previous, c)
	}
	m := previous.Clone()
	if m.ID != "" && lifecycleStorageFormat(version) {
		m.upgradeLifecycle()
	}
	if m.ID != "" {
		m.ensureControlIdentity()
	}
	r := Result{Monitor: &m}
	emit := func(kind string, a *Action) {
		e := Event{MonitorID: m.ID, CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation, Revision: m.Revision, At: c.At, Type: kind, IncidentID: m.notificationIncident(), Failure: m.LastFailure}
		if kind == "monitor_ignored" {
			e.Actor, e.Reason, e.ControlRevision = m.IgnoredBy, m.IgnoreReason, m.ControlRevision
		} else if kind == "monitor_unignored" {
			e.Actor, e.Reason, e.ControlRevision = "system", "verified_recovery", m.ControlRevision
		}
		if a != nil {
			e.Control = a.Control
			e.Failure, e.Cause = a.Failure, a.Cause
			e.DeliveryID, e.NotificationEpisode, e.EndpointID = a.DeliveryID, a.NotificationEpisode, a.EndpointID
			if a.RecoveryFailure != nil {
				v := *a.RecoveryFailure
				e.RecoveryFailure = &v
			}
			e.ActionID, e.Kind, e.Color, e.Endpoint, e.Outcome = a.ID, a.Kind, a.Color, a.Endpoint, a.Outcome
			e.Revision = a.Revision
			e.CatalogUID = a.CatalogUID
			e.FileIncarnation = a.FileIncarnation
			e.IncidentID = a.IncidentID
		}
		r.Events = append(r.Events, e)
	}
	if c.Kind == "configure" {
		enabledChanged := previous.ID != "" && !previous.Removed && previous.Policy.Enabled != c.Config.Policy.Enabled
		if m.ID == "" {
			m = c.Config.Clone()
			if lifecycleStorageFormat(version) && m.CatalogUID == "" {
				m.FileIncarnation = identity("file/" + m.ID)
			}
			m.NextCheck = c.At
			m.Actions = make(map[string]Action)
			m.Cooldowns = make(map[string]time.Time)
		} else {
			recreated := m.CatalogUID != "" && m.CatalogUID != c.Config.CatalogUID || lifecycleStorageFormat(version) && m.Removed && m.CatalogUID == ""
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
				incarnation := m.FileIncarnation
				m = c.Config.Clone()
				if lifecycleStorageFormat(version) && m.CatalogUID == "" {
					m.FileIncarnation = identity("file/" + m.ID + "/" + incarnation)
				}
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
			if m.Policy.Healthy != c.Config.Policy.Healthy {
				m.IgnoreHealthyChecks = 0
			}
			m.Policy = c.Config.Policy.Clone()
		}
		m.ensureControlIdentity()
		if deliveryStorageFormat(version) {
			m.upgradeNotifications(c.At, emit)
			if m.Policy.InMaintenance(c.At) {
				m.suppressMaintenanceNotifications(c.At, emit)
			}
		}
		if lifecycleStorageFormat(version) {
			m.upgradeLifecycle()
		}
		if !m.Policy.Enabled {
			for _, id := range sortedActions(m.Actions) {
				a := m.Actions[id]
				if m.OwnsAction(a) && a.State == Queued && !m.ControlNotification(a) {
					a.State, a.Outcome, a.FinishedAt = Cancelled, "monitor_disabled", c.At
					m.Actions[id] = a
					emit("action_cancelled", &a)
				}
			}
		}
		if lifecycleStorageFormat(version) && enabledChanged {
			m.queueEnabledNotice(c.At, emit)
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
		if deliveryStorageFormat(version) {
			m.upgradeNotifications(c.At, emit)
		}
		for _, id := range sortedActions(m.Actions) {
			a := m.Actions[id]
			if lifecycleStorageFormat(version) && a.Kind == "intervention" && a.State == Queued && m.OwnsAction(a) {
				if a.Deadline.IsZero() || !c.At.Before(a.Deadline) {
					a.State, a.Outcome, a.FinishedAt = Cancelled, "intervention_deadline", c.At
					m.Actions[id] = a
					emit("action_cancelled", &a)
					continue
				}
				m.RecoveryCheckAfter = c.At
			}
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
	if deliveryStorageFormat(version) {
		m.upgradeNotifications(c.At, emit)
	}
	if c.Kind == "start" || c.Kind == "result" {
		a, ok := m.Actions[c.ActionID]
		if !ok || a.Revision != c.Revision {
			return Result{}
		}
		if c.Kind == "start" {
			controlNotice := m.ControlNotification(a)
			if lifecycleStorageFormat(version) && a.Kind == "intervention" && a.State == Queued {
				if a.Deadline.IsZero() || !c.At.Before(a.Deadline) {
					a.State, a.Outcome, a.FinishedAt = Cancelled, "intervention_deadline", c.At
					m.Actions[a.ID] = a
					emit("action_cancelled", &a)
					return r
				}
				if c.ActionDeadline.After(a.Deadline) {
					return Result{Err: ErrNotificationIdentity}
				}
				if !c.ActionDeadline.IsZero() {
					a.Deadline = c.ActionDeadline
				}
			}
			if m.NotificationVersion == DeliveryFormatVersion && a.Kind == "code" && a.State == Queued {
				if !controlNotice && (c.Maintenance || m.Policy.InMaintenance(c.At) || m.Policy.MaintenanceBetween(a.QueuedAt, c.At)) {
					m.cancelNotification(a, "maintenance", c.At, emit)
					return r
				}
				if !m.notificationEligible(a) {
					m.cancelNotification(a, "notification_superseded", c.At, emit)
					return r
				}
				if !a.Deadline.IsZero() && !c.At.Before(a.Deadline) {
					m.cancelNotification(a, "notification_deadline", c.At, emit)
					return r
				}
				if !c.ActionDeadline.IsZero() && !a.Deadline.IsZero() && !c.ActionDeadline.Equal(a.Deadline) {
					return Result{Err: ErrNotificationIdentity}
				}
			}
			if c.AgentConnection != nil && c.AgentConnection.CatalogUID != a.CatalogUID {
				return Result{Err: ErrAgentActionInvalid}
			}
			if a.State != Queued || !m.OwnsAction(a) || c.At.Before(a.NotBefore) || m.snoozed(c.At) ||
				!controlNotice && (!m.Policy.Enabled || m.Ignored || m.pendingAutomaticIgnore(c.At) || c.Maintenance || m.Policy.InMaintenance(c.At) || m.suppressedNotification(a)) {
				return Result{}
			}
			if a.Kind == "intervention" && (!m.Policy.Intervention || a.Attempt > m.Policy.recoveryLimit() ||
				m.LifecycleVersion == LifecycleFormatVersion && !m.RecoveryCheckAfter.IsZero() ||
				m.VerifyRemaining > 0 || m.recoveryDue(c.At).After(c.At) || (a.Manual && (!m.observedUnhealthy() || m.manualRecoveryDue(c.At).After(c.At))) || m.otherActiveRecovery(a.ID)) {
				return Result{}
			}
			a.State, a.StartedAt = Started, c.At
			if m.LifecycleVersion == LifecycleFormatVersion && a.Kind == "code" &&
				(a.Color == "yellow" || a.Color == "red" || a.Color == "cyan") {
				m.NotificationAttempted = a.NotificationCorrelation
			}
			if m.NotificationVersion == DeliveryFormatVersion && a.Kind == "code" && a.Deadline.IsZero() {
				a.Deadline = c.ActionDeadline
				if a.Deadline.IsZero() {
					a.Deadline = c.At.Add(NotificationTimeout)
				}
			}
			if c.ExecutorSession != "" {
				a.ExecutorKind, a.ExecutorSession = "local", c.ExecutorSession
				if c.Driver == "a2a" {
					a.ExecutorKind = "agent"
					if c.AgentConnection != nil {
						connection := c.AgentConnection.Clone()
						a.AgentConnection = &connection
					}
				}
			}
			m.Actions[a.ID] = a
			emit("action_started", &a)
			r.Allowed = true
			return r
		}
		if a.State != Started {
			return Result{}
		}
		if m.NotificationVersion == DeliveryFormatVersion && (c.Maintenance || m.Policy.InMaintenance(c.At)) {
			m.suppressMaintenanceNotifications(c.At, emit)
		}
		a.State, a.Outcome, a.FinishedAt = Succeeded, "accepted", c.At
		a.Failure = c.Failure
		if c.Outcome != "success" {
			a.State, a.Outcome = Failed, "rejected"
			if c.Ambiguous {
				a.State, a.Outcome = Unknown, "external_outcome_unknown"
			}
		}
		if a.ExecutorKind == "agent" && (c.Ambiguous || c.Outcome != "success" || !agentSuccess(a)) {
			a.State, a.Outcome = Unknown, "external_outcome_unknown"
		}
		m.Actions[a.ID] = a
		emit("action_"+string(a.State), &a)
		if a.Kind == "intervention" {
			if m.LifecycleVersion == LifecycleFormatVersion && !m.currentIntervention(a) {
				return r // Preserve the outcome without changing a later incident.
			}
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
				m.queueRecoveryFailure(a, c.At, emit)
			}
		} else if a.State == Failed && c.Retryable && a.Attempt < 3 && !m.snoozed(c.At) &&
			(m.ControlNotification(a) || m.Policy.Enabled && !m.Ignored && !m.suppressedNotification(a)) &&
			(m.NotificationVersion != DeliveryFormatVersion || m.notificationEligible(a) && (m.ControlNotification(a) || !m.Policy.InMaintenance(c.At) && !m.NotificationMaintenance) && notificationRetryAt(c, a.Attempt).Before(a.Deadline)) {
			delete(m.Actions, a.ID)
			a.Attempt++
			if m.NotificationVersion == DeliveryFormatVersion {
				a.ID = identity(fmt.Sprintf("%s/attempt/%d", a.DeliveryID, a.Attempt))
			} else {
				a.ID = identity(fmt.Sprintf("%s/retry/%d", a.ID, a.Attempt))
			}
			a.State, a.Outcome, a.NotBefore = Queued, "", notificationRetryAt(c, a.Attempt-1)
			a.Failure = FailureDiagnostic{}
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
	if m.NotificationVersion == DeliveryFormatVersion {
		m.NotificationMaintenance = c.Maintenance
		if c.Maintenance {
			m.suppressMaintenanceNotifications(c.At, emit)
		}
	}
	m.TotalChecks++
	if c.Outcome == "success" {
		m.SuccessfulChecks++
	}
	m.Generation = c.Generation
	m.LastCheck, m.LastOutcome, m.LastFailure = c.At, c.Outcome, c.Failure
	if m.LifecycleVersion == LifecycleFormatVersion && !m.RecoveryCheckAfter.IsZero() && !c.ExecutionStart.IsZero() && !c.ExecutionStart.Before(m.RecoveryCheckAfter) {
		m.RecoveryCheckAfter = time.Time{}
		if c.Outcome == "success" {
			m.cancelQueuedInterventions(c.At, "healthy_after_restart", emit)
		}
	}
	if !c.ExecutionStart.IsZero() && !c.ExecutionEnd.Before(c.ExecutionStart) {
		m.LastLatency, m.LatencyAvailable = c.ExecutionEnd.Sub(c.ExecutionStart), true
	}
	if paused {
		if c.Outcome == "success" {
			m.LastSuccess = c.At
		}
		return r // Actual in-flight observation retained; no new incident/action/schedule.
	}
	previousWarning, previousRecovering := m.Warning, m.Recovering
	m.Warning = c.Warning
	if c.Outcome != "success" {
		m.beginIncident(c.At)
		if m.NotificationVersion == DeliveryFormatVersion && !previousWarning && !previousRecovering {
			m.setNotificationCorrelation(m.IncidentID)
		}
		m.changeNotificationEpisode(identity("outage/"+m.IncidentID), c.At, emit)
		m.ConsecutiveFailures++
		m.PulseFailures++
		if m.ConsecutiveFailures >= max(1, m.Policy.Unhealthy) {
			m.countIgnoreIncident(c.At, emit)
		}
		m.Recovering, m.Warning, m.RecoveryStreak = true, false, 0
		if m.VerifyRemaining > 0 && c.Generation > m.VerificationAfter {
			m.VerifyRemaining = 0
			m.openIncident(c.At, emit)
		} else if m.VerifyRemaining == 0 {
			if m.PulseFailures == 1 && !m.Incident && (!notificationStorageFormat(version) || !previousRecovering) {
				m.queueColor("yellow", c.At, emit)
			}
			if m.PulseFailures >= max(1, m.Policy.Unhealthy) && (!c.Maintenance || m.LifecycleVersion == LifecycleFormatVersion) {
				if !c.Maintenance && m.mayRetryRecovery() {
					if m.recoveryAttempts() > 0 {
						m.LastInterventionFailure = m.lastRecoveryFailure()
					}
					m.InterventionAttempts = m.recoveryAttempts() + 1
					m.InterventionAttempted = true
					m.queueAction("intervention", "", 0, m.recoveryDue(c.At), emit)
				} else if !m.pendingIntervention() || m.LifecycleVersion == LifecycleFormatVersion && !m.pendingCurrentIntervention() {
					m.openIncident(c.At, emit)
				}
				m.PulseFailures = 0
			}
		}
	} else {
		m.ConsecutiveFailures, m.PulseFailures = 0, 0
		m.LastSuccess = c.At
		if m.Warning && !previousWarning {
			if !m.Recovering && !m.Incident {
				episode := identity(fmt.Sprintf("warning/%s/%s/%d", m.ID, m.CatalogUID, c.Generation))
				m.changeNotificationEpisode(episode, c.At, emit)
				if m.NotificationVersion == DeliveryFormatVersion {
					m.setNotificationCorrelation(episode)
				}
			}
			m.queueColor("yellow", c.At, emit)
		}
		if !m.Recovering && !m.Incident {
			if m.LifecycleVersion == LifecycleFormatVersion {
				if m.Warning {
					m.RecoveryStreak = 0
				} else if previousWarning || m.RecoveryStreak > 0 {
					m.RecoveryStreak++
					if m.RecoveryStreak >= max(1, m.Policy.Healthy) {
						m.RecoveryStreak = 0
						m.changeNotificationEpisode(identity("warning_recovered/"+m.NotificationEpisode), c.At, emit)
						if m.greenEligible() {
							m.queueColor("green", c.At, emit)
						}
					}
				}
			} else if previousWarning && !m.Warning {
				m.changeNotificationEpisode(identity("warning_recovered/"+m.NotificationEpisode), c.At, emit)
				m.queueColor("green", c.At, emit)
			}
		}
		if m.VerifyRemaining > 0 {
			if c.Generation > m.VerificationAfter {
				m.VerifyRemaining--
				if m.VerifyRemaining == 0 {
					m.recovered(c.At, emit)
				}
			}
		} else if m.Recovering && (m.LifecycleVersion == LifecycleFormatVersion || !m.unresolvedIntervention()) {
			m.RecoveryStreak++
			if m.RecoveryStreak >= max(1, m.Policy.Healthy) {
				m.recovered(c.At, emit)
			}
		}
	}
	m.observeIgnoreRecovery(c, emit)
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

func notificationRetryAt(command Command, attempt int) time.Time {
	return command.At.Add(max(time.Second*time.Duration(1<<(attempt+1)), command.RetryDelay))
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
		if m.OwnsAction(a) && a.Kind == "intervention" && (a.State == Queued || a.State == Started || a.Held()) {
			return true
		}
	}
	return false
}

func (m *Monitor) unresolvedIntervention() bool {
	for _, a := range m.Actions {
		if m.OwnsAction(a) && a.Kind == "intervention" && (a.State == Started || a.Held()) {
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
	episode := identity("outage/" + m.IncidentID)
	if m.NotificationVersion == DeliveryFormatVersion && m.NotificationEpisode != episode && !m.Warning {
		m.setNotificationCorrelation(m.IncidentID)
	}
	m.changeNotificationEpisode(episode, at, emit)
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
	m.changeNotificationEpisode(identity("recovered/"+m.IncidentID), at, emit)
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.Kind == "intervention" && a.State == Queued {
			a.State, a.Outcome, a.FinishedAt = Cancelled, "health_recovered", at
			m.Actions[id] = a
			emit("action_cancelled", &a)
		}
	}
	if m.greenEligible() {
		m.queueColor("green", at, emit)
	}
	if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
		m.IncidentClosedAt = at
	}
}

func (m *Monitor) queueColor(color string, at time.Time, emit func(string, *Action)) {
	if m.Policy.NotificationTriggers[color] != "" {
		return
	}
	m.queueNotification(color, at, nil, emit)
}

func (m *Monitor) queueNotification(color string, at time.Time, failure *RecoveryFailure, emit func(string, *Action)) {
	if m.Ignored || m.pendingAutomaticIgnore(at) || !m.Policy.Enabled || m.snoozed(at) || (m.Dismissed && m.notificationIncident() != "") ||
		m.NotificationVersion == DeliveryFormatVersion && (m.NotificationMaintenance || m.Policy.InMaintenance(at)) {
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
		if m.NotificationVersion == DeliveryFormatVersion {
			return // Suppress the repeat; never release it after its episode ends.
		}
		due = last.Add(m.Policy.Cooldown)
	}
	for n := 0; n < count; n++ {
		m.queueActionWithFailure("code", color, n, due, failure, emit)
	}
	if m.Cooldowns == nil {
		m.Cooldowns = make(map[string]time.Time)
	}
	m.Cooldowns[color] = due
}

func (m *Monitor) queueAction(kind, color string, endpoint int, at time.Time, emit func(string, *Action)) {
	m.queueActionWithFailure(kind, color, endpoint, at, nil, emit)
}

func (m *Monitor) queueActionWithFailure(kind, color string, endpoint int, at time.Time, failure *RecoveryFailure, emit func(string, *Action)) {
	for id, a := range m.Actions {
		if a.Kind != kind || a.Color != color || !m.OwnsAction(a) {
			continue
		}
		if kind == "code" && m.LifecycleVersion == LifecycleFormatVersion {
			if a.EndpointID != notificationEndpointIdentity(*m, color, endpoint) {
				continue
			}
		} else if a.Endpoint != endpoint {
			continue
		}
		if kind == "code" && m.LifecycleVersion == LifecycleFormatVersion && a.NotificationEpisode != m.NotificationEpisode {
			// Keep pending/uncertain historical effects attached to their original
			// delivery; they cannot veto an independent episode or destination.
			if a.State != Queued && a.State != Started && a.State != Unknown && a.Review == nil {
				delete(m.Actions, id)
			}
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
	if m.FileIncarnation != "" {
		id = identity(m.FileIncarnation + "/" + id)
	}
	a := Action{ID: id, CatalogUID: m.CatalogUID, IncidentID: m.notificationIncident(), Revision: m.Revision, Kind: kind, Color: color, Endpoint: endpoint, Attempt: 1, State: Queued, NotBefore: at}
	a.FileIncarnation = m.FileIncarnation
	if kind == "code" && m.NotificationVersion == DeliveryFormatVersion {
		if color == "yellow" || color == "red" {
			a.Cause = m.LastFailure
			if failure != nil {
				a.Cause = m.Actions[failure.ActionID].Failure
			}
		}
		a.QueuedAt = at
		a.NotificationCorrelation = m.NotificationCorrelation
		a.ProviderCorrelationVersion = m.ProviderCorrelationVersion
		a.DeliveryID, a.NotificationEpisode = id, m.NotificationEpisode
		a.EndpointID = notificationEndpointIdentity(*m, color, endpoint)
	}
	if failure != nil {
		v := *failure
		a.RecoveryFailure = &v
	}
	if kind == "intervention" {
		a.Attempt = max(1, m.recoveryAttempts())
		if m.LifecycleVersion == LifecycleFormatVersion {
			budget := m.Policy.RecoveryTimeout
			if budget == 0 {
				budget = 10 * time.Second
			}
			a.QueuedAt, a.Deadline = at, at.Add(budget)
		}
	}
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[id] = a
	emit("action_queued", &a)
}
