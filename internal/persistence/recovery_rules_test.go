package persistence

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRecoveryRulesRequireConfigurationHashBeforeAdmission(t *testing.T) {
	for _, revision := range []string{"original", "12f4de8b-0379-493b-8ebc-d5b912d8b373", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		t.Run(revision, func(t *testing.T) {
			s := openCatalogMemory(t)
			m := ruleMonitor(t, 1)
			m.Revision = revision
			command := Command{Kind: "configure", MonitorID: m.ID, Revision: revision, Config: &m, At: time.Now().UTC()}
			if _, err := s.Submit(t.Context(), []Command{command}); !errors.Is(err, ErrRecoveryRules) {
				t.Fatal("invalid rule configuration reached committed admission", err)
			}
			if _, found := s.Get(m.ID); found {
				t.Fatal("rejected rule configuration installed a monitor")
			}
			if err := validateRecoveryRuleState(m, RecoveryRulesFormatVersion); !errors.Is(err, ErrRecoveryRules) {
				t.Fatal("snapshot validation accepted the invalid rule revision", err)
			}
			// A revision is an opaque identity for ruleless historical monitors.
			// Typed observations alone must not change that compatibility rule.
			m.Policy.RecoveryRules = RecoveryRules{}
			m.RecoveryDecision = RecoveryDecision{}
			for _, version := range []int{NotificationConditionFormatVersion, RecoveryRulesFormatVersion} {
				result := transitionAtFormat(Monitor{}, command, version)
				if result.Err != nil || result.Monitor == nil || result.Monitor.Revision != revision {
					t.Fatal("ruleless revision compatibility changed", version, result.Err)
				}
				if err := validateNotificationIdentity(*result.Monitor, version); err != nil {
					t.Fatal("ruleless historical state became invalid", version, err)
				}
			}
			results, err := s.Submit(t.Context(), []Command{command})
			if err != nil || len(results) != 1 || results[0].Err != nil {
				t.Fatal("current ruleless configure rejected an opaque revision", err)
			}
			if installed, found := s.Get(m.ID); !found || installed.Revision != revision || !installed.Policy.RecoveryRules.IsZero() {
				t.Fatal("current ruleless configure did not install the original revision")
			}
			failure, err := api.CanonicalFailure(api.FailureTaxonomyVersion, "network.connection_refused", "connect")
			if err != nil {
				t.Fatal(err)
			}
			m.LastFailure = CheckFailure(failure)
			if err := validateRecoveryRuleState(m, RecoveryRulesFormatVersion); err != nil {
				t.Fatal("typed failure imposed a rule-only revision restriction", err)
			}
		})
	}
}

func TestRecoveryRulesHistoricalGrowthDoesNotEnterQualification(t *testing.T) {
	var baseline float64
	for _, count := range []int{0, 500, 5000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, old := seedArchivedAttempts(t, count)
			if err := s.migrateActionProjection(t.Context()); err != nil {
				t.Fatal(err)
			}
			config := ruleMonitor(t, 4)
			config.ID, config.Revision = old.ID, identity("rules-revision")
			at := time.Now().UTC()
			if got := submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})[0]; got.Err != nil {
				t.Fatal(got.Err)
			}
			failure, err := api.CanonicalFailure("1", "network.connection_refused", "connect")
			if err != nil {
				t.Fatal(err)
			}
			// Make retained storage inaccessible: this unqualified observation must
			// update only bounded current state, regardless of lifetime history.
			s.fsm.mu.Lock()
			ledger := s.fsm.collections
			s.fsm.collections = nil
			s.fsm.mu.Unlock()
			result := submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, Outcome: "failure", Failure: CheckFailure(failure), At: at.Add(time.Second)})[0]
			s.fsm.mu.Lock()
			s.fsm.collections = ledger
			s.fsm.mu.Unlock()
			if result.Err != nil || result.Monitor == nil || result.Monitor.RecoveryDecision.Count != 1 || len(result.Monitor.Actions) != 0 {
				t.Fatal("qualification accessed retained history", result.Err)
			}
			allocations := testing.AllocsPerRun(100, func() { _ = result.Monitor.Clone() })
			if count == 0 {
				baseline = allocations
			} else if allocations != baseline {
				t.Fatal("history increased projection allocations", allocations, baseline)
			}
			t.Logf("retained=%d historical access=0 clone allocations=%g", count, allocations)
		})
	}
}

