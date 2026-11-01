package persistence

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func dispatchTransition(t *testing.T, m Monitor, c Command) Monitor {
	t.Helper()
	c.MonitorID, c.Revision = m.ID, m.Revision
	if c.Kind == "pulse" {
		c.Generation = m.Generation + 1
		if c.ExecutionStart.IsZero() {
			c.ExecutionStart = c.At
			c.ExecutionEnd = c.At
		}
	}
	if c.Kind == "start" {
		c.NoticeRevision = ActionNoticeRevision(m.Actions[c.ActionID])
		c.Driver = m.Actions[c.ActionID].Rule.Target.Driver
	}
	r := transitionAtFormat(m, c, DispatchFormatVersion)
	if c.Kind == "start" && r.Monitor == nil && r.Err == nil && !r.Allowed {
		return m
	}
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("dispatch transition", c.Kind, r.Err)
	}
	return *r.Monitor
}

func TestRecoveryBudgetBeginsAtFirstStartAndSurvivesRestart(t *testing.T) {
	for _, rules := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "rules"}[rules], func(t *testing.T) {
			p := lifecyclePolicy()
			p.Intervention = true
			p.RecoveryTimeout = time.Minute
			p.RecoveryMaxAttempts = 3
			m := configuredPolicyMonitor(t, p)
			if rules {
				m = ruleMonitor(t, 1)
			}
			at := policyTime().Add(time.Second)
			failure, _ := api.CanonicalFailure("1", "network.connection_refused", "connect")
			m = dispatchTransition(t, m, Command{Kind: "pulse", At: at, Outcome: "failure", Failure: CheckFailure(failure)})
			a := interventionAction(t, m)
			if a.ExecutionBudget <= 0 || !a.Deadline.IsZero() {
				t.Fatal("queue consumed budget", a)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m = dispatchTransition(t, m, Command{Kind: "recover", At: at.Add(5 * time.Minute)})
			m = dispatchTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: at.Add(5 * time.Minute)})
			if m.Actions[a.ID].State != Queued {
				t.Fatal("restart bypassed fresh observation")
			}
			started := at.Add(5*time.Minute + time.Second)
			m = dispatchTransition(t, m, Command{Kind: "pulse", At: started, Outcome: "failure", Failure: CheckFailure(failure)})
			m = dispatchTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: started})
			a = m.Actions[a.ID]
			if a.State != Started || !a.Deadline.Equal(started.Add(a.ExecutionBudget)) {
				t.Fatal("first start deadline", a)
			}
			if rules && !m.RecoveryDecision.Deadline.Equal(a.Deadline) {
				t.Fatal("rule pin precedes authoritative deadline")
			}
			m = dispatchTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: started.Add(time.Second)})
			if m.Actions[a.ID].Deadline != a.Deadline {
				t.Fatal("repeated start renewed deadline")
			}
			m = dispatchTransition(t, m, Command{Kind: "recover", At: started.Add(2 * time.Second)})
			if m.Actions[a.ID].State != Unknown || m.Actions[a.ID].Deadline != a.Deadline {
				t.Fatal("restart repeated uncertain effect")
			}
			if err := validateActionFormat(m, DispatchFormatVersion); err != nil {
				t.Fatal(err)
			}
			if err := validateActionFormat(m, RecoveryRulesFormatVersion); err == nil {
				t.Fatal("downgrade accepted new budget")
			}
		})
	}
}

func TestDispatchUpgradeNeverRenewsHistoricalIntervention(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	a := queuedRuleAction(t, m)
	if a.Deadline.IsZero() || a.ExecutionBudget != 0 {
		t.Fatal("historical fixture")
	}
	m = dispatchTransition(t, m, Command{Kind: "recover", At: a.Deadline.Add(time.Second)})
	got := m.Actions[a.ID]
	if got.State != Cancelled || got.Deadline != a.Deadline || got.ExecutionBudget != 0 {
		t.Fatal("upgrade manufactured execution budget", got)
	}
}
