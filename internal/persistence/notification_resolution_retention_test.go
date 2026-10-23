package persistence

import (
	"fmt"
	"testing"
	"time"
)

func exposedResolution(t *testing.T) (Monitor, Action, NotificationResolution) {
	t.Helper()
	m := conditionMonitor(t)
	at := policyTime()
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "failure"})
	a := notificationByColor(t, m, "red", Queued)
	m = conditionTransition(t, m, Command{Kind: "start", At: at.Add(2 * time.Second), ActionID: a.ID})
	m = conditionTransition(t, m, Command{Kind: "result", At: at.Add(3 * time.Second), ActionID: a.ID, Outcome: "success"})
	if len(m.Notifications.Resolutions) != 1 {
		t.Fatal("missing original resolution")
	}
	for _, v := range m.Notifications.Resolutions {
		return m, m.Actions[a.ID], v
	}
	panic("unreachable")
}

func TestObsoleteNotificationResolutionRecordsCancellationWithoutInventingAction(t *testing.T) {
	for _, scenario := range []string{"before-recovery", "suppressed-recovery", "removed", "queued-recovery"} {
		t.Run(scenario, func(t *testing.T) {
			m, opening, condition := exposedResolution(t)
			at := policyTime()
			if scenario == "suppressed-recovery" {
				m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(4 * time.Second), End: at.Add(time.Hour)}}
			}
			if scenario == "suppressed-recovery" || scenario == "queued-recovery" {
				for _, second := range []int{5, 6} {
					m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
				}
			}
			old := m.Notifications.Resolutions[condition.ID]
			config := m.Clone()
			config.Revision = "replacement"
			command := Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(7 * time.Second)}
			if scenario == "removed" {
				command = Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, At: command.At}
			}
			r := transitionAtFormat(m, command, NotificationConditionFormatVersion)
			if r.Err != nil || r.Monitor == nil {
				t.Fatal(r.Err)
			}
			m = *r.Monitor
			if len(m.Notifications.Resolutions) != 0 || m.notificationRetainsAction(opening.ID) {
				t.Fatal("obsolete condition still pins ordinary history", m.Notifications.Resolutions)
			}
			if got := m.Actions[opening.ID]; got.State != Succeeded || got.StartedAt != opening.StartedAt || got.DeliveryID != opening.DeliveryID {
				t.Fatal("cancellation changed the opening provider fact")
			}
			var cancellation *Event
			for _, event := range r.Events {
				if event.Type == "notification_resolution_cancelled" {
					if cancellation != nil {
						t.Fatal("duplicated condition cancellation")
					}
					cancellation = &event
				}
			}
			if cancellation == nil || cancellation.ConditionID != condition.ID || cancellation.OpeningActionID != opening.ID ||
				cancellation.ActionID != old.ActionID || cancellation.IncidentID != opening.IncidentID || cancellation.Revision != opening.Revision || cancellation.Resolution != nil {
				t.Fatal("condition cancellation lost ownership or invented recovery/action evidence", cancellation)
			}
			if old.ActionID != "" && m.Actions[old.ActionID].State != Cancelled {
				t.Fatal("changed target left a queued closure executable")
			}
			if scenario == "removed" {
				config = conditionMonitor(t)
				r = transitionAtFormat(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(8 * time.Second)}, NotificationConditionFormatVersion)
				if r.Err != nil || len(r.Monitor.Notifications.Resolutions) != 0 || r.Monitor.FileIncarnation == m.FileIncarnation {
					t.Fatal("re-added monitor retained old condition/incarnation", r.Err)
				}
			}
		})
	}
}

