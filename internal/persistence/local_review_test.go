package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoppedActionReviewPreservesEvidenceAndRestarts(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention = true
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at})
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, At: at, Generation: 1, Outcome: "failure"})[0].Monitor
	a := interventionAction(t, m)
	r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at, ExecutorSession: s.executorSession})[0]
	if !r.Allowed {
		t.Fatal("start not admitted", r.Err)
	}
	a = r.Monitor.Actions[a.ID]
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAdministrative(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	view, err := s.StoppedActions(ctx)
	defer view.Close()
	observed, ok, _ := view.Get(a.ID)
	if err != nil || !ok || observed.State != Started {
		t.Fatal("administrative read performed recovery", err)
	}
	body := &ActionReviewCommand{ActionID: a.ID, MonitorUID: observed.ReviewMonitorIdentity(), ExpectedRevision: observed.ReviewRevision,
		Revision: "local-review", OperationID: "local-review", Actor: "administrator", Reason: "Inspected provider audit", Resolution: "rejected"}
	c := Command{Kind: "local_action_review", MonitorID: m.ID, At: at.Add(time.Second), ActionReview: body}
	stale := *body
	stale.ExpectedRevision = "stale"
	c.ActionReview = &stale
	if _, err = s.ReviewStoppedAction(ctx, c); !errors.Is(err, ErrActionReviewConflict) {
		t.Fatal("stale observation accepted", err)
	}
	view.Close()
	view, err = s.StoppedActions(ctx)
	defer view.Close()
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _, _ := view.Get(a.ID)
	if unchanged.State != Started || unchanged.ExecutorFenced {
		t.Fatal("failed review changed action or fenced execution")
	}
	c.ActionReview = body
	r, err = s.ReviewStoppedAction(ctx, c)
	if err != nil || !r.Allowed {
		t.Fatal("stopped review failed", err)
	}
	a = r.Monitor.Actions[a.ID]
	if a.State != Unknown || a.Outcome != "interrupted_after_start" || a.Review == nil || a.Held() || len(r.Monitor.Actions) != len(m.Actions) {
		t.Fatal("review changed provider facts or admitted work", a)
	}
	if r.Operation.OldVersion != observed.ReviewRevision {
		t.Fatal("receipt does not retain the administrator's precondition")
	}
	if r.Operation.State != "completed" || len(s.fsm.image.Operations) != 0 {
		t.Fatal("stopped review retained pending owner work")
	}
	if err = s.Snapshot(); err == nil {
		t.Fatal("administrative owner created a snapshot")
	}
	view.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal("reviewed file action did not restore", err)
	}
	restored, _, err := s.ActionContext(ctx, a.ID)
	if err != nil || restored.Review == nil || restored.Review.Revision != body.Revision || restored.State != Unknown || restored.Held {
		t.Fatal("reviewed action changed on restart", err)
	}
	status, ok, statusErr := s.MonitorStatusContext(ctx, m.ID)
	if statusErr != nil || !ok || status.UnknownActions != 1 || status.ActionsNeedingReview != 0 {
		t.Fatal("review erased provider uncertainty or retained a released hold", status, statusErr)
	}
	if len(s.fsm.image.Operations) != 0 {
		t.Fatal("stopped review accumulated a pending receipt on restart")
	}
	if _, err = s.ReviewStoppedAction(ctx, c); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("live owner accepted local review", err)
	}
	// Snapshot only under normal ownership, after replaying the stopped review.
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err = s.ActionContext(ctx, a.ID)
	if err != nil || restored.Review == nil || restored.Review.Revision != body.Revision || restored.Held {
		t.Fatal("snapshot lost stopped review", err)
	}
}

func TestStoppedReviewDoesNotFenceRemoteExecution(t *testing.T) {
	for _, kind := range []string{"worker", "agent"} {
		t.Run(kind, func(t *testing.T) {
			s := openCatalogMemory(t)
			m, _, _, h := localUnknown(t, s)
			if err := h.Finish(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.Lock()
			defer s.fsm.mu.Unlock()
			m = s.fsm.image.Monitors[m.ID]
			a := latestManual(t, m)
			a.ExecutorKind = kind
			a.ExecutorFinishedAt = time.Time{}
			a.AgentReceipt = nil
			m.Actions[a.ID] = a
			s.fsm.image.Monitors[m.ID] = m
			c := reviewCommand(m, a, "remote-review", "accepted", time.Now().UTC().Add(time.Minute))
			c.Kind, c.ExecutorSession = "local_action_review", "stopped-owner"
			before := s.fsm.image.LocalExecutorSession
			r := s.fsm.applyLocalActionReview(c, s.fsm.image.Index+1, ActionFormatVersion)
			if !errors.Is(r.Err, ErrExecutorUnfenced) || s.fsm.image.LocalExecutorSession != before || s.fsm.image.Monitors[m.ID].Actions[a.ID].Review != nil {
				t.Fatal("exclusive filesystem ownership was treated as remote fencing", r.Err)
			}
		})
	}
}

func TestStoppedNotificationReviewPreservesDeliveryCounts(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	configure(t, s)
	at := time.Now().UTC()
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, Outcome: "failure", At: at})[0].Monitor
	var notice Action
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == "red" && a.Endpoint == 0 {
			notice = a
		}
	}
	if notice.ID == "" {
		t.Fatal("no notification")
	}
	m = *mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: notice.ID, At: at, ExecutorSession: s.executorSession}).Monitor
	notice = m.Actions[notice.ID]
	before := m.IncidentNotifications()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAdministrative(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := Command{Kind: "local_action_review", MonitorID: m.ID, At: at.Add(time.Second), ActionReview: &ActionReviewCommand{
		ActionID: notice.ID, MonitorUID: actionMonitorIdentity(m.ID, notice), ExpectedRevision: ActionReviewRevision(notice),
		Revision: "notice-review", OperationID: "notice-review", Actor: "administrator", Reason: "Inspected provider records", Resolution: "inconclusive"}}
	r, err := s.ReviewStoppedAction(t.Context(), c)
	if err != nil || !r.Allowed {
		t.Fatal("stopped review rejected", err)
	}
	after := r.Monitor.IncidentNotifications()
	if after.Pending != before.Pending-1 || after.Unknown != before.Unknown+1 {
		t.Fatal("interrupted delivery was not counted", before, after)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	incident, found, err := s.IncidentContext(t.Context(), m.IncidentID)
	if err != nil || !found || incident.Notifications != after {
		t.Fatal("snapshot changed notification counts", incident, err)
	}
}
