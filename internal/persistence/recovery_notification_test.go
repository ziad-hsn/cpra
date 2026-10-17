package persistence

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecoveryNotificationWaitsForExhaustedAttempts(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.RecoveryMaxAttempts = true, 2
	p.Endpoints = map[string]int{"red": 1}
	// Decode the wire policy so this test also rejects silently ignored triggers.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["notification_triggers"], wire["recovery_driver"] = map[string]string{"red": "recovery_exhausted"}, "docker"
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	for attempt := 1; attempt <= 2; attempt++ {
		a := interventionAction(t, m)
		m = policyStart(t, m, a, a.NotBefore)
		m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), "failure", false)
		count := 0
		for _, action := range m.Actions {
			if action.Kind == "code" {
				count++
				if v := action.RecoveryFailure; v == nil || v.ActionID != a.ID || v.Driver != "docker" || v.Attempt != 2 || v.MaxAttempts != 2 ||
					v.Trigger != NotifyRecoveryExhausted || !v.FailedAt.Equal(a.NotBefore.Add(time.Second)) || action.IncidentID != a.IncidentID {
					t.Fatal("notification lost its original confirmed failure", action)
				}
			}
		}
		if want := attempt - 1; count != want {
			t.Fatalf("attempt %d queued %d notifications; want %d", attempt, count, want)
		}
		if attempt == 1 {
			m = policyPulse(t, m, a.NotBefore.Add(2*time.Second), "failure")
		}
	}
}

func TestRecoveryNotificationUnknownEndpointRemainsHeld(t *testing.T) {
	m, first := recoveryNotificationFixture(t, NotifyRecoveryFailed)
	m = policyOutcome(t, m, first, first.StartedAt.Add(time.Second), "failure", false)
	rows := recoveryNotificationActions(m)
	for _, a := range rows {
		m = policyStart(t, m, a, a.NotBefore)
		m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), "failure", true)
	}
	m = policyPulse(t, m, first.StartedAt.Add(3*time.Second), "failure")
	second := interventionAction(t, m)
	m = policyStart(t, m, second, second.NotBefore)
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore.Add(time.Second), Outcome: "failure"})
	if len(recoveryNotificationActions(*r.Monitor)) != len(rows) {
		t.Fatal("new failure bypassed uncertain endpoint delivery")
	}
	for _, original := range rows {
		a := r.Monitor.Actions[original.ID]
		if !a.Held() || a.RecoveryFailure == nil || a.RecoveryFailure.ActionID != first.ID {
			t.Fatal("unknown endpoint outcome was replaced or repeated")
		}
	}
	for _, event := range r.Events {
		if event.Type == "action_queued" && event.Kind == "code" {
			t.Fatal("unknown delivery created another alert")
		}
	}
}

func recoveryNotificationFixture(t *testing.T, trigger string) (Monitor, Action) {
	t.Helper()
	p := testMonitor().Policy
	p.Intervention, p.RecoveryMaxAttempts, p.RecoveryDriver = true, 2, "docker"
	p.Endpoints = map[string]int{"red": 2}
	p.NotificationTriggers = map[string]string{"red": trigger}
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, a.NotBefore)
	return m, m.Actions[a.ID]
}

func recoveryNotificationActions(m Monitor) []Action {
	var out []Action
	for _, a := range m.Actions {
		if a.Kind == "code" {
			out = append(out, a)
		}
	}
	return out
}

func TestRecoveryNotificationRequiresConfirmedFailure(t *testing.T) {
	for _, outcome := range []string{"unknown", "success", "agent_failure"} {
		t.Run(outcome, func(t *testing.T) {
			m, a := recoveryNotificationFixture(t, NotifyRecoveryFailed)
			if outcome == "agent_failure" {
				a.ExecutorKind = "agent"
				m.Actions[a.ID] = a
			}
			r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID,
				At: a.StartedAt.Add(time.Second), Outcome: outcome, Ambiguous: outcome == "unknown"})
			if len(recoveryNotificationActions(*r.Monitor)) != 0 {
				t.Fatal("unconfirmed intervention caused failure escalation")
			}
		})
	}
}

