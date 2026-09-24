package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func policyTime() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }

func configuredPolicyMonitor(t *testing.T, p Policy) Monitor {
	t.Helper()
	m := testMonitor()
	m.Policy = p.Clone()
	r := transition(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	if !r.Allowed || r.Err != nil || r.Monitor == nil {
		t.Fatal("configuration failed", r.Err)
	}
	return *r.Monitor
}

func policyPulse(t *testing.T, m Monitor, at time.Time, outcome string) Monitor {
	t.Helper()
	r := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at, Outcome: outcome})
	if r.Monitor == nil {
		t.Fatal("pulse did not update monitor")
	}
	return *r.Monitor
}

func interventionAction(t *testing.T, m Monitor) Action {
	t.Helper()
	var found []Action
	for _, action := range m.Actions {
		if action.CatalogUID == m.CatalogUID && action.Kind == "intervention" {
			found = append(found, action)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one current intervention action, got %d", len(found))
	}
	return found[0]
}

func policyStart(t *testing.T, m Monitor, action Action, at time.Time) Monitor {
	t.Helper()
	r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at})
	if !r.Allowed || r.Monitor == nil {
		t.Fatal("eligible start was rejected")
	}
	return *r.Monitor
}

func policyOutcome(t *testing.T, m Monitor, action Action, at time.Time, outcome string, ambiguous bool) Monitor {
	t.Helper()
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at, Outcome: outcome, Ambiguous: ambiguous})
	if r.Monitor == nil {
		t.Fatal("known action result was rejected")
	}
	return *r.Monitor
}

func TestPolicyRecoveryBoundAndCooldown(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.RecoveryMaxAttempts, p.RecoveryCooldown = true, 2, time.Minute
	m := configuredPolicyMonitor(t, p)
	firstAt := policyTime().Add(time.Second)
	m = policyPulse(t, m, firstAt, "failure")
	first := interventionAction(t, m)
	if first.Attempt != 1 || m.InterventionAttempts != 1 || first.NotBefore != firstAt {
		t.Fatal("first attempt has unexpected identity/budget", first, m.InterventionAttempts)
	}
	m = policyStart(t, m, first, firstAt)
	failedAt := firstAt.Add(time.Second)
	m = policyOutcome(t, m, first, failedAt, "failure", false)
	m = policyPulse(t, m, failedAt.Add(time.Second), "failure")
	second := interventionAction(t, m)
	if second.ID == first.ID || second.Attempt != 2 || m.InterventionAttempts != 2 || second.NotBefore != failedAt.Add(time.Minute) {
		t.Fatal("retry lacks a distinct identity, limit reservation or cooldown", second)
	}
	if r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore.Add(-time.Nanosecond)}); r.Allowed {
		t.Fatal("recovery started before the committed cooldown")
	}
	m = policyStart(t, m, second, second.NotBefore)
	m = policyOutcome(t, m, second, second.NotBefore.Add(time.Second), "failure", false)
	for n := 1; n <= 4; n++ {
		m = policyPulse(t, m, second.NotBefore.Add(time.Duration(n+1)*time.Minute), "failure")
	}
	last := interventionAction(t, m)
	if m.InterventionAttempts != 2 || last.ID != second.ID || last.State != Failed {
		t.Fatal("per-incident recovery budget was exceeded")
	}
	// Healthy recovery resets this incident's budget and cooldown, even though
	// terminal action outcomes remain available in the current state/history.
	for n := 0; n < p.Healthy; n++ {
		m = policyPulse(t, m, second.NotBefore.Add(time.Duration(n+8)*time.Minute), "success")
	}
	if m.InterventionAttempted || m.InterventionAttempts != 0 || !m.LastInterventionFailure.IsZero() {
		t.Fatal("completed incident retained its recovery budget")
	}
	nextAt := second.NotBefore.Add(12 * time.Minute)
	m = policyPulse(t, m, nextAt, "failure")
	next := interventionAction(t, m)
	if next.Attempt != 1 || next.ID == second.ID || next.NotBefore != nextAt {
		t.Fatal("new incident did not receive its own recovery budget")
	}
}

