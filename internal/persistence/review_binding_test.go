package persistence

import (
	"encoding/json"
	"testing"
	"time"
)

func boundReviewMonitor(t *testing.T) (Monitor, Action, Action) {
	t.Helper()
	m := testMonitor()
	m.Policy = lifecyclePolicy()
	m.Policy.Intervention = true
	m.Policy.Endpoints = map[string]int{"blue": 1}
	m.Policy.NotificationEndpointIDs = map[string][]string{"blue": {"original"}}
	m.Policy.NotificationBindings = map[string][]string{"blue": {NotificationBinding("check", "original", "credential")}}
	r := transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}, DispatchFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	m = dispatchTransition(t, *r.Monitor, Command{Kind: "pulse", At: policyTime().Add(time.Second), Outcome: "failure"})
	recovery := interventionAction(t, m)
	m = dispatchTransition(t, m, Command{Kind: "start", ActionID: recovery.ID, At: policyTime().Add(2 * time.Second)})
	m = dispatchTransition(t, m, Command{Kind: "result", ActionID: recovery.ID, At: policyTime().Add(3 * time.Second), Outcome: "failure", Ambiguous: true})
	opening := notificationByColor(t, m, "blue", Queued)
	m = dispatchTransition(t, m, Command{Kind: "start", ActionID: opening.ID, At: policyTime().Add(4 * time.Second)})
	m = dispatchTransition(t, m, Command{Kind: "result", ActionID: opening.ID, At: policyTime().Add(5 * time.Second), Outcome: "success"})
	return m, recovery, opening
}

func TestReviewClosureBindingSurvivesHarmlessEdit(t *testing.T) {
	for _, phase := range []string{"before-recovery", "queued", "started"} {
		t.Run(phase, func(t *testing.T) {
			m, recovery, opening := boundReviewMonitor(t)
			var closing Action
			if phase != "before-recovery" {
				m = recoverBoundResolution(t, m)
				closing = notificationByColor(t, m, "blue", Queued)
			}
			if phase == "started" {
				m = dispatchTransition(t, m, Command{Kind: "start", ActionID: closing.ID, At: policyTime().Add(7 * time.Second)})
			}
			m = editResolutionMonitor(t, m, func(p *Policy) { p.Interval += time.Second })
			if phase == "before-recovery" {
				m = recoverBoundResolution(t, m)
				closing = notificationByColor(t, m, "blue", Queued)
			}
			closing = m.Actions[closing.ID]
			if closing.ReviewBinding == "" || closing.NotificationCorrelation != opening.NotificationCorrelation || closing.Revision != opening.Revision || !m.Actions[recovery.ID].Held() || !m.ActionConfigurationCurrent(closing) {
				t.Fatal("closure lost identity/authority or released effect hold")
			}
			// The operational projection keeps only the bounded active action.
			projected := m.Clone()
			projected.Notifications.ReviewAlerts = nil
			if !projected.ActionConfigurationCurrent(closing) {
				t.Fatal("projected closure lost its committed dependency binding")
			}
			alert := m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID]
			if got := reviewConditionObservation(m, alert, closing, true, "close"); got.State == "blocked" {
				t.Fatal("observation contradicted retained authority", got)
			}
			if phase != "started" {
				r := transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: closing.Revision, ActionID: closing.ID, NoticeRevision: ActionNoticeRevision(closing), At: policyTime().Add(8 * time.Second)}, DispatchFormatVersion)
				if r.Err != nil || !r.Allowed || r.Monitor == nil {
					t.Fatal("closure start", r.Err)
				}
				m = *r.Monitor
			}
			r := transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: closing.Revision, ActionID: closing.ID, At: policyTime().Add(9 * time.Second), Outcome: "success"}, DispatchFormatVersion)
			if r.Err != nil || r.Monitor == nil || r.Monitor.Actions[closing.ID].State != Succeeded {
				t.Fatal("closure result", r.Err)
			}
			m = *r.Monitor
			if err := m.validateReviewAlert(m.Notifications.ReviewAlerts[alert.ID]); err != nil {
				t.Fatal(err)
			}
			bad := m.Clone()
			a := bad.Actions[closing.ID]
			a.ReviewBinding = NotificationBinding("other", "original", "credential")
			bad.Actions[a.ID] = a
			if bad.validateReviewAlert(bad.Notifications.ReviewAlerts[alert.ID]) == nil {
				t.Fatal("mismatched retained binding accepted")
			}
			if validateReviewNotice(m.Actions[closing.ID], RecoveryRulesFormatVersion) == nil {
				t.Fatal("older format accepted new closure binding")
			}
		})
	}
}

func TestReviewClosureBindingRejectsChangedDependencies(t *testing.T) {
	for _, field := range []string{"target", "credential", "destination", "slot"} {
		t.Run(field, func(t *testing.T) {
			m, _, opening := boundReviewMonitor(t)
			m = editResolutionMonitor(t, m, func(p *Policy) {
				switch field {
				case "target":
					p.NotificationBindings["blue"][0] = NotificationBinding("changed", "original", "credential")
				case "credential":
					p.NotificationBindings["blue"][0] = NotificationBinding("check", "original", "changed")
				case "destination":
					p.NotificationEndpointIDs["blue"][0] = "changed"
				case "slot":
					p.Endpoints["blue"] = 0
					delete(p.NotificationEndpointIDs, "blue")
					delete(p.NotificationBindings, "blue")
				}
			})
			m = recoverBoundResolution(t, m)
			if queuedColorCount(m, "blue") != 0 || m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID].ClosureBlocked != "configuration_changed" {
				t.Fatal("changed dependency received closure")
			}
		})
	}
}

func TestHistoricalReviewDestinationCanonicalBytes(t *testing.T) {
	d := reviewDestination{ID: "destination", Timeout: time.Second}
	b, err := json.Marshal(d)
	if err != nil || string(b) != `{"id":"destination","timeout":1000000000}` {
		t.Fatal("historical digest projection changed", string(b), err)
	}
	d.Binding = NotificationBinding("check", "destination", "credential")
	b, _ = json.Marshal(d)
	var decoded reviewDestination
	if json.Unmarshal(b, &decoded) != nil || decoded != d {
		t.Fatal("bound policy failed round trip")
	}
}