func TestRecoveryNotificationControlsAndReplay(t *testing.T) {
	for _, control := range []string{"acknowledge", "dismiss", "disable", "snooze"} {
		t.Run(control, func(t *testing.T) {
			m, a := recoveryNotificationFixture(t, NotifyRecoveryFailed)
			switch control {
			case "acknowledge":
				m.AcknowledgedBy, m.AcknowledgedAt = "operator", a.StartedAt
			case "dismiss":
				m.Dismissed = true
			case "disable":
				m.Policy.Enabled = false
			case "snooze":
				m.SnoozedUntil = a.StartedAt.Add(time.Hour)
			}
			c := Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: a.StartedAt.Add(time.Second), Outcome: "failure"}
			r := transition(m, c)
			want := 0
			if control == "acknowledge" {
				want = 2
			}
			if len(recoveryNotificationActions(*r.Monitor)) != want || r.Monitor.Actions[a.ID].State != Failed {
				t.Fatal("control lost the known outcome or admitted suppressed work")
			}
			replayed := transition(*r.Monitor, c)
			if len(replayed.Events) != 0 || replayed.Monitor != nil {
				t.Fatal("duplicate result created another escalation")
			}
		})
	}
}

func TestRecoveryNotificationReceiptAndEndpointIsolation(t *testing.T) {
	m, recovery := recoveryNotificationFixture(t, NotifyRecoveryFailed)
	at := recovery.StartedAt.Add(time.Second)
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at, Outcome: "failure"})
	m = *r.Monitor
	rows := recoveryNotificationActions(m)
	if len(rows) != 2 {
		t.Fatal("each endpoint needs its own queued intent")
	}
	for _, event := range r.Events {
		if event.Type != "action_queued" {
			continue
		}
		if event.RecoveryFailure == nil || event.RecoveryFailure.ActionID != recovery.ID || event.RecoveryFailure.Trigger != NotifyRecoveryFailed {
			t.Fatal("history omitted the chosen trigger and original failure")
		}
		event.RecoveryFailure.Driver = "changed"
		if m.Actions[event.ActionID].RecoveryFailure.Driver != "docker" {
			t.Fatal("history and monitor share mutable failure metadata")
		}
	}
	first, second := rows[0], rows[1]
	m = policyStart(t, m, first, first.NotBefore)
	m = policyOutcome(t, m, first, first.NotBefore.Add(time.Second), "success", false)
	m = policyStart(t, m, second, second.NotBefore)
	c := Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore.Add(time.Second), Outcome: "failure", Retryable: true}
	r = transition(m, c)
	m = *r.Monitor
	if m.Actions[first.ID].State != Succeeded {
		t.Fatal("one rejected endpoint repeated confirmed delivery to another")
	}
	var retry Action
	for _, row := range recoveryNotificationActions(m) {
		if row.Endpoint == second.Endpoint {
			retry = row
		}
	}
	if retry.ID == second.ID || retry.Attempt != 2 || retry.RecoveryFailure == nil || *retry.RecoveryFailure != *second.RecoveryFailure {
		t.Fatal("confirmed notification retry lost original intervention correlation")
	}
	for _, event := range r.Events {
		if event.RecoveryFailure != nil && event.RecoveryFailure.ActionID != recovery.ID {
			t.Fatal("notification failure recursively became a recovery failure")
		}
	}
	// A second intervention can notify an already completed endpoint, while an
	// unfinished/unknown delivery retains the existing endpoint hold.
	m = policyPulse(t, m, at.Add(2*time.Second), "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, a.NotBefore)
	m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), "failure", false)
	if len(recoveryNotificationActions(m)) != 2 || m.Actions[retry.ID].RecoveryFailure.ActionID != recovery.ID {
		t.Fatal("per-attempt policy duplicated pending endpoint work")
	}
}