func ruleMonitor(t *testing.T, threshold int) Monitor {
	t.Helper()
	rules := []api.RulePredicate{
		{ID: "network", Match: api.FailureMatch{Codes: []string{"network.connection_refused", "network.connection_reset"}}},
		{ID: "http", Match: api.FailureMatch{Codes: []string{"unexpected_http_status"}}},
	}
	targets := []RecoveryRuleTarget{
		{RuleID: "network", Driver: "webhook", Binding: identity("network"), Budget: time.Minute},
		{RuleID: "http", Driver: "docker", Binding: identity("http"), Budget: time.Minute},
	}
	p, err := NewRecoveryRules("http", "", "1", rules, targets)
	if err != nil {
		t.Fatal(err)
	}
	m := Monitor{ID: "rules", Name: "rules", Revision: identity("configuration"), Policy: Policy{Interval: time.Second, Enabled: true, Intervention: true, Unhealthy: threshold, Healthy: 2, RecoveryMaxAttempts: 3, RecoveryRules: p}}
	r := transitionAtFormat(Monitor{}, Command{Kind: "configure", At: policyTime(), MonitorID: m.ID, Revision: m.Revision, Config: &m}, RecoveryRulesFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	return *r.Monitor
}

func rulePulse(t *testing.T, m Monitor, code string) Monitor {
	t.Helper()
	phase := "connect"
	if code == "unexpected_http_status" {
		phase = "response"
	}
	if code == "ca_file_missing" {
		phase = "local"
	}
	f, _ := api.CanonicalFailure("1", code, phase)
	if code == "unexpected_http_status" {
		value := 503
		f.HTTPStatus = &value
	}
	c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: policyTime().Add(time.Duration(m.Generation+1) * time.Second), Generation: m.Generation + 1, Outcome: "failure", Failure: CheckFailure(f)}
	if code == "" {
		c.Outcome, c.Failure = "success", FailureDiagnostic{}
	}
	if code == "ca_file_missing" {
		c.Outcome = "configuration_problem"
	}
	c.ExecutionStart, c.ExecutionEnd = c.At.Add(-time.Millisecond), c.At
	r := transitionAtFormat(m, c, RecoveryRulesFormatVersion)
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("pulse", r.Err)
	}
	if err := validateNotificationIdentity(*r.Monitor, RecoveryRulesFormatVersion); err != nil {
		t.Fatal("state", err)
	}
	return *r.Monitor
}

func queuedRuleAction(t *testing.T, m Monitor) Action {
	t.Helper()
	for _, a := range m.Actions {
		if a.Kind == "intervention" && a.State == Queued {
			return a
		}
	}
	t.Fatal("no queued rule action")
	return Action{}
}

func TestRecoveryRulesQualifySameRuleWithoutCombiningDifferentRules(t *testing.T) {
	m := ruleMonitor(t, 3)
	m = rulePulse(t, m, "network.connection_refused")
	m = rulePulse(t, m, "unexpected_http_status")
	m = rulePulse(t, m, "network.connection_reset")
	if m.RecoveryDecision.Count != 1 || m.recoveryAttempts() != 0 || !m.Incident {
		t.Fatalf("incident/qualification mixed: %+v", m.RecoveryDecision)
	}
	m = rulePulse(t, m, "network.connection_refused")
	m = rulePulse(t, m, "network.connection_reset")
	a := queuedRuleAction(t, m)
	if a.Rule.RuleID != "network" || a.Rule.Observation != 5 || a.Rule.Target.Driver != "webhook" || a.Cause.Code != "network.connection_reset" {
		t.Fatal("incorrect selection", a.Rule)
	}
}

func TestRecoveryRulesFirstStartPinsAndUnknownDoesNotFreezeHealth(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	a := queuedRuleAction(t, m)
	start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "webhook", At: policyTime().Add(2 * time.Second), NoticeRevision: ActionNoticeRevision(a)}
	r := transitionAtFormat(m, start, RecoveryRulesFormatVersion)
	if !r.Allowed {
		t.Fatal("qualified start denied", r.Err)
	}
	m = *r.Monitor
	if m.RecoveryDecision.Selection != a.Rule {
		t.Fatal("selection not committed")
	}
	r = transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: start.At.Add(time.Millisecond), Outcome: "failure", Ambiguous: true}, RecoveryRulesFormatVersion)
	m = *r.Monitor
	m = rulePulse(t, m, "unexpected_http_status")
	if m.RecoveryDecision.Reason != "incident_selection_pinned" || m.recoveryAttempts() != 1 || m.Actions[a.ID].State != Unknown {
		t.Fatal("unknown action allowed another rule")
	}
	m = rulePulse(t, m, "")
	m = rulePulse(t, m, "")
	if m.Incident || m.Recovering || m.RecoveryDecision.Selection != (RecoverySelection{}) || !m.Actions[a.ID].Held() {
		t.Fatal("health recovery changed unknown evidence")
	}
	m = rulePulse(t, m, "unexpected_http_status")
	if !m.pendingIntervention() || m.recoveryAttempts() != 0 {
		t.Fatal("later outage bypassed hold")
	}
}

