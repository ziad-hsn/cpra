package persistence

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestNotificationDeadlineStartsAtIntent(t *testing.T) {
	s := openCatalogMemory(t)
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention = false
	config.Policy.Endpoints = map[string]int{"red": 1}
	config.Policy.NotificationTimeouts = map[string][]time.Duration{"red": {time.Minute}}
	at := policyTime()
	submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
	var a Action
	for _, candidate := range m.Actions {
		if candidate.Color == "red" {
			a = candidate
		}
	}
	if a.ID == "" || !a.Deadline.Equal(at.Add(time.Minute)) {
		t.Fatal("notification budget not frozen at intent", a)
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	submit(t, s, Command{Kind: "recover", At: at.Add(time.Minute)})
	r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, ActionDeadline: at.Add(2 * time.Minute), At: at.Add(time.Minute)})[0]
	if r.Allowed {
		t.Fatal("expired queue acquired a new budget")
	}
	current := retainedAction(t, s, a.ID)
	if current.State != Cancelled || !current.Deadline.Equal(a.Deadline) {
		t.Fatal("original deadline or cancellation lost")
	}
}

func TestActionMigrationDoesNotInventNotificationBudget(t *testing.T) {
	m, delivered, pending := notificationTransitionFixtureAtFormat(t, Queued, DeliveryFormatVersion)
	command := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: policyTime().Add(1250 * time.Millisecond), Outcome: "failure"}
	old := transitionAtFormat(m, command, DeliveryFormatVersion)
	if old.Monitor.Actions[pending.ID].State != Queued {
		t.Fatal("historical replay changed")
	}
	current := transition(m, command)
	if a := current.Monitor.Actions[pending.ID]; a.State != Cancelled || !a.Deadline.IsZero() || a.Outcome != "notification_deadline_unrecorded" {
		t.Fatal("migration granted a fresh budget")
	}
	if current.Monitor.Actions[delivered.ID].State != Succeeded {
		t.Fatal("migration changed delivered evidence")
	}
}

func TestRecoveryRetryRequiresTransientProofAndOriginalBudget(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent", true: "transient"}[retryable], func(t *testing.T) {
			config := testMonitor()
			config.Policy = lifecyclePolicy()
			config.Policy.Intervention, config.Policy.RecoveryMaxAttempts, config.Policy.RecoveryTimeout = true, 3, time.Minute
			at := policyTime()
			m := *transition(Monitor{}, Command{Kind: "configure", Config: &config, At: at}).Monitor
			m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at}).Monitor
			a := interventionAction(t, m)
			m = *transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at}).Monitor
			m = *transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "failure", Retryable: retryable, At: at.Add(time.Second)}).Monitor
			m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: "failure", At: at.Add(2 * time.Second)}).Monitor
			var next Action
			for _, candidate := range m.Actions {
				if candidate.Kind == "intervention" && candidate.State == Queued {
					next = candidate
				}
			}
			if (next.ID != "") != retryable {
				t.Fatal("retry permission did not match proof", next)
			}
			if retryable && (!next.Deadline.Equal(a.Deadline) || !next.QueuedAt.Equal(a.QueuedAt)) {
				t.Fatal("retry refreshed original budget")
			}
		})
	}
}

func TestManualRecoveryAfterExpiredAttemptHasNewBudget(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := policyTime().Add(2 * time.Second)
	m = *mustResult(t, s, manualCommand(m, g, "first-manual", at)).Monitor
	first := latestManual(t, m)
	m = finishManualFailure(t, s, m, g, at.Add(time.Millisecond))
	later := at.Add(2 * time.Minute)
	m = *mustResult(t, s, manualCommand(m, g, "second-manual", later)).Monitor
	next := latestManual(t, m)
	if !next.Manual || !next.Deadline.After(first.Deadline) || !next.QueuedAt.Equal(later) {
		t.Fatal("fresh audited authorization inherited the expired automatic budget")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNoEffectRetryAndLateEvidenceSurviveSnapshot(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "late"}[late], func(t *testing.T) {
			s := openCatalogMemory(t)
			config := testMonitor()
			config.Policy = lifecyclePolicy()
			config.Policy.Intervention = false
			config.Policy.Endpoints = map[string]int{"red": 1}
			at := policyTime()
			m := *submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})[0].Monitor
			m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
			var a Action
			for _, candidate := range m.Actions {
				if candidate.Color == "red" {
					a = candidate
				}
			}
			sealed, err := agentObservationSealer(t).Seal(t.Context(), AgentConnectionBinding(s.Status().NodeID, m.ID, a), []byte(agentObservationSecret))
			if err != nil {
				t.Fatal(err)
			}
			h, err := s.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at, Driver: "a2a", AgentConnection: &AgentConnection{CatalogUID: a.CatalogUID, Payload: sealed}})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			command := Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "failure", Retryable: true}
			if late {
				submit(t, s, Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Millisecond)})
				command.Kind, command.Retryable = "late_result", false
				command.ExecutionStart, command.ExecutionEnd = at, at.Add(time.Second)
			}
			r := submit(t, s, command)[0]
			if r.Err != nil || r.Monitor == nil {
				t.Fatal("known no-effect result rejected", r.Err)
			}
			m = *r.Monitor
			if late {
				a = m.Actions[a.ID]
				if a.State != Unknown || !a.Held() || a.LateEvidence == nil || a.LateEvidence.Outcome != "rejected" {
					t.Fatal("late evidence changed provider hold")
				}
			} else {
				var retry Action
				for _, candidate := range m.Actions {
					if candidate.Color == "red" && candidate.State == Queued {
						retry = candidate
					}
				}
				if retry.ID == "" || retry.ID == a.ID || retry.AgentConnection != nil || retry.AgentReceipt != nil || !retry.Deadline.Equal(a.Deadline) {
					t.Fatal("retry carried stale agent authority or lost budget")
				}
			}
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
				t.Fatal(err)
			}
		})
	}
}
