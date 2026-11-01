package persistence

import (
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (s RecoverySelection) Public() *api.RecoverySelection {
	if s == (RecoverySelection{}) {
		return nil
	}
	return &api.RecoverySelection{RuleID: s.RuleID, Driver: s.Target.Driver, ConfigurationRevision: s.Configuration, DecisionRevision: s.Decision, IncidentID: s.IncidentID, ObservationID: strconv.FormatUint(s.Observation, 10)}
}

func (m Monitor) RuleObservation(at time.Time) *api.RecoveryDecision {
	if m.Policy.RecoveryRules.IsZero() && m.RecoveryDecision.Selection == (RecoverySelection{}) {
		return nil
	}
	d := m.RecoveryDecision
	observation := ""
	if d.Observation != 0 {
		observation = strconv.FormatUint(d.Observation, 10)
	}
	reason := d.Reason
	if m.recoveryRuleQualified() {
		switch {
		case m.unresolvedIntervention():
			reason = "intervention_outcome_held"
		case m.pendingIntervention():
			reason = "intervention_pending"
		case m.recoveryAttempts() >= m.Policy.recoveryLimit():
			reason = "attempt_limit"
		case m.VerifyRemaining > 0:
			reason = "verifying_recovery"
		}
	}
	return &api.RecoveryDecision{TaxonomyVersion: m.Policy.RecoveryRules.Taxonomy(), ConfigurationRevision: m.Revision, DecisionRevision: d.Revision, CandidateRuleID: d.RuleID, ObservationID: observation, Reason: reason, QualificationReason: d.Reason, QualifyingObservations: d.Count, RequiredObservations: max(1, m.Policy.Unhealthy), Selected: d.Selection.Public()}
}

// RecoveryAdmissionObservation reports policy state for both single actions and
// rules. It grants no execution authority and does not allocate an attempt.
func (m Monitor) RecoveryAdmissionObservation(at time.Time) *api.RecoveryAdmission {
	if !m.Policy.Intervention && m.recoveryAttempts() == 0 {
		return nil
	}
	admission := &api.RecoveryAdmission{Reason: m.recoveryEligibility(at), ObservedAt: at.UTC(), AttemptsUsed: m.recoveryAttempts(), AttemptLimit: m.Policy.recoveryLimit()}
	deadline := m.RecoveryDecision.Deadline
	if a, ok := m.latestRecovery(); ok && deadline.IsZero() {
		deadline = a.Deadline
	}
	if !deadline.IsZero() {
		admission.Deadline = api.Pointer(deadline.UTC())
	}
	prospective := m
	prospective.InterventionAttempts = m.recoveryAttempts() + 1
	if due := prospective.manualRecoveryDue(at); due.After(at) {
		admission.NotBefore = api.Pointer(due.UTC())
		if admission.Reason == "eligible" {
			admission.Reason = "cooldown"
		}
	}
	return admission
}