func TestPolicyDoesNotRetryUnknownOrSuccessfulRecovery(t *testing.T) {
	for _, outcome := range []string{"unknown", "success"} {
		t.Run(outcome, func(t *testing.T) {
			p := testMonitor().Policy
			p.Intervention, p.RecoveryMaxAttempts = true, 3
			m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
			a := interventionAction(t, m)
			m = policyStart(t, m, a, a.NotBefore)
			m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), outcome, outcome == "unknown")
			for n := 0; n < 5; n++ {
				m = policyPulse(t, m, a.NotBefore.Add(time.Duration(n+2)*time.Minute), "failure")
			}
			current := interventionAction(t, m)
			if current.ID != a.ID || m.InterventionAttempts != 1 || current.State == Queued || current.State == Started {
				t.Fatal("uncertain or successful recovery was automatically repeated")
			}
		})
	}
}

func TestPolicyLegacyRecoveryBudgetAndFailureEvidence(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention = true
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, a.NotBefore)
	m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), "failure", false)
	// Reproduce an old serialized record with only InterventionAttempted and
	// the retained failed action, without either new optional state field.
	m.InterventionAttempts, m.LastInterventionFailure = 0, time.Time{}
	raw, err := json.Marshal(image{Version: FormatVersion, Monitors: map[string]Monitor{m.ID: m}})
	if err != nil || strings.Contains(string(raw), "intervention_attempts") || strings.Contains(string(raw), "recovery_max_attempts") {
		t.Fatal("legacy snapshot fixture unexpectedly contains new optional fields", err)
	}
	restored, err := decodeImage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal("older snapshot policy was not accepted", err)
	}
	m = restored.Monitors[m.ID]
	before := m.Clone()
	m = policyPulse(t, m, a.NotBefore.Add(time.Minute), "failure")
	if current := interventionAction(t, m); current.ID != a.ID || current.State != Failed {
		t.Fatal("legacy zero policy became unlimited recovery")
	}
	before.Policy.RecoveryMaxAttempts, before.Policy.RecoveryCooldown = 2, 5*time.Minute
	m = policyPulse(t, before, a.NotBefore.Add(time.Minute), "failure")
	next := interventionAction(t, m)
	if next.Attempt != 2 || next.NotBefore != a.NotBefore.Add(time.Second+5*time.Minute) || m.LastInterventionFailure != a.NotBefore.Add(time.Second) {
		t.Fatal("legacy confirmed failure lost its prior attempt/cooldown", next)
	}
}

func TestPolicySameRevisionConfigurationAdoptsOptions(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention = true
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	a := interventionAction(t, m)
	config := m.Clone()
	config.Name, config.Policy.Enabled = "renamed", false
	config.Policy.Interval, config.Policy.RecoveryMaxAttempts, config.Policy.RecoveryCooldown = 2*time.Minute, 4, 3*time.Minute
	config.Policy.Maintenance = []MaintenanceWindow{{Start: policyTime(), End: policyTime().Add(time.Hour)}}
	config.Policy.Endpoints = map[string]int{"red": 3}
	at := policyTime().Add(2 * time.Second)
	r := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, At: at})
	if !r.Allowed || r.Monitor == nil {
		t.Fatal("same-revision option change rejected")
	}
	updated := *r.Monitor
	if updated.Name != config.Name || !reflect.DeepEqual(updated.Policy, config.Policy) || updated.NextCheck != at ||
		updated.Sequence != m.Sequence || updated.InterventionAttempts != m.InterventionAttempts || updated.Actions[a.ID].ID != a.ID || updated.Actions[a.ID].State != Cancelled || updated.Actions[a.ID].Outcome != "monitor_disabled" || len(r.Events) == 0 {
		t.Fatal("same-revision disable lost policy/identity or failed to cancel unsent work")
	}
	config.Policy.Endpoints["red"] = 99
	config.Policy.Maintenance[0].End = at
	if updated.Policy.Endpoints["red"] != 3 || updated.Policy.Maintenance[0].End != policyTime().Add(time.Hour) {
		t.Fatal("configuration policy retained mutable caller-owned slices/maps")
	}
	if r := transition(updated, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at, Outcome: "success"}); r.Monitor != nil || r.Allowed {
		t.Fatal("late pulse was applied after disable")
	}
	if r := transition(updated, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at}); r.Allowed {
		t.Fatal("queued recovery started after disable")
	}
	// Results of an already-started action remain commit-worthy after disable.
	m = policyStart(t, m, a, a.NotBefore)
	config = m.Clone()
	config.Policy.Enabled = false
	r = transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, At: at})
	updated = policyOutcome(t, *r.Monitor, a, at.Add(time.Second), "success", false)
	if updated.Actions[a.ID].State != Succeeded {
		t.Fatal("disable discarded an already-started known outcome")
	}
}

