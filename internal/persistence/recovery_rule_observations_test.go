package persistence

import (
	"testing"
	"time"
)

func TestRuleObservationSeparatesQualificationFromAdmission(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	at := m.LastCheck.Add(time.Millisecond)
	queued := queuedRuleAction(t, m)
	got := m.RuleObservation(at)
	admission := m.RecoveryAdmissionObservation(at)
	if got.QualificationReason != "qualified" || admission == nil || admission.Reason != "intervention_pending" || admission.AttemptsUsed != 1 || admission.AttemptLimit != 3 || admission.Deadline == nil || *admission.Deadline != queued.Deadline {
		t.Fatalf("qualified observation lost its independent blocker: %+v %+v", got, admission)
	}
	m = dispatchTransition(t, m, Command{Kind: "start", ActionID: queued.ID, At: at})
	m = dispatchTransition(t, m, Command{Kind: "result", ActionID: queued.ID, At: at.Add(time.Millisecond), Outcome: "failure", Ambiguous: true})
	got = m.RuleObservation(at.Add(time.Second))
	admission = m.RecoveryAdmissionObservation(at.Add(time.Second))
	if got.QualificationReason != "qualified" || admission.Reason != "intervention_outcome_held" || m.manualRecoveryEligible(at.Add(time.Second)) {
		t.Fatal("rule qualification bypassed unknown intervention", got)
	}
	// Public observations are copies, never a way to modify the selected budget.
	*admission.Deadline = time.Time{}
	if m.RecoveryAdmissionObservation(at).Deadline.IsZero() {
		t.Fatal("public deadline leaked mutable ownership")
	}
}

func TestRuleAdmissionExplainsCooldownWithoutGrantingPermission(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	a := queuedRuleAction(t, m)
	start := m.LastCheck.Add(time.Millisecond)
	m = dispatchTransition(t, m, Command{Kind: "start", ActionID: a.ID, At: start})
	m = dispatchTransition(t, m, Command{Kind: "result", ActionID: a.ID, At: start.Add(time.Millisecond), Outcome: "failure", Retryable: true})
	m.Policy.RecoveryCooldown = 30 * time.Second
	at := start.Add(time.Second).In(time.FixedZone("fixture", 7200))
	admission := m.RecoveryAdmissionObservation(at)
	if admission.Reason != "cooldown" || admission.NotBefore == nil || !admission.NotBefore.After(at) || admission.ObservedAt.Location() != time.UTC {
		t.Fatalf("cooldown/UTC missing: %+v", admission)
	}
}
