package persistence

import (
	"encoding/json"
	"testing"
	"time"
)

func conditionTransition(t *testing.T, m Monitor, c Command) Monitor {
	t.Helper()
	c.MonitorID, c.Revision = m.ID, m.Revision
	if c.Kind == "pulse" {
		c.Generation = m.Generation + 1
	}
	if c.Kind == "start" {
		c.NoticeRevision = ActionNoticeRevision(m.Actions[c.ActionID])
	}
	r := transitionAtFormat(m, c, NotificationConditionFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("condition transition failed", r.Err)
	}
	return *r.Monitor
}

func conditionMonitor(t *testing.T) Monitor {
	t.Helper()
	m := testMonitor()
	m.Policy = lifecyclePolicy()
	m.Policy.Intervention = false
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
	r := transitionAtFormat(Monitor{}, c, NotificationConditionFormatVersion)
	if r.Err != nil || r.Monitor == nil || !r.Allowed {
		t.Fatal("configuration failed", r.Err)
	}
	return *r.Monitor
}

func TestNotificationConditionBudgetStartsAtCommittedExecution(t *testing.T) {
	m := conditionMonitor(t)
	at := policyTime().Add(time.Second)
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at, Outcome: "failure"})
	a := notificationByColor(t, m, "red", Queued)
	if a.ExecutionBudget != 45*time.Second || !a.Deadline.IsZero() {
		t.Fatal("queue admission consumed the notification budget", a)
	}
	// Waiting for a serialized destination and a process restart grant no work.
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m = conditionTransition(t, m, Command{Kind: "recover", At: at.Add(2 * time.Minute)})
	started := at.Add(3 * time.Minute)
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: started})
	a = m.Actions[a.ID]
	if a.State != Started || !a.Deadline.Equal(started.Add(45*time.Second)) {
		t.Fatal("first committed start did not allocate the captured budget", a)
	}
	deadline := a.Deadline
	m = conditionTransition(t, m, Command{Kind: "result", ActionID: a.ID, At: started.Add(time.Second), Outcome: "failure", Retryable: true})
	retry := notificationByColor(t, m, "red", Queued)
	if retry.Attempt != 2 || retry.Deadline != deadline || retry.DeliveryID != a.DeliveryID {
		t.Fatal("safe retry changed deadline or delivery identity", retry)
	}
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: retry.ID, At: retry.NotBefore})
	if m.Actions[retry.ID].Deadline != deadline {
		t.Fatal("retry renewed execution budget")
	}
}

func TestNotificationConditionPreservesHistoricalDeadline(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := notificationByColor(t, m, "red", Queued)
	deadline := a.Deadline
	m = conditionTransition(t, m, Command{Kind: "recover", At: deadline.Add(time.Second)})
	if got := m.Actions[a.ID]; got.ExecutionBudget != 0 || got.Deadline != deadline {
		t.Fatal("upgrade renewed an existing action", got)
	}
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: deadline.Add(2 * time.Second)})
	if got := m.Actions[a.ID]; got.State != Cancelled || got.Deadline != deadline {
		t.Fatal("expired historical permission executed", got)
	}
}

func TestNotificationConditionUnknownStartIsNeverRepeated(t *testing.T) {
	m := conditionMonitor(t)
	at := policyTime().Add(time.Second)
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at, Outcome: "failure"})
	a := notificationByColor(t, m, "red", Queued)
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: at.Add(time.Minute)})
	deadline := m.Actions[a.ID].Deadline
	m = conditionTransition(t, m, Command{Kind: "recover", At: at.Add(2 * time.Minute)})
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(3 * time.Minute), Outcome: "failure"})
	if got := m.Actions[a.ID]; got.State != Unknown || got.Deadline != deadline || queuedColorCount(m, "red") != 0 {
		t.Fatal("unknown start was renewed or repeated", got)
	}
}

