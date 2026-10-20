package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func unknownRecoveryWithBlue(t *testing.T) (Monitor, Action, time.Time) {
	t.Helper()
	p := lifecyclePolicy()
	p.Intervention = true
	p.Endpoints = map[string]int{"blue": 1, "green": 1}
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, at)
	m = policyOutcome(t, m, a, at.Add(time.Millisecond), "failure", true)
	return m, a, at
}

func TestBlueHealthClosurePreservesUnknownAction(t *testing.T) {
	m, recovery, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	if opening.ReviewNotice == nil || opening.ReviewNotice.Phase != "open" || opening.ReviewNotice.ActionID != recovery.ID || opening.NotificationCorrelation == m.NotificationCorrelation {
		t.Fatal("review alert is not independent of the health notification")
	}
	m = policyStart(t, m, opening, at.Add(2*time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(3*time.Millisecond), "success", false)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	if queuedColorCount(m, "blue") != 0 {
		t.Fatal("blue closure preceded verified health")
	}
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	closure := notificationByColor(t, m, "blue", Queued)
	if closure.ReviewNotice.Phase != "close" || closure.ReviewNotice.Reason != "health_recovered" || closure.ReviewNotice.Health != "healthy" ||
		closure.EndpointID != opening.EndpointID || closure.NotificationCorrelation != opening.NotificationCorrelation || closure.DeliveryID == opening.DeliveryID || !m.Actions[recovery.ID].Held() {
		t.Fatal("blue closure lost its original identity or changed the recovery outcome")
	}
	if queuedColorCount(m, "green") != 0 {
		t.Fatal("blue alone incorrectly qualified for ordinary green")
	}
	m = policyStart(t, m, closure, at.Add(2*time.Second))
	m = policyOutcome(t, m, closure, at.Add(3*time.Second), "failure", true)
	m = policyPulse(t, m, at.Add(4*time.Second), "failure")
	next := notificationByColor(t, m, "blue", Queued)
	if next.ReviewNotice.Phase != "open" || next.NotificationCorrelation == opening.NotificationCorrelation || !m.Actions[closure.ID].Held() || !m.Actions[recovery.ID].Held() {
		t.Fatal("later outage reused a closed alert or repeated an unknown closure")
	}
	if err := validateNotificationIdentity(m, ActionFormatVersion); err != nil {
		t.Fatal(err)
	}
}

func TestBlueSnapshotRestartRetainsHoldAndClosure(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention, config.Policy.Endpoints = true, map[string]int{"blue": 1}
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, At: at, Outcome: "failure"})[0].Monitor
	recovery := interventionAction(t, m)
	r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at, ExecutorSession: s.executorSession})[0]
	if !r.Allowed {
		t.Fatal("recovery start", r.Err)
	}
	m = *submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at.Add(time.Millisecond), Outcome: "failure", Ambiguous: true})[0].Monitor
	m = reconcileReviewFacts(t, s, m.ID, at.Add(time.Millisecond))
	opening := notificationByColor(t, m, "blue", Queued)
	r = submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), At: at.Add(2 * time.Millisecond), ExecutorSession: s.executorSession})[0]
	if !r.Allowed {
		t.Fatal("opening start", r.Err)
	}
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, At: at.Add(3 * time.Millisecond), Outcome: "success"})
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal("opening snapshot restoration", err)
	}
	for n := 2; n <= 3; n++ {
		m = *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: uint64(n), At: at.Add(time.Duration(n) * time.Second), Outcome: "success"})[0].Monitor
	}
	m = reconcileReviewFacts(t, s, m.ID, at.Add(3*time.Second))
	closing := notificationByColor(t, m, "blue", Queued)
	if closing.NotificationCorrelation != opening.NotificationCorrelation || !retainedAction(t, s, recovery.ID).Held() {
		t.Fatal("restore changed the condition or effect hold")
	}
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal("closure snapshot restoration", err)
	}
	m, _ = s.Get(config.ID)
	if got := m.Actions[closing.ID]; got.State != Queued || !got.Deadline.Equal(closing.Deadline) || got.ReviewNotice == nil || got.ReviewNotice.Reason != "health_recovered" || !retainedAction(t, s, recovery.ID).Held() {
		t.Fatal("restart changed queued closure or released unknown action")
	}
}

