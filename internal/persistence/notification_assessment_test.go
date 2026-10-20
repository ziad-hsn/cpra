package persistence

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestExpiredNotificationIsNotReauthorized(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := notificationByColor(t, m, "red", Queued)
	r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: a.Deadline})
	if r.Allowed || r.Monitor == nil || r.Monitor.Actions[a.ID].Outcome != "notification_deadline" {
		t.Fatal("expired intent was admitted")
	}
	m = *r.Monitor
	for n := 1; n <= 5; n++ {
		m = policyPulse(t, m, a.Deadline.Add(time.Duration(n)*time.Second), "failure")
	}
	if queuedColorCount(m, "red") != 0 {
		t.Fatal("ongoing condition acquired a new execution budget")
	}
	m = policyPulse(t, m, a.Deadline.Add(6*time.Second), "success")
	m = policyPulse(t, m, a.Deadline.Add(7*time.Second), "success")
	m = policyPulse(t, m, a.Deadline.Add(8*time.Second), "failure")
	if queuedColorCount(m, "red") != 1 {
		t.Fatal("expired earlier delivery blocked a distinct outage")
	}
}

func TestExposureMigrationHasDeterministicOrdering(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := notificationByColor(t, m, "red", Queued)
	a.State, a.StartedAt = Unknown, at
	b := a.Clone()
	b.ID, b.StartedAt = "second", at.Add(time.Second)
	b.RecoveryFailure = &RecoveryFailure{ActionID: "failed-recovery"}
	var expected []byte
	for n := range 30 {
		copy := m.Clone()
		copy.Notifications = nil
		copy.Actions = make(map[string]Action)
		if n%2 == 0 {
			copy.Actions[a.ID], copy.Actions[b.ID] = a, b
		} else {
			copy.Actions[b.ID], copy.Actions[a.ID] = b, a
		}
		copy.upgradeNotificationState()
		encoded, err := json.Marshal(copy.Notifications)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			expected = encoded
		} else if !bytes.Equal(expected, encoded) {
			t.Fatal("migration depends on map ordering")
		}
		d := copy.Notifications.Deliveries[deliverySlot(b.Color, b.EndpointID)]
		if d.Condition != "failed-recovery" || !d.StartedAt.Equal(b.StartedAt) {
			t.Fatal("latest proven start was lost")
		}
	}
	m.Notifications, m.Actions = nil, map[string]Action{a.ID: a}
	a.StartedAt, a.Attempt = time.Time{}, 2
	m.Actions[a.ID] = a
	m.upgradeNotificationState()
	d := m.Notifications.Deliveries[deliverySlot(a.Color, a.EndpointID)]
	if !d.Started || !d.StartedAt.IsZero() {
		t.Fatal("migration fabricated an unavailable start timestamp")
	}
}

func TestRecoveredMaintenanceOutageDoesNotSuppressFutureWarning(t *testing.T) {
	p := lifecyclePolicy()
	p.Endpoints["yellow"] = 1
	at := policyTime()
	p.Maintenance = []MaintenanceWindow{{Start: at, End: at.Add(10 * time.Second)}}
	m := configuredPolicyMonitor(t, p)
	m = policyPulse(t, m, at.Add(time.Second), "failure")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	r := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at.Add(11 * time.Second), Outcome: "success", Warning: true})
	if r.Monitor == nil || queuedColorCount(*r.Monitor, "yellow") != 1 || queuedColorCount(*r.Monitor, "red") != 0 {
		t.Fatal("closed maintenance outage suppressed a new warning")
	}
}

func queuedColorCount(m Monitor, color string) int {
	n := 0
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == color && a.State == Queued {
			n++
		}
	}
	return n
}

