package persistence

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func lifecyclePolicy() Policy {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy, p.Healthy = false, 1, 2
	p.Endpoints = map[string]int{"red": 1, "green": 1}
	return p
}

func TestUnknownDeliveryDoesNotHoldNextOutage(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	first := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, first, at)
	m = policyOutcome(t, m, first, at.Add(time.Millisecond), "failure", true)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	_ = notificationByColor(t, m, "green", Queued)
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	next := notificationByColor(t, m, "red", Queued)
	if next.DeliveryID == first.DeliveryID || next.NotificationEpisode == first.NotificationEpisode || !m.Actions[first.ID].Held() {
		t.Fatal("new outage reused or erased the uncertain delivery")
	}
	m = policyStart(t, m, next, at.Add(3*time.Second))
	if m.Actions[first.ID].State != Unknown || m.Actions[next.ID].State != Started {
		t.Fatal("independent delivery did not start while preserving original evidence")
	}
}

func TestUnknownInterventionDoesNotHoldObservedHealth(t *testing.T) {
	p := lifecyclePolicy()
	p.Intervention = true
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, at)
	m = policyOutcome(t, m, a, at.Add(time.Millisecond), "failure", true)
	firstIncident := m.IncidentID
	red := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, red, at.Add(2*time.Millisecond))
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	if m.Incident || m.Recovering || m.IncidentClosedAt.IsZero() || !m.Actions[a.ID].Held() {
		t.Fatal("uncertain intervention prevented observed recovery or lost its hold")
	}
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	if !m.Incident || m.IncidentID == firstIncident || !m.Actions[a.ID].Held() {
		t.Fatal("new outage was lost behind the old intervention")
	}
	_ = notificationByColor(t, m, "red", Queued)
	for _, action := range m.Actions {
		if action.Kind == "intervention" && (action.State == Queued || action.State == Started) {
			t.Fatal("new outage admitted a conflicting intervention")
		}
	}
}

func TestGreenRequiresActualAlertStart(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "started"}[started], func(t *testing.T) {
			p := lifecyclePolicy()
			p.Unhealthy = 3
			p.Endpoints["yellow"] = 1
			m := configuredPolicyMonitor(t, p)
			at := policyTime().Add(time.Second)
			m = policyPulse(t, m, at, "failure")
			if started {
				m = policyStart(t, m, notificationByColor(t, m, "yellow", Queued), at)
			}
			m = policyPulse(t, m, at.Add(time.Second), "success")
			for _, a := range m.Actions {
				if a.Color == "green" {
					t.Fatal("green preceded the healthy threshold")
				}
			}
			m = policyPulse(t, m, at.Add(2*time.Second), "success")
			green := false
			for _, a := range m.Actions {
				green = green || a.Color == "green" && a.State == Queued
			}
			if green != started {
				t.Fatal("green did not match actual alert exposure")
			}
		})
	}
}

func TestCompletedInterventionCannotChangeLaterIncident(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			p := lifecyclePolicy()
			p.Intervention = true
			p.Endpoints["cyan"] = 1
			m := configuredPolicyMonitor(t, p)
			at := policyTime().Add(time.Second)
			m = policyPulse(t, m, at, "failure")
			a := interventionAction(t, m)
			m = policyStart(t, m, a, at)
			m = policyPulse(t, m, at.Add(time.Second), "success")
			m = policyPulse(t, m, at.Add(2*time.Second), "success")
			m = policyPulse(t, m, at.Add(3*time.Second), "failure")
			incident := m.IncidentID
			m = policyOutcome(t, m, a, at.Add(4*time.Second), outcome, false)
			if m.IncidentID != incident || m.VerifyRemaining != 0 || !m.Incident {
				t.Fatal("old action changed new incident verification")
			}
			if m.Actions[a.ID].State == Started {
				t.Fatal("actual result was discarded")
			}
			for _, action := range m.Actions {
				if action.Color == "cyan" && action.State == Queued {
					t.Fatal("old action emitted a new incident completion")
				}
			}
		})
	}
}

