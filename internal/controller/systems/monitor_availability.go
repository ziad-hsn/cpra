package systems

import (
	"context"
	"slices"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// A preparation problem belongs to one unsent selection, not to the monitor's
// entire lifetime. Successful checks and superseding decisions can retire it.
// This is called only by the ECS owner and never reads historical actions.
func interventionPreparationProblem(js *components.JobStorage, m persistence.Monitor) bool {
	if js == nil || js.InterventionPreparationAction == "" {
		return false
	}
	a, ok := m.Actions[js.InterventionPreparationAction]
	if ok && a.State == persistence.Queued && a.Kind == "intervention" && m.OwnsAction(a) && m.ActionConfigurationCurrent(a) &&
		a.Rule != (persistence.RecoverySelection{}) && js.InterventionRuleBinding != a.Rule.Target.Binding {
		return true
	}
	js.InterventionPreparationAction = ""
	js.InterventionPreparationRetry = time.Time{}
	return false
}

// projectAvailability runs only on the owner. It preserves the last target
// result while making local preparation failure explicit to older health readers.
func (s *DurableSystem) projectAvailability(ent ecs.Entity, m persistence.Monitor, at time.Time, summary *fleetview.MonitorSummary) {
	at = at.UTC()
	summary.LastObservedHealth, summary.LastObservedAt = summary.Health, m.LastCheck
	summary.Availability = fleetview.MonitorAvailability{State: "available", ObservedAt: at}
	if m.ConfigurationProblem.Code != "" {
		summary.Health, summary.Status = "unknown", "unknown"
	}
	switch {
	case !m.Policy.Enabled:
		summary.Availability.State, summary.Availability.Reason = "paused", "disabled"
		summary.Status = "disabled"
	case !m.SnoozedUntil.IsZero():
		// A snooze ends through a committed owner transition, not a client clock.
		summary.Availability.State, summary.Availability.Reason = "paused", "snoozed"
	case m.ConfigurationProblem.Code != "":
		summary.Availability.State, summary.Availability.Reason = "unavailable", "configurationProblem"
	case m.LastCheck.IsZero():
		summary.Availability.State, summary.Availability.Reason = "pending", "notObserved"
	}
	state := s.states.Get(ent)
	summary.Maintenance = m.Policy.InMaintenance(at) || manifest.InMaintenance(state.Maintenance, at)
	summary.NotificationsSuppressed = !m.Policy.Enabled || !m.SnoozedUntil.IsZero() || m.Ignored || m.Dismissed || summary.Maintenance
	summary.SuppressionObservedAt = at
	if m.Policy.Intervention && (m.Policy.RecoveryRules.IsZero() || m.Policy.RecoveryRules.HasIntervention()) && m.Policy.Endpoints["blue"] == 0 {
		summary.ConfigurationWarnings = []string{"unknownRecoveryDestination"}
	}
	if !m.Policy.RecoveryRules.IsZero() && m.Policy.Endpoints["red"] > 0 && (m.Policy.NotificationTriggers["red"] == persistence.NotifyRecoveryFailed || m.Policy.NotificationTriggers["red"] == persistence.NotifyRecoveryExhausted) {
		summary.ConfigurationWarnings = append(summary.ConfigurationWarnings, "recoveryOnlyCoverage")
	}
	conditions, truncated := m.NotificationConditions()
	summary.NotificationConditionsTruncated = truncated
	summary.NotificationConditions = make([]fleetview.NotificationCondition, 0, len(conditions))
	for _, condition := range conditions {
		summary.NotificationConditions = append(summary.NotificationConditions, fleetview.NotificationCondition(condition))
	}
	s.logConfigurationWarning(*summary, at)
	if s.observationDeadlines == nil {
		s.observationDeadlines = newControlDeadlines()
	}
	if next := nextMaintenanceBoundary(m.Policy.Maintenance, state.Maintenance, at); !next.IsZero() {
		s.observationDeadlines.Schedule(ent, next)
	} else {
		s.observationDeadlines.Cancel(ent)
	}
}

// Status remains complete even when diagnostic logs are bounded during a large
// import. Only changes are logged, with no targets, credentials or provider text.
func (s *DurableSystem) logConfigurationWarning(summary fleetview.MonitorSummary, at time.Time) {
	if s.logger == nil {
		return
	}
	warn := len(summary.ConfigurationWarnings) != 0
	if s.index != nil {
		if previous, ok := s.index.Get(summary.ID); ok && previous.IncarnationUID == summary.IncarnationUID && slices.Equal(previous.ConfigurationWarnings, summary.ConfigurationWarnings) {
			return
		}
	}
	if !warn {
		return
	}
	if at.Sub(s.warningLogWindow) >= time.Minute || at.Before(s.warningLogWindow) {
		s.warningLogWindow, s.warningLogs = at, 0
	}
	for _, reason := range summary.ConfigurationWarnings {
		if s.warningLogs < 10 {
			s.logger.Warn(api.ValidationMessage(reason), "monitor_id", summary.MonitorID, "monitor_uid", summary.IncarnationUID, "configuration_revision", summary.ConfigurationRevision, "warning_code", reason)
		} else if s.warningLogs == 10 {
			s.logger.Warn("Additional configuration warning transitions omitted from logs; inspect monitor status", "warning_code", "configuration_warnings_truncated")
		}
		if s.warningLogs <= 10 {
			s.warningLogs++
		}
	}
}

// Window boundaries wake the owner even for monitors with a long check interval.
// The heap has one entry per configured monitor, not one per recurring window.
func nextMaintenanceBoundary(absolute []persistence.MaintenanceWindow, recurring []manifest.CompiledWindow, at time.Time) time.Time {
	var next time.Time
	include := func(candidate time.Time) {
		if candidate.After(at) && (next.IsZero() || candidate.Before(next)) {
			next = candidate
		}
	}
	for _, window := range absolute {
		include(window.Start)
		include(window.End)
	}
	for _, window := range recurring {
		include(window.Schedule.Next(at))
		start := window.Schedule.Next(at.Add(-window.Duration))
		if !start.IsZero() && !start.After(at) {
			include(start.Add(window.Duration))
		}
	}
	return next
}

func (s *DurableSystem) refreshMonitorAvailability(at time.Time) {
	if s.index == nil || s.observationDeadlines == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	for processed := 0; processed < 100 && ctx.Err() == nil; processed++ {
		due := s.observationDeadlines.Due(at, 1)
		if len(due) == 0 {
			return
		}
		ent := due[0]
		if !s.world.Alive(ent) {
			continue
		}
		state := s.states.Get(ent)
		m, ok, err := s.store.GetContext(ctx, state.MonitorID)
		if err != nil {
			// Observation contention does not authorize writes or poison admission.
			// Keep the original deadline so the next owner update retries it.
			s.observationDeadlines.Schedule(ent, at)
			return
		}
		if !ok || m.Removed || m.Revision != state.Revision {
			continue
		}
		s.index.Put(s.summaryAt(ent, m, at))
	}
}