func TestOngoingOutageAssessedAfterMaintenance(t *testing.T) {
	p := lifecyclePolicy()
	p.Unhealthy = 2
	p.AutoIgnore = &AutoIgnorePolicy{Incidents: 2, Window: time.Hour}
	at := policyTime()
	p.Maintenance = []MaintenanceWindow{{Start: at, End: at.Add(10 * time.Second)}}
	m := configuredPolicyMonitor(t, p)
	for n := 1; n <= 3; n++ {
		m = policyPulse(t, m, at.Add(time.Duration(n)*time.Second), "failure")
	}
	if !m.Incident || len(m.IgnoreIncidents) != 0 || queuedColorCount(m, "red") != 0 {
		t.Fatal("maintenance outage must retain health without counting or notifying")
	}
	m = policyPulse(t, m, at.Add(11*time.Second), "failure")
	if len(m.IgnoreIncidents) != 0 || queuedColorCount(m, "red") != 0 {
		t.Fatal("maintenance-established outage skipped the fresh outside threshold")
	}
	m = policyPulse(t, m, at.Add(12*time.Second), "failure")
	if len(m.IgnoreIncidents) != 1 || queuedColorCount(m, "red") != 1 {
		t.Fatal("continuing outage did not become eligible after fresh threshold confirmation")
	}
	a := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, a, at.Add(12*time.Second))
	m = policyOutcome(t, m, a, at.Add(13*time.Second), "success", false)
	m = policyPulse(t, m, at.Add(2*time.Hour), "failure")
	if queuedColorCount(m, "red") != 0 || len(m.IgnoreIncidents) != 1 {
		t.Fatal("reassessment repeated an already attempted delivery or counted the same outage twice")
	}
}

func TestHealthyIgnoreWaitsForQualifyingOutage(t *testing.T) {
	p := lifecyclePolicy()
	p.Unhealthy, p.Healthy = 2, 2
	m := configuredPolicyMonitor(t, p)
	at := policyTime()
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m.enterIgnore(at.Add(2*time.Second), "operator", "operator", "watching", func(string, *Action) {})
	for n := 3; n < 6; n++ {
		m = policyPulse(t, m, at.Add(time.Duration(n)*time.Second), "success")
	}
	if !m.Ignored || queuedColorCount(m, "green") != 0 {
		t.Fatal("healthy observations manufactured an Ignore recovery")
	}
	m = policyPulse(t, m, at.Add(6*time.Second), "failure")
	m = policyPulse(t, m, at.Add(7*time.Second), "success")
	m = policyPulse(t, m, at.Add(8*time.Second), "success")
	if !m.Ignored || queuedColorCount(m, "green") != 0 {
		t.Fatal("sub-threshold failure manufactured an Ignore recovery")
	}
	m = policyPulse(t, m, at.Add(9*time.Second), "failure")
	m = policyPulse(t, m, at.Add(10*time.Second), "failure")
	m = policyPulse(t, m, at.Add(11*time.Second), "success")
	if !m.Ignored {
		t.Fatal("Ignore ended before the healthy threshold")
	}
	m = policyPulse(t, m, at.Add(12*time.Second), "success")
	if m.Ignored || queuedColorCount(m, "green") != 1 {
		t.Fatal("qualifying outage did not complete Ignore recovery")
	}
}

func TestUnignoreReassessesOnlyCurrentOutage(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "ongoing", true: "recovered"}[recovered], func(t *testing.T) {
			m := configuredPolicyMonitor(t, lifecyclePolicy())
			at := policyTime()
			m = policyPulse(t, m, at.Add(time.Second), "failure")
			old := notificationByColor(t, m, "red", Queued)
			m.enterIgnore(at.Add(2*time.Second), "operator", "operator", "watching", func(string, *Action) {})
			if recovered {
				m = policyPulse(t, m, at.Add(3*time.Second), "success")
				m = policyPulse(t, m, at.Add(4*time.Second), "success")
			} else {
				m.leaveIgnore(at.Add(4*time.Second), false, func(string, *Action) {})
			}
			outcome := "failure"
			if recovered {
				outcome = "success"
			}
			m = policyPulse(t, m, at.Add(5*time.Second), outcome)
			if m.Actions[old.ID].State == Queued || queuedColorCount(m, "red") != map[bool]int{false: 1, true: 0}[recovered] {
				t.Fatal("suppression exit lost the continuing outage or replayed stale work")
			}
		})
	}
}