func TestRestartInterventionRequiresFreshObservation(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			s := openCatalogMemory(t)
			config := testMonitor()
			config.Policy = lifecyclePolicy()
			config.Policy.Intervention = true
			at := policyTime().Add(time.Second)
			submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: policyTime()})
			m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, At: at, Generation: 1, Outcome: "failure"})[0].Monitor
			a := interventionAction(t, m)
			restart := at.Add(time.Second)
			submit(t, s, Command{Kind: "recover", At: restart})
			start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: restart.Add(time.Millisecond)}
			if submit(t, s, start)[0].Allowed {
				t.Fatal("restored recovery started without a new observation")
			}
			raw := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(raw, []byte(actionSnapshotMagic)) {
				t.Fatal("restart barrier lost its format")
			}
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
				t.Fatal(err)
			}
			// A delayed pre-restart execution is not a fresh observation.
			submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: restart.Add(time.Second), Generation: 2, Outcome: "failure", ExecutionStart: at})
			start.At = restart.Add(2 * time.Second)
			if submit(t, s, start)[0].Allowed {
				t.Fatal("old execution released the restart barrier")
			}
			m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: restart.Add(3 * time.Second), Generation: 3, Outcome: outcome, ExecutionStart: restart.Add(time.Second)})[0].Monitor
			start.At = restart.Add(4 * time.Second)
			r := submit(t, s, start)[0]
			if r.Allowed != (outcome == "failure") {
				t.Fatal("fresh health did not govern original intent")
			}
			if outcome == "success" && m.Actions[a.ID].State != Cancelled {
				t.Fatal("fresh success left queued recovery")
			}
		})
	}
}

func TestLifecycleFieldsRequireNewFormat(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	m.ActionVersion = 0 // Exercise the supported format-42 contract independently.
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
	if commandMinimumFormat(c) != LifecycleFormatVersion {
		t.Fatal("lifecycle state escaped version boundary")
	}
	raw, err := json.Marshal(envelope{Version: DeliveryFormatVersion, Commands: []Command{c}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeEnvelope(raw); err == nil {
		t.Fatal("format 40 accepted new lifecycle state")
	}
}

func TestHistoricalUnknownInterventionReplay(t *testing.T) {
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention = true
	apply := func(m Monitor, c Command) Monitor {
		c.MonitorID, c.Revision = config.ID, config.Revision
		r := transitionAtFormat(m, c, DeliveryFormatVersion)
		if r.Err != nil || r.Monitor == nil {
			t.Fatal("historical replay failed", r.Err)
		}
		return *r.Monitor
	}
	at := policyTime()
	m := apply(Monitor{}, Command{Kind: "configure", Config: &config, At: at})
	m = apply(m, Command{Kind: "pulse", At: at.Add(time.Second), Generation: 1, Outcome: "failure"})
	a := interventionAction(t, m)
	m = apply(m, Command{Kind: "start", ActionID: a.ID, At: at.Add(2 * time.Second)})
	m = apply(m, Command{Kind: "result", ActionID: a.ID, At: at.Add(3 * time.Second), Outcome: "failure", Ambiguous: true})
	for n := uint64(2); n < 5; n++ {
		m = apply(m, Command{Kind: "pulse", At: at.Add(time.Duration(n+3) * time.Second), Generation: n, Outcome: "success"})
	}
	if !m.Incident || !m.Recovering || m.LifecycleVersion != 0 {
		t.Fatal("historical format replay was changed")
	}
}

func TestWarningRecoveryRequiresHealthyThreshold(t *testing.T) {
	p := lifecyclePolicy()
	p.Endpoints["yellow"] = 1
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	r := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Generation: 1, Outcome: "success", Warning: true})
	m = *r.Monitor
	m = policyStart(t, m, notificationByColor(t, m, "yellow", Queued), at)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	for _, a := range m.Actions {
		if a.Color == "green" {
			t.Fatal("clear warning emitted green before threshold")
		}
	}
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	_ = notificationByColor(t, m, "green", Queued)
}