func TestReviewMaintenanceAdmitsBlueWithoutControllerTicks(t *testing.T) {
	s, err := Open(t.Context(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention, config.Policy.Endpoints = true, map[string]int{"blue": 1}
	at := time.Now().UTC()
	mustResult(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, Generation: 1, At: at, Outcome: "failure"})[0].Monitor
	recovery := interventionAction(t, m)
	mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at, ExecutorSession: s.executorSession})
	results := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at.Add(time.Millisecond), Outcome: "failure", Ambiguous: true})
	if len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatalf("record unknown recovery outcome: %+v", results)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		// Only observe. The real Store maintenance loop must commit the pending
		// review assessment; this test does not invoke its reconciliation helper.
		current, ok := s.Get(m.ID)
		if ok && queuedColorCount(current, "blue") == 1 {
			if !current.RecoveryHeld() || !retainedAction(t, s, recovery.ID).Held() {
				t.Fatal("automatic review assessment released the unknown effect")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background review assessment did not admit the blue notification")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBlueSuppressionDoesNotInventProviderClosure(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "started"}[started], func(t *testing.T) {
			m, recovery, at := unknownRecoveryWithBlue(t)
			opening := notificationByColor(t, m, "blue", Queued)
			if started {
				m = policyStart(t, m, opening, at.Add(2*time.Millisecond))
				m = policyOutcome(t, m, opening, at.Add(3*time.Millisecond), "failure", true)
			}
			m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(time.Second), End: at.Add(10 * time.Second)}}
			m = policyPulse(t, m, at.Add(2*time.Second), "success")
			m = policyPulse(t, m, at.Add(3*time.Second), "success")
			if queuedColorCount(m, "blue") != 0 || !m.Actions[recovery.ID].Held() {
				t.Fatal("blue closure bypassed suppression or released the intervention")
			}
			alert := m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID]
			if alert.ClosedAt.IsZero() || alert.ClosingStarted || alert.ClosingDelivery != "" {
				t.Fatal("local health closure pretended provider delivery")
			}
			m = policyPulse(t, m, at.Add(11*time.Second), "success")
			if queuedColorCount(m, "blue") != map[bool]int{false: 0, true: 1}[started] {
				t.Fatal("closure did not respect original opening exposure")
			}
		})
	}
}

func TestBlueClosureCannotUseReplacementDestination(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	config := m.Clone()
	config.Revision = "replacement-target"
	m = *transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: at.Add(time.Second)}).Monitor
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	if queuedColorCount(m, "blue") != 0 || m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID].ClosureBlocked != "configuration_changed" {
		t.Fatal("closure was redirected to replacement configuration")
	}
}

func TestBlueUnknownBetweenHealthySamples(t *testing.T) {
	p := lifecyclePolicy()
	p.Intervention, p.Endpoints = true, map[string]int{"blue": 1}
	m := configuredPolicyMonitor(t, p)
	at := policyTime().Add(time.Second)
	m = policyPulse(t, m, at, "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, at)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m = policyOutcome(t, m, a, at.Add(2*time.Second), "failure", true)
	if queuedColorCount(m, "blue") != 1 || !m.Recovering {
		t.Fatal("one healthy sample hid an uncertain recovery")
	}
}

func TestBluePreparedMessageCannotStartAfterItsMeaningChanges(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	prepared := notificationByColor(t, m, "blue", Queued).Clone()
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: prepared.ID, At: at.Add(4 * time.Second), NoticeRevision: ActionNoticeRevision(prepared)})
	if r.Allowed {
		t.Fatal("stale prepared healthy message received a start")
	}
	current := m.Actions[prepared.ID]
	if !current.Deadline.Equal(prepared.Deadline) || current.DeliveryID != prepared.DeliveryID || current.ReviewNotice.Health != "unhealthy" {
		t.Fatal("meaning update changed original authority")
	}
	m = policyStart(t, m, current, at.Add(4*time.Second))
}

