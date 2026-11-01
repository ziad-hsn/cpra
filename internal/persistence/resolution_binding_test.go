package persistence

import (
	"encoding/json"
	"testing"
	"time"
)

func boundResolutionMonitor(t *testing.T) (Monitor, Action) {
	t.Helper()
	m := testMonitor()
	m.Policy = lifecyclePolicy()
	m.Policy.Intervention = false
	m.Policy.NotificationEndpointIDs = map[string][]string{"red": {"original"}, "green": {"original"}}
	binding := NotificationBinding("check-target", "original", "credential-incarnation")
	m.Policy.NotificationBindings = map[string][]string{"red": {binding}, "green": {binding}}
	r := transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}, DispatchFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	m = *r.Monitor
	m = dispatchTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Second), Outcome: "failure"})
	a := notificationByColor(t, m, "red", Queued)
	m = dispatchTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: policyTime().Add(2 * time.Second)})
	m = dispatchTransition(t, m, Command{Kind: "result", ActionID: a.ID, At: policyTime().Add(3 * time.Second), Outcome: "failure", Ambiguous: true})
	return m, m.Actions[a.ID]
}
func editResolutionMonitor(t *testing.T, m Monitor, change func(*Policy)) Monitor {
	t.Helper()
	config := Monitor{ID: m.ID, Name: m.Name, Revision: m.Revision + "-edit", CatalogUID: m.CatalogUID, Policy: m.Policy.Clone()}
	change(&config.Policy)
	r := transitionAtFormat(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: policyTime().Add(4 * time.Second)}, DispatchFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	return *r.Monitor
}
func recoverBoundResolution(t *testing.T, m Monitor) Monitor {
	t.Helper()
	for _, s := range []int{5, 6} {
		m = dispatchTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Duration(s) * time.Second), Outcome: "success"})
	}
	return m
}
func TestResolutionBindingPreservesOriginalOwnershipAcrossEdits(t *testing.T) {
	for _, phase := range []string{"before-recovery", "queued", "started"} {
		t.Run(phase, func(t *testing.T) {
			m, opening := boundResolutionMonitor(t)
			var green Action
			if phase != "before-recovery" {
				m = recoverBoundResolution(t, m)
				green = notificationByColor(t, m, "green", Queued)
			}
			if phase == "started" {
				m = dispatchTransition(t, m, Command{Kind: "start", ActionID: green.ID, At: policyTime().Add(7 * time.Second)})
				green = m.Actions[green.ID]
			}
			m = editResolutionMonitor(t, m, func(p *Policy) { p.Interval += time.Second })
			if phase == "before-recovery" {
				m = recoverBoundResolution(t, m)
				green = notificationByColor(t, m, "green", Queued)
			}
			if m.Actions[opening.ID].State != Unknown || green.NotificationCorrelation != opening.NotificationCorrelation || green.Revision != opening.Revision {
				t.Fatal("original evidence changed")
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if err = validateActionFormat(m, DispatchFormatVersion); err != nil {
				t.Fatal(err)
			}
			if err = validateActionFormat(m, RecoveryRulesFormatVersion); err == nil {
				t.Fatal("older format accepted new bindings")
			}
			if phase != "started" {
				r := transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: green.Revision, ActionID: green.ID, NoticeRevision: ActionNoticeRevision(m.Actions[green.ID]), At: policyTime().Add(8 * time.Second)}, DispatchFormatVersion)
				if !r.Allowed || r.Monitor == nil {
					t.Fatal("retained resolution blocked", r.Err)
				}
				m = *r.Monitor
			}
			r := transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: green.Revision, ActionID: green.ID, At: policyTime().Add(9 * time.Second), Outcome: "success"}, DispatchFormatVersion)
			if r.Err != nil || r.Monitor == nil || r.Monitor.Actions[green.ID].State != Succeeded {
				t.Fatalf("original completion lost: %+v monitor=%+v", r, r.Monitor)
			}
		})
	}
}
func TestResolutionBindingRejectsChangedDependencies(t *testing.T) {
	for _, field := range []string{"check", "credential", "destination"} {
		t.Run(field, func(t *testing.T) {
			m, _ := boundResolutionMonitor(t)
			m = editResolutionMonitor(t, m, func(p *Policy) {
				switch field {
				case "destination":
					p.NotificationEndpointIDs["green"][0] = "replacement"
				case "check":
					p.NotificationBindings["green"][0] = NotificationBinding("new-check", "original", "credential-incarnation")
				case "credential":
					p.NotificationBindings["green"][0] = NotificationBinding("check-target", "original", "new-credential")
				}
			})
			m = recoverBoundResolution(t, m)
			if queuedColorCount(m, "green") != 0 {
				t.Fatal("changed dependency received old closure")
			}
		})
	}
}