func TestPolicyQueuedStartRechecksChangedRecoveryOptions(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.RecoveryMaxAttempts = true, 2
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, a.NotBefore)
	m = policyOutcome(t, m, a, a.NotBefore.Add(time.Second), "failure", false)
	m = policyPulse(t, m, a.NotBefore.Add(2*time.Second), "failure")
	second := interventionAction(t, m)
	for _, scenario := range []string{"lower limit", "longer cooldown", "intervention disabled"} {
		t.Run(scenario, func(t *testing.T) {
			config := m.Clone()
			switch scenario {
			case "lower limit":
				config.Policy.RecoveryMaxAttempts = 1
			case "longer cooldown":
				config.Policy.RecoveryCooldown = time.Minute
			case "intervention disabled":
				config.Policy.Intervention = false
			}
			r := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, At: second.NotBefore})
			r = transition(*r.Monitor, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore})
			if r.Allowed {
				t.Fatal("queued start used old policy")
			}
		})
	}
}

func TestPolicyAbsoluteMaintenanceAtPulseAndStart(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention = true
	start, end := policyTime().Add(time.Minute), policyTime().Add(2*time.Minute)
	p.Maintenance = []MaintenanceWindow{{Start: start, End: end}}
	for _, tc := range []struct {
		at     time.Time
		inside bool
	}{
		{start.Add(-time.Nanosecond), false}, {start, true}, {end.Add(-time.Nanosecond), true}, {end, false},
	} {
		if p.InMaintenance(tc.at) != tc.inside {
			t.Fatal("absolute maintenance boundary is not [start, end)")
		}
	}
	m := configuredPolicyMonitor(t, p)
	m = policyPulse(t, m, start, "failure")
	if m.TotalChecks != 1 || m.InterventionAttempted || m.InterventionAttempts != 0 {
		t.Fatal("maintenance did not preserve observations while suppressing recovery admission")
	}
	m = policyPulse(t, m, end, "failure")
	a := interventionAction(t, m)
	if a.State != Queued {
		t.Fatal("recovery did not resume after maintenance")
	}
	// Actions admitted before a maintenance boundary must be checked again by
	// the FSM at start, for both notifications and recovery.
	p.Endpoints = map[string]int{"yellow": 1}
	m = policyPulse(t, configuredPolicyMonitor(t, p), start.Add(-time.Second), "failure")
	if len(m.Actions) != 2 {
		t.Fatal("fixture requires queued notification and recovery")
	}
	for _, action := range m.Actions {
		if r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: start}); r.Allowed {
			t.Fatal("queued action bypassed durable maintenance", action.Kind)
		}
		_ = policyStart(t, m, action, end)
	}
}