func TestBlueEvidenceSurvivesMonitorRecreation(t *testing.T) {
	m, recovery, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	oldIncarnation := m.FileIncarnation
	r := transition(m, Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second)})
	m = *r.Monitor
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	r = transition(m, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at.Add(2 * time.Second)})
	m = *r.Monitor
	if m.FileIncarnation == oldIncarnation || !m.Actions[recovery.ID].Held() {
		t.Fatal("recreation lost the original action")
	}
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	if queuedColorCount(m, "blue") != 0 {
		t.Fatal("recreated target received old blue work")
	}
	if err := validateNotificationIdentity(m, ActionFormatVersion); err != nil {
		t.Fatal(err)
	}
	s := openCatalogMemory(t)
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	// Preserve coverage of synchronous pre-cutover action-review replay.
	s.fsm.image.ActionProjectionState = nil
	a := m.Actions[recovery.ID]
	a.ExecutorKind, a.ExecutorSession = "local", "stopped-original-owner"
	m.Actions[a.ID] = a
	s.fsm.installMonitor(Monitor{}, m, at.Add(4*time.Second))
	command := reviewCommand(m, a, "review-original", "rejected", at.Add(5*time.Second))
	command.ActionReview.MonitorUID = actionMonitorIdentity(m.ID, a)
	reviewed := s.fsm.applyActionReview(command, 10, ActionFormatVersion)
	if reviewed.Err != nil || !reviewed.Allowed {
		t.Fatal("review of retained original action failed", reviewed.Err)
	}
	found := false
	for _, event := range reviewed.Events {
		if event.Type != "review_alert_closed" {
			continue
		}
		found = true
		if event.FileIncarnation != oldIncarnation || event.CatalogUID != a.CatalogUID || event.Revision != a.Revision || event.ReviewNotice.Health != "unknown" {
			t.Fatal("old action closure borrowed the replacement target identity or health")
		}
	}
	if !found || reviewed.Monitor.Actions[a.ID].Held() || queuedColorCount(*reviewed.Monitor, "blue") != 0 {
		t.Fatal("review did not close only the original local condition")
	}
}

func TestBlueReviewClosureRechecksContradictoryEvidence(t *testing.T) {
	m, recovery, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(2*time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(3*time.Millisecond), "success", false)
	a := m.Actions[recovery.ID]
	a.Review = &ActionReview{Revision: "review-1", Resolution: "rejected", Actor: "operator", At: at.Add(time.Second), Reason: "observed no effect"}
	m.Actions[a.ID] = a
	m.assessReviewAlerts(at.Add(time.Second), func(string, *Action) {})
	closure := notificationByColor(t, m, "blue", Queued)
	r := transition(m, Command{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, Outcome: "success", ExecutionStart: a.StartedAt, ExecutionEnd: at.Add(2 * time.Second), At: at.Add(3 * time.Second)})
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("late evidence was not retained", r.Err)
	}
	m = *r.Monitor
	r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: closure.ID, At: at.Add(4 * time.Second), NoticeRevision: ActionNoticeRevision(closure)})
	if r.Allowed || !r.Monitor.Actions[a.ID].Held() {
		t.Fatal("conflicting review still authorized its stale closure")
	}
	m = policyPulse(t, *r.Monitor, at.Add(5*time.Second), "failure")
	if queuedColorCount(m, "blue") != 0 || !m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID].ClosedAt.IsZero() {
		t.Fatal("conflicting review did not restore review-needed condition")
	}
}

func TestBlueDeferredClosureUsesCurrentHealthAndOriginalProviderScheme(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(time.Second), End: at.Add(10 * time.Second)}}
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	m.ProviderCorrelationVersion = 0
	m = policyPulse(t, m, at.Add(11*time.Second), "failure")
	alert := m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID]
	closure, ok := m.reviewDelivery(alert.ClosingDelivery)
	if !ok || closure.ReviewNotice.Health != "unhealthy" || closure.ProviderCorrelationVersion != opening.ProviderCorrelationVersion || closure.NotificationCorrelation != opening.NotificationCorrelation {
		t.Fatal("deferred closure reported stale health or changed provider identity")
	}
	if !m.reviewNoticeEligible(closure) {
		t.Fatal("truthful historical closure was not eligible")
	}
	if err := validateNotificationIdentity(m, ActionFormatVersion); err != nil {
		t.Fatal(err)
	}
}

func reconcileReviewFacts(t *testing.T, s *Store, id string, at time.Time) Monitor {
	t.Helper()
	for step := 0; step < 20000; step++ {
		s.fsm.mu.RLock()
		l := s.fsm.collections
		rows, err := l.reviewPage(reviewPrefix("fact", id), "", 1)
		s.fsm.mu.RUnlock()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			m, _ := s.Get(id)
			return m
		}
		r := rows[0]
		result := submit(t, s, Command{Kind: "action_reconcile", MonitorID: id, Revision: r.Key, Generation: r.Fact.Revision, At: at})[0]
		if result.Err != nil && !errors.Is(result.Err, errReviewProgress) {
			t.Fatal(result.Err)
		}
	}
	t.Fatal("review assessment did not drain")
	return Monitor{}
}
