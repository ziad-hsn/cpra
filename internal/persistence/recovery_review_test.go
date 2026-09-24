package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func manualFixture(t *testing.T, s *Store) (Monitor, *CatalogGuard) {
	t.Helper()
	m, g := managedControlMonitor(t, s, "manual")
	m.Policy.Unhealthy = 100
	m.Policy.RecoveryMaxAttempts = 10
	m.Policy.Endpoints = nil
	r := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: policyTime()})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	m = controlPulse(t, s, *r.Monitor, g, policyTime().Add(time.Second), "failure")
	return m, g
}
func manualCommand(m Monitor, g *CatalogGuard, id string, at time.Time) Command {
	return Command{Kind: "manual_recovery", MonitorID: m.ID, Guard: g, At: at, ManualRecovery: &ManualRecoveryCommand{MonitorUID: m.CatalogUID, ExpectedControlRevision: m.ControlRevision, OperationID: id, Actor: "oncall", Reason: "Confirmed service outage", Limits: runtimeconfig.ManualRecovery{}.Effective()}}
}
func reviewCommand(m Monitor, a Action, id, resolution string, at time.Time) Command {
	return Command{Kind: "action_review", MonitorID: m.ID, At: at, ActionReview: &ActionReviewCommand{MonitorUID: a.CatalogUID, ActionID: a.ID, ExpectedRevision: ActionReviewRevision(a), Revision: id, OperationID: id, Actor: "oncall", Reason: "Examined provider evidence", Note: "Independent verification", Resolution: resolution, EvidenceRefs: []string{"incident/123", "provider/audit/456"}}}
}
func mustResult(t *testing.T, s *Store, c Command) Result {
	t.Helper()
	r := submit(t, s, c)[0]
	if !r.Allowed || r.Err != nil || r.Monitor == nil {
		t.Fatalf("%s failed: %+v", c.Kind, r)
	}
	return r
}
func latestManual(t *testing.T, m Monitor) Action {
	t.Helper()
	a, ok := m.latestRecovery()
	if !ok {
		t.Fatal("no recovery action")
	}
	return a
}
func finishManualFailure(t *testing.T, s *Store, m Monitor, g *CatalogGuard, at time.Time) Monitor {
	t.Helper()
	a := latestManual(t, m)
	m = *mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: g, At: at}).Monitor
	r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Millisecond), Outcome: "failure"})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	return *r.Monitor
}
func TestManualRecoveryAdmissionRateBudgetAndReceipts(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := policyTime().Add(2 * time.Second)
	for n := 0; n < 3; n++ {
		c := manualCommand(m, g, fmt.Sprintf("request-%d", n), at.Add(time.Duration(n)*time.Minute))
		// The facade cannot override the deployment's rate policy.
		c.ManualRecovery.Limits = runtimeconfig.ManualRecovery{MinimumInterval: time.Second, PerHour: 100}
		results, err := s.RequestRecovery(context.Background(), c)
		if err != nil || results[0].Err != nil || !results[0].Allowed {
			t.Fatal(results, err)
		}
		r := results[0]
		m = *r.Monitor
		a := latestManual(t, m)
		if !a.Manual || a.OperationID != c.ManualRecovery.OperationID || r.Operation.Subject != "recovery" || r.Operation.ActionID != a.ID || a.Attempt != n+1 {
			t.Fatal("manual receipt or budget", r.Operation, a)
		}
		if n == 0 {
			if r := submit(t, s, manualCommand(m, g, "while-queued", c.At))[0]; !errors.Is(r.Err, ErrRecoveryIneligible) {
				t.Fatal("active recovery allowed", r.Err)
			}
			op := *results[0].Operation
			done := submit(t, s, Command{Kind: "operation", At: c.At, Operation: &OperationUpdate{ID: op.ID, Key: op.Key, UID: op.UID, Revision: op.NewVersion, Subject: op.Subject, ActionID: op.ActionID, Applied: true}})[0]
			if !done.Allowed {
				t.Fatal("exact owner receipt rejected", done.Err)
			}
		}
		m = finishManualFailure(t, s, m, g, c.At.Add(time.Millisecond))
		early := manualCommand(m, g, fmt.Sprintf("too-soon-%d", n), c.At.Add(59*time.Second))
		if r := submit(t, s, early)[0]; !errors.Is(r.Err, ErrRecoveryRateLimited) {
			t.Fatal("minute limit", r.Err)
		}
	}
	if r := submit(t, s, manualCommand(m, g, "fourth", at.Add(3*time.Minute)))[0]; !errors.Is(r.Err, ErrRecoveryRateLimited) {
		t.Fatal("hour limit", r.Err)
	}
	// A later hourly window prunes the bounded request list.
	r := mustResult(t, s, manualCommand(m, g, "next-hour", at.Add(time.Hour)))
	if len(r.Monitor.ManualRequests) != 3 {
		t.Fatal("rolling boundary did not expire only oldest request")
	}
}
func TestManualRecoveryStrictGuardsAndStartRecheck(t *testing.T) {
	for _, scenario := range []string{"disabled", "snoozed", "maintenance", "healthy", "unprobed", "unconfigured", "attempts", "verification", "cooldown", "stale-dependencies", "stale-config"} {
		t.Run(scenario, func(t *testing.T) {
			s := openCatalogMemory(t)
			m, g := manualFixture(t, s)
			at := policyTime().Add(2 * time.Second)
			s.fsm.mu.Lock()
			v := s.fsm.image.Monitors[m.ID]
			switch scenario {
			case "disabled":
				v.Policy.Enabled = false
			case "snoozed":
				v.SnoozedUntil = at.Add(time.Hour)
			case "maintenance":
				v.Policy.Maintenance = []MaintenanceWindow{{Start: at, End: at.Add(time.Hour)}}
			case "healthy":
				v.LastOutcome = "success"
			case "unprobed":
				v.LastCheck = time.Time{}
			case "unconfigured":
				v.Policy.Intervention = false
			case "attempts":
				v.InterventionAttempts = 10
			case "verification":
				v.VerifyRemaining = 1
			case "cooldown":
				v.InterventionAttempts = 1
				v.InterventionAttempted = true
				v.Policy.RecoveryCooldown = time.Hour
				v.LastInterventionFailure = at
				v.Actions["prior"] = Action{ID: "prior", Revision: v.Revision, CatalogUID: v.CatalogUID, IncidentID: v.IncidentID, Kind: "intervention", State: Failed, Attempt: 1, FinishedAt: at}
			case "stale-dependencies":
				v.DependencyRevision = "stale"
			case "stale-config":
				v.CatalogRevision = "stale"
			}
			s.fsm.image.Monitors[m.ID] = v
			s.fsm.mu.Unlock()
			r := submit(t, s, manualCommand(m, g, "must-reject", at))[0]
			if r.Allowed || r.Err == nil {
				t.Fatal("unsafe admission", r)
			}
		})
	}
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := policyTime().Add(2 * time.Second)
	m = *mustResult(t, s, manualCommand(m, g, "before-health", at)).Monitor
	a := latestManual(t, m)
	m = controlPulse(t, s, m, g, at.Add(time.Second), "success")
	if r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: g, At: at.Add(2 * time.Second)})[0]; r.Allowed {
		t.Fatal("manual action started after health recovered")
	}
}