func TestPolicyValidationAndSnapshotIsolation(t *testing.T) {
	p := testMonitor().Policy
	p.Maintenance = []MaintenanceWindow{{Start: policyTime(), End: policyTime().Add(time.Hour)}}
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.RecoveryMaxAttempts = -1 },
		func(p *Policy) { p.RecoveryCooldown = -time.Nanosecond },
		func(p *Policy) { p.Maintenance[0].Start = time.Time{} },
		func(p *Policy) { p.Maintenance[0].End = p.Maintenance[0].Start },
		func(p *Policy) { p.Endpoints["red"] = -1 },
	} {
		bad := p.Clone()
		mutate(&bad)
		m := testMonitor()
		m.Policy = bad
		command := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
		if err := validateCommand(command); err == nil {
			t.Fatal("malformed policy accepted in log command")
		}
		raw, _ := json.Marshal(image{Version: FormatVersion, Monitors: map[string]Monitor{m.ID: m}})
		if _, err := decodeImage(strings.NewReader(string(raw))); err == nil {
			t.Fatal("malformed policy accepted in snapshot")
		}
	}
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy = p.Clone()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	read, _ := s.Get(m.ID)
	read.Policy.Maintenance[0].End = policyTime().Add(3 * time.Hour)
	read.Policy.Endpoints["red"] = 50
	if got := frozen.(*frozenSnapshot).image.Monitors[m.ID].Policy; got.Maintenance[0].End != p.Maintenance[0].End || got.Endpoints["red"] != p.Endpoints["red"] {
		t.Fatal("snapshot policy changed through an exported copy")
	}
}

func TestPolicyRecoveryBudgetAndCooldownSurviveRestart(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m := testMonitor()
	m.Policy.Intervention, m.Policy.RecoveryMaxAttempts, m.Policy.RecoveryCooldown = true, 2, time.Hour
	m.Policy.Maintenance = []MaintenanceWindow{{Start: policyTime().Add(10 * time.Minute), End: policyTime().Add(20 * time.Minute)}}
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: policyTime().Add(time.Second), Outcome: "failure"})
	m, _ = s.Get(m.ID)
	a := interventionAction(t, m)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: a.NotBefore})
	failedAt := a.NotBefore.Add(time.Second)
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: failedAt, Outcome: "failure"})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, At: failedAt.Add(time.Second), Outcome: "failure"})
	m, _ = s.Get(m.ID)
	second := interventionAction(t, m)
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(m.ID)
	if !reflect.DeepEqual(current.Policy, m.Policy) || current.InterventionAttempts != 2 || current.LastInterventionFailure != failedAt || current.Actions[second.ID] != second {
		t.Fatal("restart lost policy or recovery admission state")
	}
	if result := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore.Add(-time.Nanosecond)})[0]; result.Allowed {
		t.Fatal("restart bypassed recovery cooldown")
	}
	if result := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore})[0]; !result.Allowed {
		t.Fatal("safe queued attempt did not survive restart", result.Err)
	}
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: second.NotBefore.Add(time.Second), Outcome: "failure"})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 3, At: second.NotBefore.Add(2 * time.Hour), Outcome: "failure"})
	current, _ = s.Get(m.ID)
	if last := interventionAction(t, current); last.ID != second.ID || last.State != Failed || current.InterventionAttempts != 2 {
		t.Fatal("restart reset the per-incident attempt limit")
	}
}

func TestHasCatalogRetainsIdentityAndReportsUnavailable(t *testing.T) {
	for _, scenario := range []string{"empty", "tombstone", "pending bootstrap", "storage failed", "FSM failed", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			s := openCatalogMemory(t)
			want, unavailable := false, false
			switch scenario {
			case "tombstone":
				r := createCatalog(t, s, catalogRecord(t, s, "Recipient", "retained", "uid", "rv", "private"))
				if result := catalogSubmit(t, s, deleteCatalogMutation(r, "deleted")); result.Err != nil {
					t.Fatal(result.Err)
				}
				want = true
			case "pending bootstrap":
				manifest, _ := bootstrapFixture(t, s)
				bootstrapSubmit(t, s, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest})
				want = true
			case "storage failed":
				s.mu.Lock()
				s.err = errors.New("injected write failure")
				s.mu.Unlock()
				unavailable = true
			case "FSM failed":
				s.fsm.mu.Lock()
				s.fsm.err = errors.New("injected replay failure")
				s.fsm.mu.Unlock()
				unavailable = true
			case "closed":
				_ = s.Close()
				unavailable = true
			}
			got, err := s.HasCatalog()
			if got != want || (err != nil) != unavailable {
				t.Fatalf("HasCatalog = %t, %v; want identity=%t unavailable=%t", got, err, want, unavailable)
			}
		})
	}
}
