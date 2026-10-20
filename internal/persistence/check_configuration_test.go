package persistence

import (
	"reflect"
	"testing"
	"time"
)

func TestCheckPreparationPreservesHealthAndBlocksRecovery(t *testing.T) {
	for _, initial := range []string{"", "success", "failure"} {
		t.Run(initial, func(t *testing.T) {
			config := testMonitor()
			config.Policy.Unhealthy, config.Policy.Healthy = 1, 2
			at := policyTime()
			m := *transition(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at}).Monitor
			if initial != "" {
				m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Generation: 1, Outcome: initial}).Monitor
			}
			before := m.Clone()
			for n := 1; n <= 3; n++ {
				at = at.Add(time.Second)
				c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Scheduled: at, Generation: m.Generation + 1,
					Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_missing")}
				if err := validateCommand(c); err != nil {
					t.Fatal(err)
				}
				r := transition(m, c)
				m = *r.Monitor
				if m.LastCheck != before.LastCheck || m.LastOutcome != before.LastOutcome || m.LastFailure != before.LastFailure ||
					m.TotalChecks != before.TotalChecks || m.SuccessfulChecks != before.SuccessfulChecks || m.ConsecutiveFailures != before.ConsecutiveFailures ||
					m.Recovering != before.Recovering || m.Incident != before.Incident || m.IncidentID != before.IncidentID || m.VerifyRemaining != before.VerifyRemaining ||
					!reflect.DeepEqual(m.Actions, before.Actions) || m.ConfigurationProblem.Code != "ca_file_missing" || m.NextCheck != at.Add(m.Policy.Interval) {
					t.Fatal("local preparation changed health or admitted work")
				}
				if n == 1 && len(r.Events) != 1 || n > 1 && len(r.Events) != 0 {
					t.Fatal("preparation transition history is missing or repeated")
				}
				if m.manualRecoveryEligible(at) {
					t.Fatal("manual recovery admitted without a usable check")
				}
				for _, action := range m.Actions {
					if action.Kind != "intervention" || action.State != Queued {
						continue
					}
					if result := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at}); result.Allowed {
						t.Fatal("queued recovery started while check configuration was broken")
					}
				}
			}
			m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second), Generation: m.Generation + 1, Outcome: "success"}).Monitor
			if m.ConfigurationProblem.Code != "" || !m.ConfigurationProblemAt.IsZero() || m.SuccessfulChecks != before.SuccessfulChecks+1 {
				t.Fatal("actual observation did not clear configuration problem")
			}
		})
	}
}

func TestCheckPreparationDiagnosticFormatAndRestart(t *testing.T) {
	d := NewFailureDiagnostic("ca_file_invalid")
	c := Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", At: policyTime(), Generation: 1, Outcome: "configuration_problem", Failure: d}
	if commandMinimumFormat(c) != ActionFormatVersion || d.validate(LifecycleFormatVersion) == nil {
		t.Fatal("preparation bypassed format gate")
	}
	for _, kind := range []string{"result", "start"} {
		bad := c
		bad.Kind, bad.ActionID = kind, "action"
		if validateCommand(bad) == nil {
			t.Fatal("non-check accepted preparation diagnostic")
		}
	}
	config := testConfig(t)
	store, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configure(t, store)
	r := submit(t, store, c)[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := store.Get(c.MonitorID)
	if !ok || m.ConfigurationProblem != d || m.ConfigurationProblemAt != c.At || m.Incident || m.TotalChecks != 0 || len(m.Actions) != 0 {
		t.Fatal("restart changed preparation into health or external work")
	}
}
