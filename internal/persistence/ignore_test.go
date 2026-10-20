package persistence

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestAutomaticIgnoreCountsDistinctThresholdOutages(t *testing.T) {
	p := lifecyclePolicy()
	p.Unhealthy, p.Healthy = 2, 2
	p.AutoIgnore = &AutoIgnorePolicy{Incidents: 2, Window: time.Hour}
	p.Endpoints["yellow"] = 1
	m := configuredPolicyMonitor(t, p)
	at := policyTime()
	m = policyPulse(t, m, at.Add(time.Second), "failure")
	if len(m.IgnoreIncidents) != 0 {
		t.Fatal("counted a first failure before the unhealthy threshold")
	}
	m = policyPulse(t, m, at.Add(2*time.Second), "failure")
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	if m.Ignored || len(m.IgnoreIncidents) != 1 {
		t.Fatal("repeated failures counted as separate outages")
	}
	m = policyPulse(t, m, at.Add(4*time.Second), "success")
	m = policyPulse(t, m, at.Add(5*time.Second), "success")
	if len(m.IgnoreIncidents) != 1 {
		t.Fatal("ordinary recovery reset the rolling counter")
	}
	m = policyPulse(t, m, at.Add(6*time.Second), "failure")
	for _, a := range m.Actions {
		if a.State == Queued && a.Color == "yellow" {
			t.Fatal("potential Ignore-triggering outage sent yellow before its threshold")
		}
	}
	m = policyPulse(t, m, at.Add(7*time.Second), "failure")
	if !m.Ignored || !m.Incident || m.LastOutcome != "failure" || m.IgnoreCause != "incident_frequency" {
		t.Fatal("threshold outage did not enter Ignore while retaining actual health")
	}
	for _, a := range m.Actions {
		if a.State == Queued {
			t.Fatal("Ignore retained an unsent ordinary action")
		}
	}
	m = policyPulse(t, m, at.Add(8*time.Second), "success")
	if !m.Ignored {
		t.Fatal("Ignore ended before verified recovery")
	}
	m = policyPulse(t, m, at.Add(9*time.Second), "success")
	if m.Ignored || len(m.IgnoreIncidents) != 0 || m.Incident {
		t.Fatal("verified recovery did not clear Ignore and its counter")
	}
	green := notificationByColor(t, m, "green", Queued)
	m = policyPulse(t, m, at.Add(10*time.Second), "success")
	if notificationByColor(t, m, "green", Queued).ID != green.ID {
		t.Fatal("Ignore recovery admitted duplicate green deliveries")
	}
}

func TestAutomaticIgnoreWindowAndPolicyValidation(t *testing.T) {
	for _, p := range []*AutoIgnorePolicy{{}, {Incidents: 1, Window: time.Hour}, {Incidents: 1001, Window: time.Hour}, {Incidents: 2}, {Incidents: 2, Window: 31 * 24 * time.Hour}} {
		if p.validate() == nil {
			t.Fatal("invalid policy accepted", p)
		}
	}
	p := lifecyclePolicy()
	p.AutoIgnore = &AutoIgnorePolicy{Incidents: 2, Window: time.Minute}
	m := configuredPolicyMonitor(t, p)
	at := policyTime()
	m = policyPulse(t, m, at.Add(time.Second), "failure")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	m = policyPulse(t, m, at.Add(time.Minute+time.Second), "failure")
	if m.Ignored || len(m.IgnoreIncidents) != 1 {
		t.Fatal("rolling window retained an expired outage at its lower boundary")
	}
}

func TestManualIgnoreRetainsStartedEvidenceAndRestartState(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "manual-ignore")
	at := policyTime().Add(time.Second)
	m = controlPulse(t, s, m, guard, at, "failure")
	a := interventionAction(t, m)
	m = *submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: guard, At: at})[0].Monitor
	request := controlRequest(m, "ignore", "ignore-revision", at.Add(time.Second))
	m = *applyControlRequest(t, s, request).Monitor
	if !m.Ignored || m.IgnoredBy != request.Control.Actor || m.Actions[a.ID].State != Started {
		t.Fatal("Ignore erased started evidence or actor")
	}
	if submit(t, s, request)[0].Err == nil {
		t.Fatal("stale Ignore precondition succeeded")
	}
	image := captureSnapshotBytes(t, s.fsm)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(image))); err != nil {
		t.Fatal(err)
	}
	m, _ = monitorWithActionHistory(t, s, m.ID)
	if !m.Ignored || m.IgnoreReason != request.Control.Reason {
		t.Fatal("snapshot lost Ignore")
	}
	m = *submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), Outcome: "failure", Ambiguous: true})[0].Monitor
	if !m.Actions[a.ID].Held() {
		t.Fatal("Ignore discarded an uncertain result")
	}
	for _, action := range m.Actions {
		if action.State == Queued {
			t.Fatal("started result admitted a suppressed follow-up")
		}
	}
	m = *applyControlRequest(t, s, controlRequest(m, "unignore", "unignore-revision", at.Add(3*time.Second))).Monitor
	if m.Ignored || !retainedAction(t, s, a.ID).Held() {
		t.Fatal("Unignore changed provider evidence")
	}
}