func TestRecoveryRulesQueuedReplacementPreservesAllowance(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	first := queuedRuleAction(t, m)
	m = rulePulse(t, m, "unexpected_http_status")
	second := queuedRuleAction(t, m)
	if second.Rule.RuleID != "http" || second.ID == first.ID || second.Attempt != 2 || !second.Deadline.Equal(first.Deadline) {
		t.Fatal("replacement renewed attempt or deadline", second)
	}
	r := transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: first.ID, Driver: "webhook", At: policyTime().Add(3 * time.Second), NoticeRevision: ActionNoticeRevision(first)}, RecoveryRulesFormatVersion)
	if r.Allowed {
		t.Fatal("obsolete action started")
	}
}

func TestRecoveryRulesUnavailableAndSuppressionResetQualification(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 2), "network.connection_refused")
	before := m.IncidentID
	m = rulePulse(t, m, "ca_file_missing")
	if m.RecoveryDecision.Count != 0 || m.IncidentID != before || m.LastOutcome != "failure" {
		t.Fatal("unavailable observation changed incident facts")
	}
	m = rulePulse(t, m, "network.connection_refused")
	if m.RecoveryDecision.Count != 1 || m.recoveryAttempts() != 0 {
		t.Fatal("stale observations qualified")
	}
	c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: policyTime().Add(4 * time.Second), Generation: 4, Outcome: "failure", Failure: m.LastFailure, Maintenance: true}
	m = *transitionAtFormat(m, c, RecoveryRulesFormatVersion).Monitor
	m = rulePulse(t, m, "network.connection_refused")
	if m.RecoveryDecision.Count != 1 || m.recoveryAttempts() != 0 {
		t.Fatal("maintenance reused qualification")
	}
}

func TestRecoveryRulesSnapshotFrozenAndHistoricalFormatsRefused(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 2), "network.connection_refused")
	frozen := m.Clone()
	data, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	m = rulePulse(t, m, "network.connection_reset")
	var restored Monitor
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.RecoveryDecision.Count != 1 || frozen.RecoveryDecision.Count != 1 || m.RecoveryDecision.Count != 2 {
		t.Fatal("snapshot moved with live state")
	}
	if err = validateNotificationIdentity(restored, 46); err == nil {
		t.Fatal("older format accepted rules")
	}
	if err = validateNotificationIdentity(restored, 48); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(restored.Policy.RecoveryRules)
	copy := restored.Policy.RecoveryRules
	if err = json.Unmarshal([]byte(`{"driver":"http","taxonomy":"1","rules":[],"targets":[]}`), &copy); err == nil {
		t.Fatal("invalid policy accepted")
	}
	after, _ := json.Marshal(restored.Policy.RecoveryRules)
	if string(before) != string(after) || strings.Contains(string(data), "credential") {
		t.Fatal("immutable state changed or contained credentials")
	}
}

func TestRecoveryRulesProgramOwnership(t *testing.T) {
	a, b := ruleMonitor(t, 1).Policy.RecoveryRules, ruleMonitor(t, 1).Policy.RecoveryRules
	var cache recoveryProgramCache
	first := cache.replace(RecoveryRules{}, a)
	second := cache.replace(RecoveryRules{}, b)
	if first.data.compiled != second.data.compiled || cache.entries[first.data.compiled.Digest()].users != 2 {
		t.Fatal("program not shared")
	}
	second = cache.replace(second, second)
	cache.replace(first, RecoveryRules{})
	if cache.entries[second.data.compiled.Digest()].users != 1 {
		t.Fatal("membership lost")
	}
	cache.replace(second, RecoveryRules{})
	if len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatal("program leaked")
	}
	// A privately owned policy predates an otherwise equal cached program.
	first = cache.replace(RecoveryRules{}, a)
	second = cache.replace(b, b)
	if cache.entries[first.data.compiled.Digest()].users != 2 {
		t.Fatal("private policy mistaken for a counted user")
	}
	var rebuilt recoveryProgramCache
	fresh := rebuilt.replace(RecoveryRules{}, second)
	if rebuilt.entries[fresh.data.compiled.Digest()].users != 1 {
		t.Fatal("snapshot membership reused after rebuild")
	}
}