func TestRecoveryNotificationConfigurationCancelsOriginalIntent(t *testing.T) {
	m, a := recoveryNotificationFixture(t, NotifyRecoveryFailed)
	m = policyOutcome(t, m, a, a.StartedAt.Add(time.Second), "failure", false)
	rows := recoveryNotificationActions(m)
	m = policyStart(t, m, rows[0], rows[0].NotBefore)
	config := testMonitor()
	config.Revision = "replacement"
	r := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: a.StartedAt.Add(3 * time.Second)})
	for _, prior := range rows {
		got := r.Monitor.Actions[prior.ID]
		want := Cancelled
		if prior.ID == rows[0].ID {
			want = Unknown
		}
		if got.State != want || got.Revision != prior.Revision || !reflect.DeepEqual(got.RecoveryFailure, prior.RecoveryFailure) {
			t.Fatal("configuration replacement redirected the failure notification")
		}
	}
}

func TestRecoveryNotificationPolicyValidation(t *testing.T) {
	m, _ := recoveryNotificationFixture(t, NotifyRecoveryFailed)
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.Intervention = false },
		func(p *Policy) { p.RecoveryDriver = "" },
		func(p *Policy) { p.NotificationTriggers["red"] = "unknown" },
		func(p *Policy) { p.NotificationTriggers = map[string]string{"green": NotifyRecoveryFailed} },
	} {
		p := m.Policy.Clone()
		mutate(&p)
		if p.Validate() == nil {
			t.Fatal("invalid escalation policy accepted")
		}
	}
	if m.Policy.NotificationTriggers["red"] != NotifyRecoveryFailed {
		t.Fatal("policy clone aliases original trigger map")
	}
}

func TestRecoveryNotificationSnapshotAndFormatBoundary(t *testing.T) {
	s := openCatalogMemory(t)
	m, _ := recoveryNotificationFixture(t, NotifyRecoveryFailed)
	config := testMonitor()
	config.Policy = m.Policy
	c := Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: policyTime()}
	if err := validateCommand(c); err != nil || commandWriteFormat(c) != LifecycleFormatVersion || commandMinimumFormat(c) != RecoveryNotificationFormatVersion {
		t.Fatal("configuration has no explicit format boundary", err)
	}
	raw, _ := json.Marshal(envelope{Version: AgentActionFormatVersion, Commands: []Command{c}})
	if _, err := decodeEnvelope(raw); err == nil {
		t.Fatal("old command format accepted changed notification semantics")
	}
	submit(t, s, c)
	submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, Outcome: "failure", At: policyTime().Add(time.Second)})
	m, _ = s.Get(config.ID)
	a := interventionAction(t, m)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: a.NotBefore})
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: a.NotBefore.Add(time.Second), Outcome: "failure"})
	i, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != LifecycleFormatVersion || len(recoveryNotificationActions(i.Monitors[m.ID])) != 2 {
		t.Fatal("snapshot lost committed failure notification policy or intents")
	}
	for _, row := range recoveryNotificationActions(i.Monitors[m.ID]) {
		if row.RecoveryFailure == nil || row.RecoveryFailure.ActionID != a.ID {
			t.Fatal("snapshot lost original failure correlation")
		}
	}
	i.Version = AgentActionFormatVersion
	old, _ := json.Marshal(i)
	if _, err := decodeImage(strings.NewReader(string(old))); err == nil {
		t.Fatal("old snapshot format accepted failure notifications")
	}
	i.Version = DeliveryFormatVersion
	for id, row := range i.Monitors[m.ID].Actions {
		if row.RecoveryFailure != nil {
			row.RecoveryFailure.ActionID = ""
			i.Monitors[m.ID].Actions[id] = row
			break
		}
	}
	corrupt, _ := json.Marshal(i)
	if _, err := decodeImage(strings.NewReader(string(corrupt))); err == nil {
		t.Fatal("corrupt source action was silently restored")
	}
}