func TestGrayTransitionDuringIgnoreAndMaintenance(t *testing.T) {
	p := lifecyclePolicy()
	p.Endpoints["gray"] = 1
	p.Maintenance = []MaintenanceWindow{{Start: policyTime(), End: policyTime().Add(time.Hour)}}
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m.enterIgnore(at, "operator", "operator", "investigating", func(string, *Action) {})
	for _, enabled := range []bool{false, true} {
		config := m.Clone()
		config.Policy.Enabled = enabled
		config.Revision += "-next"
		r := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at})
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(r.Err)
		}
		m = *r.Monitor
		gray := notificationByColor(t, m, "gray", Queued)
		if !m.ControlNotification(gray) || gray.Control.Enabled != enabled || !m.Ignored {
			t.Fatal("gray lost the control transition or configuration cleared Ignore")
		}
		// Startup reconciles the same manifest after recovering committed work.
		sequence := m.Sequence
		r = transition(m, Command{Kind: "recover", At: at.Add(time.Millisecond)})
		r = transition(*r.Monitor, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(2 * time.Millisecond)})
		m = *r.Monitor
		if m.Actions[gray.ID].State != Queued || m.Sequence != sequence {
			t.Fatal("unchanged restart cancelled or duplicated the original control notice")
		}
		r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: gray.ID, At: at, Maintenance: true})
		if !r.Allowed {
			t.Fatal("gray blocked by Ignore, disabled state or maintenance")
		}
		m = policyOutcome(t, *r.Monitor, gray, at.Add(time.Millisecond), "success", false)
		before := m.Sequence
		r = transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(time.Second)})
		m = *r.Monitor
		if m.Sequence != before {
			t.Fatal("unchanged restart emitted gray")
		}
		at = at.Add(2 * time.Second)
	}
}

func TestIgnoreRecoveryPolicyChangeRestores(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "ignore-policy")
	config := m.Clone()
	config.Policy.Healthy = 5
	config.Policy.Unhealthy = 1
	m = *submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: guard, At: policyTime()})[0].Monitor
	m = controlPulse(t, s, m, guard, policyTime().Add(time.Millisecond), "failure")
	m = *applyControlRequest(t, s, controlRequest(m, "ignore", "ignore-version", policyTime().Add(time.Second))).Monitor
	for n := 2; n < 5; n++ {
		m = controlPulse(t, s, m, guard, policyTime().Add(time.Duration(n)*time.Second), "success")
	}
	if m.IgnoreHealthyChecks != 3 {
		t.Fatal("fixture did not retain recovery progress")
	}
	config = Monitor{ID: m.ID, Name: m.Name, Revision: m.Revision, CatalogUID: m.CatalogUID, Policy: m.Policy.Clone()}
	config.Policy.Healthy = 1
	m = *submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: guard, At: policyTime().Add(5 * time.Second)})[0].Monitor
	if !m.Ignored || m.IgnoreHealthyChecks != 0 {
		t.Fatal("policy change cleared Ignore or retained incompatible recovery progress")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal("policy change produced an unreadable snapshot", err)
	}
}

func TestAutomaticIgnoreClockRollbackRestores(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "ignore-clock")
	config := Monitor{ID: m.ID, Name: m.Name, Revision: m.Revision, CatalogUID: m.CatalogUID, Policy: m.Policy.Clone()}
	config.Policy.Healthy, config.Policy.Unhealthy = 1, 1
	config.Policy.AutoIgnore = &AutoIgnorePolicy{Incidents: 2, Window: time.Hour}
	at := policyTime()
	m = *submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: guard, At: at})[0].Monitor
	m = controlPulse(t, s, m, guard, at.Add(10*time.Minute), "failure")
	m = controlPulse(t, s, m, guard, at.Add(11*time.Minute), "success")
	m = controlPulse(t, s, m, guard, at.Add(5*time.Minute), "failure")
	if m.Ignored || len(m.IgnoreIncidents) != 1 || !m.IgnoreIncidents[0].Equal(at.Add(5*time.Minute)) {
		t.Fatal("clock rollback counted a future outage")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal("clock rollback produced an unreadable snapshot", err)
	}
	m, _ = s.Get(m.ID)
	m = controlPulse(t, s, m, guard, at.Add(6*time.Minute), "success")
	m = controlPulse(t, s, m, guard, at.Add(7*time.Minute), "failure")
	if !m.Ignored {
		t.Fatal("restored counter did not count a subsequent in-window outage")
	}
}