func localUnknown(t *testing.T, s *Store) (Monitor, Action, *CatalogGuard, *LocalExecution) {
	t.Helper()
	m, g := manualFixture(t, s)
	at := policyTime().Add(2 * time.Second)
	m = *mustResult(t, s, manualCommand(m, g, "initial", at)).Monitor
	a := latestManual(t, m)
	h, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: g, At: at.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), Outcome: "failure", Ambiguous: true})[0]
	if r.Monitor == nil {
		t.Fatal(r.Err)
	}
	m = *r.Monitor
	return m, m.Actions[a.ID], g, h
}
func TestActionReviewExecutorFenceProviderFactsAndLateConflict(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, g, h := localUnknown(t, s)
	at := time.Now().UTC().Add(time.Minute)
	t.Cleanup(func() { _ = h.Finish(context.Background()) })
	if r := submit(t, s, reviewCommand(m, a, "active-review", "rejected", at))[0]; !errors.Is(r.Err, ErrExecutorUnfenced) {
		t.Fatal("active executor cleared", r.Err)
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("active executor released lock", err)
	}
	oldVersion := ActionReviewRevision(a)
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	stale := reviewCommand(m, a, "stale-review", "rejected", at)
	stale.ActionReview.ExpectedRevision = oldVersion
	if r := submit(t, s, stale)[0]; !errors.Is(r.Err, ErrActionReviewConflict) {
		t.Fatal("stale observation accepted", r.Err)
	}
	before := m.Clone()
	r := mustResult(t, s, reviewCommand(m, a, "review-one", "rejected", at))
	m = *r.Monitor
	a = m.Actions[a.ID]
	if a.State != Unknown || a.Outcome != "external_outcome_unknown" || a.Held() || a.Review.Actor != "oncall" || r.Operation.Subject != "review" {
		t.Fatal("review rewrote provider facts", a)
	}
	if m.InterventionAttempts != before.InterventionAttempts || len(m.Actions) != len(before.Actions) || m.VerifyRemaining != before.VerifyRemaining {
		t.Fatal("review rearmed work")
	}
	// Automatic failure observations cannot treat the operator assertion as a provider rejection.
	m.Policy.Unhealthy = 1
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: at}).Monitor
	m = controlPulse(t, s, m, g, at.Add(time.Second), "failure")
	if len(m.Actions) != 1 || m.InterventionAttempts != 1 {
		t.Fatal("review caused automatic retry")
	}
	// A later explicit request consumes the remaining ordinary attempt budget.
	m = *mustResult(t, s, manualCommand(m, g, "second-manual", at.Add(2*time.Minute))).Monitor
	second := latestManual(t, m)
	late := Command{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, Outcome: "success", ExecutionStart: a.StartedAt, ExecutionEnd: a.StartedAt.Add(time.Second), At: at.Add(3 * time.Minute)}
	lr := mustResult(t, s, late)
	m = *lr.Monitor
	a = m.Actions[a.ID]
	if !a.Held() || a.Review.Resolution != "rejected" || !a.Review.Conflict || a.LateEvidence.Outcome != "accepted" || len(lr.Events) < 2 {
		t.Fatal("late contradiction did not restore hold", a)
	}
	if r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, Guard: g, At: late.At})[0]; r.Allowed {
		t.Fatal("queued recovery escaped restored hold")
	}
	if op, err := s.Operation("review-one"); err != nil || op.Outcome != "superseded" {
		t.Fatal("conflicting review receipt stayed active", op, err)
	}
	if r := submit(t, s, late)[0]; !r.Allowed || r.Monitor != nil || len(r.Events) != 0 {
		t.Fatal("duplicate late evidence")
	}
	// An updated review must inspect the new facts, and cannot contradict them.
	if r := submit(t, s, reviewCommand(m, a, "bad-review", "rejected", late.At))[0]; !errors.Is(r.Err, ErrLateEvidenceConflict) {
		t.Fatal("contradictory assertion cleared hold", r.Err)
	}
	if m.Actions[second.ID].State != Cancelled {
		t.Fatal("conflict kept an unsent recovery armed")
	}
	if op, err := s.Operation("second-manual"); err != nil || op.Outcome != "superseded" {
		t.Fatal("cancelled recovery receipt stayed pending", op, err)
	}
	resolved := mustResult(t, s, reviewCommand(m, a, "fresh-accepted-review", "accepted", late.At.Add(time.Second)))
	if resolved.Monitor.Actions[a.ID].Held() || resolved.Monitor.Actions[second.ID].State != Cancelled || resolved.Monitor.InterventionAttempts != m.InterventionAttempts || len(resolved.Monitor.Actions) != len(m.Actions) {
		t.Fatal("fresh review rearmed cancelled work or reset budget")
	}
}
func TestActionReviewFrozenIndexesAndSnapshotValidation(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, _, h := localUnknown(t, s)
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	at := time.Now().UTC().Add(time.Second)
	view, err := s.ActionSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	old, _ := view.Get(a.ID)
	r := mustResult(t, s, reviewCommand(m, a, "indexed-review", "accepted", at))
	m = *r.Monitor
	a = m.Actions[a.ID]
	row, ok, err := s.Action(a.ID)
	if err != nil || !ok || row.Held || !row.ExecutorFenced || row.ReviewRevision == old.ReviewRevision {
		t.Fatal(row, ok, err)
	}
	rows, _, err := view.PageByMonitor(m.ID, "", 1)
	if err != nil || len(rows) != 1 || !rows[0].Held || rows[0].Review != nil {
		t.Fatal("frozen action view changed", rows, err)
	}
	row.Review.EvidenceRefs[0] = "tamper"
	fresh, _, _ := s.Action(a.ID)
	if fresh.Review.EvidenceRefs[0] == "tamper" {
		t.Fatal("review slice escaped")
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	frozen := snapshot.(*frozenSnapshot)
	data, err := json.Marshal(frozen.image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeImage(bytes.NewReader(data)); err != nil {
		t.Fatal("valid action snapshot rejected", err)
	}
	corrupt := frozen.image.Monitors[m.ID].Clone()
	bad := corrupt.Actions[a.ID]
	bad.Review.Resolution = "succeeded"
	corrupt.Actions[a.ID] = bad
	frozen.image.Monitors[m.ID] = corrupt
	data, _ = json.Marshal(frozen.image)
	if _, err = decodeImage(bytes.NewReader(data)); err == nil {
		t.Fatal("corrupt review restored")
	}
}
func TestRecoveryReviewValidationRejectsIgnoredPlaintextAndBounds(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	valid := manualCommand(m, g, "request", policyTime().Add(time.Second))
	for _, modify := range []func(*Command){func(c *Command) { c.Outcome = "secret-value" }, func(c *Command) { c.ManualRecovery.Reason = strings.Repeat("x", 4097) }, func(c *Command) { c.ManualRecovery.Limits.PerHour = 101 }, func(c *Command) { c.Guard = nil }} {
		c := valid
		b := *valid.ManualRecovery
		c.ManualRecovery = &b
		modify(&c)
		if err := validateCommand(c); err == nil {
			t.Fatal("invalid command admitted")
		}
	}
	if validEvidenceRefs([]string{"x", "x"}) || validEvidenceRefs([]string{"x\n"}) || validEvidenceRefs(make([]string, 9)) {
		t.Fatal("invalid references accepted")
	}
}

func TestManualRecoveryAndReviewRealRaftRestart(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	m, a, g, h := localUnknown(t, s)
	if err = h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	at := time.Now().UTC().Add(time.Second)
	m = *mustResult(t, s, reviewCommand(m, a, "restart-review", "rejected", at)).Monitor
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.Get(m.ID)
	if !reflect.DeepEqual(got.ManualRequests, m.ManualRequests) || got.Actions[a.ID].Review == nil || got.Actions[a.ID].Held() || got.InterventionAttempts != 1 {
		t.Fatal("recovery/review lost on restart", got)
	}
	if r := submit(t, s, manualCommand(got, g, "too-soon", policyTime().Add(30*time.Second)))[0]; !errors.Is(r.Err, ErrRecoveryRateLimited) {
		t.Fatal("restart forgot manual rate", r.Err)
	}
	// Frozen snapshot restore includes action index and deep ownership.
	data := captureSnapshotBytes(t, s.fsm)
	f := &machine{history: s.History()}
	if err = f.Restore(io.NopCloser(bytes.NewReader(data))); err != nil {
		t.Fatal(err)
	}
	if f.actionIndex.Len() != len(got.Actions) {
		t.Fatal("action index not rebuilt")
	}
}

func TestActionReviewHistoricalIncarnationAndContradictoryFacts(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, _, h := localUnknown(t, s)
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	at := time.Now().UTC().Add(time.Second)
	// Reuse the public monitor ID through the actual catalog and owner paths.
	record := requireCatalog(t, s, CatalogKey{Kind: "Monitor", ID: m.ID})
	deleted := catalogSubmit(t, s, deleteCatalogMutation(record, "removed-original"))
	if !deleted.Allowed {
		t.Fatal(deleted.Err)
	}
	recreated := catalogRecord(t, s, "Monitor", m.ID, "replacement-uid", "replacement-version", "{}")
	recreated.Generation = 1
	result := catalogSubmit(t, s, CatalogMutation{Record: recreated, Create: true})
	if !result.Allowed {
		t.Fatal(result.Err)
	}
	g := &CatalogGuard{Conditions: []CatalogCondition{{Key: recreated.Key, UID: recreated.UID, Revision: recreated.Revision}}}
	config := m.Clone()
	config.CatalogUID = recreated.UID
	config.Revision = "replacement-execution"
	// c.Config contains startup policy, never retained observations of the old UID.
	config.IncidentID, config.IncidentRevision, config.ControlRevision = "", "", ""
	config.IncidentSequence = 0
	config.IncidentOpenedAt, config.IncidentClosedAt = time.Time{}, time.Time{}
	config.Incident, config.Recovering, config.InterventionAttempted = false, false, false
	config.InterventionAttempts = 0
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, Guard: g, At: at}).Monitor
	before := m.Clone()
	r := mustResult(t, s, reviewCommand(m, a, "historical-review", "accepted", at))
	m = *r.Monitor
	old := m.Actions[a.ID]
	if m.CatalogUID != recreated.UID || old.CatalogUID == m.CatalogUID || m.Generation != before.Generation || m.InterventionAttempts != before.InterventionAttempts {
		t.Fatal("review changed new incarnation")
	}
	receipt := r.Operation
	done := submit(t, s, Command{Kind: "operation", At: at, Operation: &OperationUpdate{ID: receipt.ID, Key: receipt.Key, UID: receipt.UID, Revision: receipt.NewVersion, Subject: receipt.Subject, ActionID: receipt.ActionID, Applied: true}})[0]
	if !done.Allowed {
		t.Fatal("historical receipt used current incarnation", done.Err)
	}
	late := Command{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, Outcome: "success", ExecutionStart: a.StartedAt, ExecutionEnd: a.StartedAt.Add(time.Millisecond), At: at}
	m = *mustResult(t, s, late).Monitor
	late.Outcome = "failure"
	late.At = late.At.Add(time.Second)
	m = *mustResult(t, s, late).Monitor
	old = m.Actions[a.ID]
	if !old.Held() || old.ConflictingEvidence == nil || old.LateEvidence.Outcome != "accepted" || old.ConflictingEvidence.Outcome != "rejected" || old.Review.Resolution != "accepted" {
		t.Fatal("contradictory facts overwritten", old)
	}
	if r := submit(t, s, reviewCommand(m, old, "cannot-clear-conflicting-facts", "accepted", late.At))[0]; !errors.Is(r.Err, ErrLateEvidenceConflict) {
		t.Fatal("conflicting facts cleared", r.Err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	data, _ := json.Marshal(snapshot.(*frozenSnapshot).image)
	if _, err = decodeImage(bytes.NewReader(data)); err != nil {
		t.Fatal("historical action conflict cannot restore", err)
	}
}
func TestActionReviewUnclassifiedCannotClearAndReturnsDetachedCopies(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := policyTime().Add(2 * time.Second)
	r := mustResult(t, s, manualCommand(m, g, "first", at))
	m = *r.Monitor
	a := latestManual(t, m)
	r.Monitor.ManualRequests[0] = time.Time{}
	r.Monitor.Actions[a.ID] = Action{}
	current, _ := s.Get(m.ID)
	if current.Actions[a.ID].ID != a.ID || current.ManualRequests[0].IsZero() {
		t.Fatal("mutable result escaped into committed state")
	}
	m = *mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: g, At: at}).Monitor
	result := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at, Outcome: "failure", Ambiguous: true})[0]
	m = *result.Monitor
	a = m.Actions[a.ID]
	if r := submit(t, s, reviewCommand(m, a, "unclassified-review", "accepted", at))[0]; !errors.Is(r.Err, ErrExecutorUnfenced) {
		t.Fatal("unclassified executor treated as stopped", r.Err)
	}
	r = mustResult(t, s, reviewCommand(m, a, "unclassified-note", "inconclusive", at))
	m = *r.Monitor
	r.Monitor.Actions[a.ID].Review.EvidenceRefs[0] = "changed"
	current, _ = s.Get(m.ID)
	if !current.Actions[a.ID].Held() || current.Actions[a.ID].Review.EvidenceRefs[0] == "changed" {
		t.Fatal("inconclusive review or copy ownership violated")
	}
}