func TestRecoveryRulesShortenedStartDeadlineIsNeverRenewed(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	a := queuedRuleAction(t, m)
	deadline := a.Deadline.Add(-30 * time.Second)
	c := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "webhook", At: policyTime().Add(2 * time.Second), ActionDeadline: deadline, NoticeRevision: ActionNoticeRevision(a)}
	r := transitionAtFormat(m, c, RecoveryRulesFormatVersion)
	if !r.Allowed {
		t.Fatal(r.Err)
	}
	m = *r.Monitor
	r = transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: c.At.Add(time.Millisecond), Outcome: "failure", Retryable: true}, RecoveryRulesFormatVersion)
	m = *r.Monitor
	m = rulePulse(t, m, "network.connection_refused")
	next := queuedRuleAction(t, m)
	if !next.Deadline.Equal(deadline) || next.Rule != a.Rule {
		t.Fatal("retry renewed deadline or selected another action", next)
	}
}

func TestRecoveryRulesCancelledUnstartedIntentCanRequalify(t *testing.T) {
	for _, reason := range []string{"monitor_ignored", "snooze", "maintenance", "recovery_rule_changed"} {
		t.Run(reason, func(t *testing.T) {
			m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
			a := queuedRuleAction(t, m)
			a.State, a.Outcome, a.FinishedAt = Cancelled, reason, policyTime().Add(2*time.Second)
			m.Actions[a.ID] = a
			m.resetRuleQualification("fresh_observations_required")
			m = rulePulse(t, m, "network.connection_refused")
			next := queuedRuleAction(t, m)
			if next.Attempt != 2 || !next.Deadline.Equal(a.Deadline) {
				t.Fatal("cancellation refunded allowances")
			}
		})
	}
}

func TestRecoveryRulesCrossedMaintenanceRequiresFreshQualification(t *testing.T) {
	m := rulePulse(t, ruleMonitor(t, 1), "network.connection_refused")
	a := queuedRuleAction(t, m)
	m.Policy.Maintenance = []MaintenanceWindow{{Start: policyTime().Add(1100 * time.Millisecond), End: policyTime().Add(1500 * time.Millisecond)}}
	c := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "webhook", At: policyTime().Add(2 * time.Second), NoticeRevision: ActionNoticeRevision(a)}
	r := transitionAtFormat(m, c, RecoveryRulesFormatVersion)
	if r.Allowed || r.Monitor.Actions[a.ID].State != Cancelled || r.Monitor.RecoveryDecision.Count != 0 {
		t.Fatal("pre-maintenance evidence authorized start")
	}
	m = rulePulse(t, *r.Monitor, "network.connection_refused")
	next := queuedRuleAction(t, m)
	if next.Attempt != 2 || !next.Deadline.Equal(a.Deadline) {
		t.Fatal("replacement renewed allowance")
	}
	c.ActionID, c.NoticeRevision, c.At = next.ID, ActionNoticeRevision(next), policyTime().Add(3*time.Second)
	r = transitionAtFormat(m, c, RecoveryRulesFormatVersion)
	if !r.Allowed {
		t.Fatal("fresh post-maintenance evidence rejected", r.Err)
	}
}

func TestRecoveryRulesOperationDigestBindsNewPreconditions(t *testing.T) {
	c := Command{Kind: "manual_recovery", ManualRecovery: &ManualRecoveryCommand{MonitorUID: "monitor", ExpectedControlRevision: "control", OperationID: "operation", Actor: "operator", Reason: "requested"}}
	old, err := operationDigestEncoding(c)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := json.Marshal(legacyOperationCommandDigest(c))
	if err != nil || string(old) != string(historical) {
		t.Fatal("historical identity changed")
	}
	c.ManualRecovery.RuleID, c.ManualRecovery.ExpectedDecision, c.ManualRecovery.ExpectedConfiguration = "network", "decision", "configuration"
	current, err := operationDigestEncoding(c)
	if err != nil || string(current) == string(old) || !strings.Contains(string(current), "cpra/recovery-rules-operation/v1") {
		t.Fatal("new preconditions omitted", err)
	}
	for _, field := range []*string{&c.ManualRecovery.RuleID, &c.ManualRecovery.ExpectedDecision, &c.ManualRecovery.ExpectedConfiguration} {
		before := *field
		*field += "changed"
		changed, _ := operationDigestEncoding(c)
		if string(changed) == string(current) {
			t.Fatal("precondition did not bind operation")
		}
		*field = before
	}
}

