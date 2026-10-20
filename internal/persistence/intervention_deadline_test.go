package persistence

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestInterventionDeadlineSurvivesRestart(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "expired"}[expired], func(t *testing.T) {
			s := openCatalogMemory(t)
			config := testMonitor()
			config.Policy = lifecyclePolicy()
			config.Policy.Intervention, config.Policy.RecoveryTimeout = true, time.Minute
			at := policyTime()
			submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})
			m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
			a := interventionAction(t, m)
			if a.QueuedAt != at || a.Deadline != at.Add(time.Minute) {
				t.Fatal("queue did not freeze the configured budget")
			}
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
				t.Fatal(err)
			}
			restarted := at.Add(time.Second)
			if expired {
				restarted = a.Deadline
			}
			submit(t, s, Command{Kind: "recover", At: restarted})
			submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: "failure", ExecutionStart: restarted.Add(time.Millisecond), At: restarted.Add(time.Second)})
			start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: restarted.Add(2 * time.Second)}
			if !expired {
				extended := start
				extended.ActionDeadline = a.Deadline.Add(time.Second)
				if submit(t, s, extended)[0].Err == nil {
					t.Fatal("start extended the original deadline")
				}
			}
			result := submit(t, s, start)[0]
			if result.Allowed == expired {
				t.Fatal("restart did not preserve original eligibility", result.Err)
			}
			current, _ := monitorWithActionHistory(t, s, m.ID)
			if current.Actions[a.ID].Deadline != a.Deadline || expired && current.Actions[a.ID].State != Cancelled {
				t.Fatal("deadline or cancellation was lost")
			}
		})
	}
}

func TestInterventionMigrationDoesNotInventDeadline(t *testing.T) {
	config := testMonitor()
	config.Policy.Intervention, config.Policy.Unhealthy = true, 1
	at := policyTime()
	m := *transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at}, DeliveryFormatVersion).Monitor
	m = *transitionAtFormat(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at}, DeliveryFormatVersion).Monitor
	a := interventionAction(t, m)
	if !a.Deadline.IsZero() {
		t.Fatal("historical replay acquired new semantics")
	}
	result := transition(m, Command{Kind: "recover", At: at.Add(time.Second)})
	if result.Monitor.Actions[a.ID].State != Cancelled || !result.Monitor.Actions[a.ID].Deadline.IsZero() {
		t.Fatal("migration manufactured permission for unbounded old work")
	}
}