func TestFileMonitorRecreationSeparatesHistoricalActions(t *testing.T) {
	p := lifecyclePolicy()
	p.Intervention = true
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, at)
	incarnation := m.FileIncarnation
	r := transition(m, Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second)})
	m = *r.Monitor
	config := testMonitor()
	config.Policy = p
	r = transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, At: at.Add(2 * time.Second)})
	m = *r.Monitor
	if m.FileIncarnation == "" || m.FileIncarnation == incarnation || m.CatalogUID != "" || m.Incident || m.Recovering {
		t.Fatal("file re-add reused old runtime identity")
	}
	old := m.Actions[a.ID]
	if old.State != Unknown || !old.Held() || m.OwnsAction(old) {
		t.Fatal("old effect lost its original incarnation")
	}
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	var next Action
	for _, candidate := range m.Actions {
		if candidate.Kind == "intervention" && candidate.State == Queued {
			next = candidate
		}
	}
	if next.ID == "" || next.ID == old.ID || !m.OwnsAction(next) {
		t.Fatal("new target did not receive independent intent")
	}
	r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: old.ID, At: at.Add(4 * time.Second)})
	if r.Allowed {
		t.Fatal("old intervention was replayed on new target")
	}
	if err := validateLifecycle(m, LifecycleFormatVersion); err != nil {
		t.Fatal(err)
	}
	old.Review = &ActionReview{Revision: "review-old", Resolution: "rejected", Actor: "administrator", Reason: "Inspected the original target", At: at.Add(time.Second)}
	m.Actions[old.ID] = old
	r = applyLateResult(m, Command{Kind: "late_result", MonitorID: m.ID, Revision: old.Revision, ActionID: old.ID, Outcome: "success", ExecutionStart: old.StartedAt, ExecutionEnd: old.StartedAt.Add(time.Second), At: at.Add(5 * time.Second)})
	if r.Err != nil || !r.Allowed || !r.Monitor.Actions[old.ID].Held() || r.Monitor.Actions[next.ID].State != Queued {
		t.Fatal("late evidence against old target affected new incarnation", r.Err)
	}
	for _, event := range r.Events {
		if event.FileIncarnation != old.FileIncarnation {
			t.Fatal("event lost original incarnation")
		}
	}
}

func TestUnknownDeliveryFollowsDestinationAcrossGroupOrder(t *testing.T) {
	p := lifecyclePolicy()
	p.Endpoints["red"] = 2
	p.NotificationEndpointIDs = map[string][]string{"red": {"target-A", "target-B"}}
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	var a Action
	for _, candidate := range m.Actions {
		if candidate.Kind == "code" && candidate.Color == "red" && candidate.Endpoint == 0 {
			a = candidate
		}
	}
	m = policyStart(t, m, a, at)
	m = policyOutcome(t, m, a, at.Add(time.Millisecond), "failure", true)
	config := testMonitor()
	config.Revision = "reordered"
	config.Policy = p.Clone()
	config.Policy.NotificationEndpointIDs["red"] = []string{"target-B", "target-A"}
	r := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(time.Second)})
	m = *r.Monitor
	// Attempting the same logical destination at its new ordinal must stay held.
	m.queueAction("code", "red", 1, at.Add(2*time.Second), func(string, *Action) {})
	for _, candidate := range m.Actions {
		if candidate.EndpointID == a.EndpointID && candidate.State == Queued {
			t.Fatal("reordering bypassed unknown delivery hold")
		}
	}
	// A newly configured destination is independent even at that same ordinal.
	m.Policy.NotificationEndpointIDs["red"][1] = "target-A-replaced"
	m.queueAction("code", "red", 1, at.Add(3*time.Second), func(string, *Action) {})
	queued := 0
	for _, candidate := range m.Actions {
		if candidate.State == Queued && candidate.Endpoint == 1 {
			queued++
		}
	}
	if queued != 1 || !m.Actions[a.ID].Held() {
		t.Fatal("replacement destination lost independence or old evidence")
	}
}