func TestNotificationConditionRejectsBudgetInOldFormat(t *testing.T) {
	m := conditionMonitor(t)
	m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Second), Outcome: "failure"})
	a := notificationByColor(t, m, "red", Queued)
	if err := validateNotificationAction(a, ActionFormatVersion, DeliveryFormatVersion); err == nil {
		t.Fatal("old format accepted new budget semantics")
	}
	if err := validateNotificationAction(a, NotificationConditionFormatVersion, DeliveryFormatVersion); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationResolutionSurvivesMaintenanceAndLaterOutage(t *testing.T) {
	m := conditionMonitor(t)
	at := policyTime()
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(5 * time.Second), End: at.Add(20 * time.Second)}}
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "failure"})
	opening := notificationByColor(t, m, "red", Queued)
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: opening.ID, At: at.Add(2 * time.Second)})
	m = conditionTransition(t, m, Command{Kind: "result", ActionID: opening.ID, At: at.Add(3 * time.Second), Outcome: "success"})
	for _, second := range []int{6, 7} {
		m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
	}
	if m.Incident || queuedColorCount(m, "green") != 0 || len(m.Notifications.Resolutions) != 1 {
		t.Fatal("recovery during maintenance lost the earlier resolution", m.Notifications)
	}
	// Another outage starts before maintenance ends. The pending resolution must
	// retain its original destination/correlation and describe current health.
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(10 * time.Second), Outcome: "failure"})
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(21 * time.Second), Outcome: "failure"})
	closing := notificationByColor(t, m, "green", Queued)
	if closing.NotificationCorrelation != opening.NotificationCorrelation || closing.IncidentID != opening.IncidentID || closing.Resolution == nil || closing.Resolution.Health != "unhealthy" {
		t.Fatal("resolution lost its earlier incident or misreported current health", closing)
	}
	m = conditionTransition(t, m, Command{Kind: "start", ActionID: closing.ID, At: at.Add(22 * time.Second)})
	if m.Actions[closing.ID].State != Started {
		t.Fatal("eligible earlier resolution did not start", m.Actions[closing.ID])
	}
}

func TestPendingOpeningRecoveredBeforeStartIsCanceled(t *testing.T) {
	m := conditionMonitor(t)
	at := policyTime()
	m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "failure"})
	opening := notificationByColor(t, m, "red", Queued)
	for _, second := range []int{60, 61} {
		m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Duration(second) * time.Second), Outcome: "success"})
	}
	if m.Actions[opening.ID].State != Cancelled || queuedColorCount(m, "green") != 0 {
		t.Fatal("unexposed episode produced a stale opening or recovery")
	}
}

func TestRecoveryTerminationExplainsIndependentLimits(t *testing.T) {
	for _, scenario := range []string{"permanent", "late_deadline", "unknown", "queued_deadline_after_restart"} {
		t.Run(scenario, func(t *testing.T) {
			m := conditionMonitor(t)
			m.Policy.Intervention, m.Policy.RecoveryMaxAttempts, m.Policy.RecoveryDriver = true, 3, "webhook"
			m.Policy.RecoveryCooldown = 0
			m.Policy.NotificationTriggers = map[string]string{"red": NotifyRecoveryExhausted}
			m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Second), Outcome: "failure"})
			a := interventionAction(t, m)
			if scenario == "queued_deadline_after_restart" {
				m = conditionTransition(t, m, Command{Kind: "recover", At: a.Deadline.Add(time.Second)})
				m = conditionTransition(t, m, Command{Kind: "pulse", At: a.Deadline.Add(2 * time.Second), Outcome: "failure"})
			} else {
				m = conditionTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: a.NotBefore})
				m = conditionTransition(t, m, Command{Kind: "result", ActionID: a.ID, At: a.NotBefore.Add(time.Millisecond), Outcome: "failure", Retryable: scenario == "late_deadline", Ambiguous: scenario == "unknown"})
				if scenario == "late_deadline" {
					if m.Actions[a.ID].TerminationReason != "" || queuedColorCount(m, "red") != 0 {
						t.Fatal("retryable result was prematurely exhausted")
					}
					// Terminal rows leave the hot projection; snapshots omit its empty
					// action map. A later budget assessment must materialize safely.
					failed := m.Actions[a.ID]
					m.ActionProjection = &ActionProjection{Version: actionProjectionVersion, LatestRecovery: &failed}
					m.Actions = nil
					raw, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &m); err != nil {
						t.Fatal(err)
					}
					m = conditionTransition(t, m, Command{Kind: "pulse", At: a.Deadline.Add(time.Second), Outcome: "failure"})
				}
			}
			if scenario == "unknown" {
				if m.Actions[a.ID].State != Unknown || queuedColorCount(m, "red") != 0 {
					t.Fatal("uncertain intervention became a confirmed failure")
				}
				return
			}
			want := "execution_deadline"
			if scenario == "permanent" {
				want = "permanent_rejection"
			}
			if got := m.Actions[a.ID]; got.TerminationReason != want {
				t.Fatalf("termination=%q want=%q", got.TerminationReason, want)
			}
			alert := notificationByColor(t, m, "red", Queued)
			if alert.RecoveryFailure == nil || alert.RecoveryFailure.TerminationReason != want || alert.RecoveryFailure.Attempt != 1 || alert.RecoveryFailure.MaxAttempts != 3 {
				t.Fatal("notification did not explain the binding limit", alert.RecoveryFailure)
			}
			if err := validateRecoveryNotificationAction(NotificationConditionFormatVersion, alert); err != nil {
				t.Fatal(err)
			}
		})
	}
}