func TestCancelledStartedResolutionLeavesHotStateAfterLaterOutage(t *testing.T) {
	m, _, condition := exposedResolution(t)
	at := policyTime()
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(9 * time.Second), End: at.Add(20 * time.Second)}}
	for _, second := range []int{4, 5} {
		m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
	}
	green := notificationByColor(t, m, "green", Queued)
	m = conditionTransition(t, m, Command{Kind: "start", At: at.Add(6 * time.Second), ActionID: green.ID})
	m = conditionTransition(t, m, Command{Kind: "result", At: at.Add(7 * time.Second), ActionID: green.ID, Outcome: "failure", Retryable: true})
	completed := m.retainedActions[green.ID]
	if completed.State != Failed || completed.Deadline.IsZero() {
		t.Fatal("completed attempt was not handed to the retained action ledger")
	}
	retry := notificationByColor(t, m, "green", Queued)
	m = conditionTransition(t, m, Command{Kind: "start", At: retry.NotBefore, ActionID: retry.ID})
	if m.Actions[retry.ID].State != Cancelled || !m.Notifications.Resolutions[condition.ID].Started {
		t.Fatal("fixture did not cancel the previously started delivery")
	}
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(21 * time.Second), Outcome: "failure"})
	if _, exists := m.Notifications.Resolutions[condition.ID]; exists {
		t.Fatal("obsolete canceled execution remained in per-check state")
	}
	if m.Actions[retry.ID].State != Cancelled || m.Actions[retry.ID].Deadline != completed.Deadline {
		t.Fatal("retirement modified execution facts or renewed the original deadline")
	}
}

func TestResolutionCleanupProtectsActiveAndUncertainBindings(t *testing.T) {
	for _, state := range []ActionState{Started, Unknown, Queued} {
		t.Run(string(state), func(t *testing.T) {
			m, _, v := exposedResolution(t)
			v.State = state
			if state == Queued {
				v.MigrationReason = "notification_identity_incomplete"
			}
			m.Notifications.Resolutions[v.ID] = v
			m.Revision = "changed"
			events := m.cancelObsoleteResolutions(policyTime().Add(time.Minute), func(string, *Action) { t.Fatal("unexpected action transition") })
			if len(events) != 0 || len(m.Notifications.Resolutions) != 1 {
				t.Fatal("cleanup lost protected uncertainty")
			}
		})
	}
}

func TestResolutionConfigurationChangesDoNotAccumulateHistoricalConditions(t *testing.T) {
	for _, count := range []int{0, 50, 500} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := conditionMonitor(t)
			for i := 0; i < count; i++ {
				at := policyTime().Add(time.Duration(i+1) * time.Minute)
				m.captureResolutionTargets(fmt.Sprintf("episode-%d", i), fmt.Sprintf("opening-%d", i), at, m.ProviderCorrelationVersion)
				config := m.Clone()
				config.Revision = fmt.Sprintf("configuration-%d", i)
				r := transitionAtFormat(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(time.Second)}, NotificationConditionFormatVersion)
				if r.Err != nil || r.Monitor == nil {
					t.Fatal(r.Err)
				}
				m = *r.Monitor
				if len(m.Notifications.Resolutions) != 0 || len(r.Events) != 1 || r.Events[0].Type != "notification_resolution_cancelled" {
					t.Fatal("configuration replacements retained obsolete per-check state", i, len(m.Notifications.Resolutions))
				}
			}
		})
	}
}

func TestIgnoreRecoveryExplicitlyAuthorizesResolutionWithoutOpening(t *testing.T) {
	m := conditionMonitor(t)
	at := policyTime()
	m.enterIgnore(at, "operator", "operator", "investigating", func(string, *Action) {})
	for second := 1; second <= m.Policy.Healthy; second++ {
		m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
	}
	if !m.Ignored || len(m.Notifications.Resolutions) != 0 || queuedColorCount(m, "green") != 0 {
		t.Fatal("healthy Ignore manufactured recovery permission")
	}
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(5 * time.Second), Outcome: "failure"})
	for second := 6; second < 6+m.Policy.Healthy; second++ {
		m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
	}
	green := notificationByColor(t, m, "green", Queued)
	if m.Ignored || green.Resolution == nil || !green.Deadline.IsZero() || green.ExecutionBudget != NotificationTimeout {
		t.Fatal("verified Ignore recovery did not authorize one unstarted resolution")
	}
	condition := m.Notifications.Resolutions[green.Resolution.ConditionID]
	if condition.OpeningActionID != "" || condition.Started || condition.RecoveredAt.IsZero() || condition.Revision != m.Revision {
		t.Fatal("Ignore exception invented an opening or lost its current configuration")
	}
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Minute), Outcome: "success"})
	if next := notificationByColor(t, m, "green", Queued); next.ID != green.ID || len(m.Notifications.Resolutions) != 1 {
		t.Fatal("later observations repeated the Ignore exception")
	}
}