func TestManualRecoveryRulesRequireCurrentDecisionAndKeepSelection(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "manual-rules")
	m.Revision = identity("manual-rules-configuration")
	m.Policy = ruleMonitor(t, 2).Policy
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: guard, At: policyTime()}).Monitor
	pulse := func(code string, at time.Time) {
		t.Helper()
		phase := "connect"
		if code == "unexpected_http_status" {
			phase = "response"
		}
		failure, err := api.CanonicalFailure("1", code, phase)
		if err != nil {
			t.Fatal(err)
		}
		if code == "unexpected_http_status" {
			status := 503
			failure.HTTPStatus = &status
		}
		result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, CheckControlRevision: m.ControlRevision, Guard: guard, At: at, Generation: m.Generation + 1, Outcome: "failure", Failure: CheckFailure(failure)})[0]
		if result.Err != nil || result.Monitor == nil {
			t.Fatal("observation commit", result.Err)
		}
		m = *result.Monitor
	}
	request := func(id string, at time.Time) Command {
		c := manualCommand(m, guard, id, at)
		c.ManualRecovery.RuleID = m.RecoveryDecision.RuleID
		c.ManualRecovery.ExpectedDecision = m.RecoveryDecision.Revision
		c.ManualRecovery.ExpectedConfiguration = m.Revision
		return c
	}
	pulse("network.connection_refused", policyTime().Add(time.Second))
	if r := submit(t, s, request("below-threshold", policyTime().Add(2*time.Second)))[0]; r.Allowed || r.Err == nil {
		t.Fatal("manual request bypassed the unhealthy qualification threshold")
	}
	pulse("network.connection_refused", policyTime().Add(3*time.Second))
	first := queuedRuleAction(t, m)
	m = *mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: first.ID, Driver: first.Rule.Target.Driver, Guard: guard, At: policyTime().Add(4 * time.Second), NoticeRevision: ActionNoticeRevision(first)}).Monitor
	first = m.Actions[first.ID]
	if first.Deadline.IsZero() {
		t.Fatal("first committed start did not establish the intervention deadline")
	}
	finished := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: first.ID, At: policyTime().Add(5 * time.Second), Outcome: "failure"})[0]
	if finished.Err != nil || finished.Monitor == nil {
		t.Fatal("result commit", finished.Err)
	}
	m = *finished.Monitor
	for _, field := range []string{"rule", "decision", "configuration", "control", "missing"} {
		c := request("stale-"+field, policyTime().Add(6*time.Second))
		switch field {
		case "rule":
			c.ManualRecovery.RuleID = "http"
		case "decision":
			c.ManualRecovery.ExpectedDecision = identity("stale-decision")
		case "configuration":
			c.ManualRecovery.ExpectedConfiguration = identity("stale-configuration")
		case "control":
			c.ManualRecovery.ExpectedControlRevision = "stale-control"
		case "missing":
			c.ManualRecovery.RuleID, c.ManualRecovery.ExpectedDecision, c.ManualRecovery.ExpectedConfiguration = "", "", ""
		}
		if r := submit(t, s, c)[0]; r.Allowed || r.Err == nil {
			t.Fatalf("manual request accepted %s precondition", field)
		}
	}
	m, _ = s.Get(m.ID)
	if m.recoveryAttempts() != 1 {
		t.Fatal("rejected requests consumed or renewed the incident allowance")
	}
	m = *mustResult(t, s, request("exact-selection", policyTime().Add(7*time.Second))).Monitor
	manual := queuedRuleAction(t, m)
	if !manual.Manual || manual.Attempt != 2 || manual.Rule != first.Rule || !manual.Deadline.Equal(first.Deadline) {
		t.Fatal("manual request replaced the pinned selection or renewed its budget")
	}
	pulse("unexpected_http_status", policyTime().Add(8*time.Second))
	if r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: manual.ID, Driver: manual.Rule.Target.Driver, Guard: guard, At: policyTime().Add(9 * time.Second), NoticeRevision: ActionNoticeRevision(manual)})[0]; r.Allowed {
		t.Fatal("manual action started after its qualifying condition changed")
	}
}

func TestRecoveryRulesV4CollectionPromotesOldStoreBeforeSnapshot(t *testing.T) {
	s := openReplayMemory(t)
	s.fsm.image.Version = NotificationConditionFormatVersion
	head := reselectionCreate(t, s, "cpra.file.base.v4", 1)
	if s.fsm.image.Version != RecoveryRulesFormatVersion {
		t.Fatal("v4 profile retained in an older image")
	}
	if head.NormalizationProfile != "cpra.file.base.v4" {
		t.Fatal("profile changed")
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Release()
}