func TestManualRecoveryReviewRejectionPreservesLongCooldown(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, g, h := localUnknown(t, s)
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	at := time.Now().UTC().Add(time.Second)
	m = *mustResult(t, s, reviewCommand(m, a, "cooldown-review", "rejected", at)).Monitor
	m.Policy.RecoveryCooldown = time.Hour
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: at}).Monitor
	if r := submit(t, s, manualCommand(m, g, "after-one-minute", at.Add(time.Minute)))[0]; !errors.Is(r.Err, ErrRecoveryIneligible) {
		t.Fatal("manual review bypassed ordinary cooldown", r.Err)
	}
	r := mustResult(t, s, manualCommand(m, g, "after-one-hour", at.Add(time.Hour)))
	m = *r.Monitor
	next := latestManual(t, m)
	if m.Actions[a.ID].State != Unknown || m.LastInterventionFailure != (time.Time{}) {
		t.Fatal("cooldown manufactured provider failure")
	}
	// A policy increase between queued admission and execution is respected too.
	m.Policy.RecoveryCooldown = 2 * time.Hour
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: at.Add(time.Hour)}).Monitor
	if r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: next.ID, Guard: g, At: at.Add(time.Hour)})[0]; r.Allowed {
		t.Fatal("queued manual action bypassed revised cooldown")
	}
}
