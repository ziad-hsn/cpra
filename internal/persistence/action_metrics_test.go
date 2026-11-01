package persistence

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestActionMetricsObserveDurableLimitsWithoutReplayingHistory(t *testing.T) {
	for _, outcome := range []string{"attempt_limit", "execution_deadline", "permanent_rejection", "unknown", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			m := conditionMonitor(t)
			m.Policy.Endpoints = map[string]int{"red": 1}
			at := time.Now().UTC()
			submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: at, Config: &m})
			pulse := func(outcome string) {
				m, _ = s.Get(m.ID)
				at = at.Add(time.Second)
				submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Scheduled: at, Generation: m.Generation + 1, Outcome: outcome})
				m, _ = s.Get(m.ID)
			}
			pulse("failure")
			if outcome == "cancelled" {
				pulse("success")
				pulse("success")
			} else {
				attempts := 1
				if outcome == "attempt_limit" {
					attempts = 3
				}
				for attempt := 1; attempt <= attempts; attempt++ {
					a := notificationByColor(t, m, "red", Queued)
					at = a.NotBefore.Add(time.Millisecond)
					submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, At: at, ActionID: a.ID, NoticeRevision: ActionNoticeRevision(a)})
					m, _ = s.Get(m.ID)
					if outcome == "execution_deadline" {
						at = m.Actions[a.ID].Deadline.Add(time.Second)
					} else {
						at = at.Add(time.Millisecond)
					}
					result := Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, At: at, ActionID: a.ID, Outcome: "failure", Retryable: outcome == "attempt_limit" || outcome == "execution_deadline", Ambiguous: outcome == "unknown"}
					submit(t, s, result)
					before := s.ActionMetrics()
					submit(t, s, result)
					if s.ActionMetrics() != before {
						t.Fatal("duplicate result incremented terminal metrics")
					}
					if attempt < attempts && before[0].AttemptLimit != 0 {
						t.Fatal("retryable attempt counted as exhaustion")
					}
					m, _ = s.Get(m.ID)
				}
			}
			want := ActionMetrics{Pipeline: "code"}
			switch outcome {
			case "attempt_limit":
				want.AttemptLimit = 1
			case "execution_deadline":
				want.ExecutionDeadline = 1
			case "permanent_rejection":
				want.PermanentRejection = 1
			case "cancelled":
				want.Cancelled = 1
			}
			if got := s.ActionMetrics(); got[0] != want || got[1] != (ActionMetrics{Pipeline: "intervention"}) {
				t.Fatalf("terminal metrics: %+v; want %+v", got, want)
			}
			if outcome != "cancelled" {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := reopened.ActionMetrics(); got != ([2]ActionMetrics{{Pipeline: "code"}, {Pipeline: "intervention"}}) {
				t.Fatal("replay was counted as a new terminal transition", got)
			}
		})
	}
}

func TestActionMetricsCountDelayedRecoveryExhaustionOnce(t *testing.T) {
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy.Intervention, m.Policy.RecoveryMaxAttempts = true, 3
	m.Policy.RecoveryTimeout, m.Policy.RecoveryCooldown = time.Minute, 0
	m.Policy.Endpoints = nil
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
	a := interventionAction(t, m)
	m = *submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at})[0].Monitor
	a = m.Actions[a.ID]
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Millisecond), Outcome: "failure", Retryable: true})
	if s.ActionMetrics()[1].ExecutionDeadline != 0 {
		t.Fatal("retryable result was counted as exhausted")
	}
	for n := range 2 {
		result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: uint64(n + 2), At: a.Deadline.Add(time.Duration(n) * time.Second), Outcome: "failure"})[0]
		if result.Err != nil || s.ActionMetrics()[1] != (ActionMetrics{Pipeline: "intervention", ExecutionDeadline: 1}) {
			t.Fatal("delayed budget exhaustion lost or duplicated", result.Err, s.ActionMetrics())
		}
	}
}

func TestActionMetricsExcludeFailedHistoryCommit(t *testing.T) {
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy.Healthy, m.Policy.Endpoints = 1, map[string]int{"red": 1}
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Generation: 1, Outcome: "failure"})
	before := s.ActionMetrics()
	failed := errors.New("injected history write failure")
	s.fsm.history.mu.Lock()
	s.fsm.history.err = failed
	s.fsm.history.mu.Unlock()
	_, err := s.Submit(t.Context(), []Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second), Generation: 2, Outcome: "success"}})
	if !errors.Is(err, ErrCommitUnconfirmed) || !errors.Is(err, failed) || s.Status().Ready || s.ActionMetrics() != before {
		t.Fatal("unconfirmed terminal write counted as committed", err, s.ActionMetrics())
	}
}

func TestActionMetricsConcurrentReads(t *testing.T) {
	s := openCatalogMemory(t)
	counters := s.fsm.actionMetrics.Load()
	var tasks sync.WaitGroup
	for range 4 {
		tasks.Go(func() {
			for range 1000 {
				counters.record([]Event{{Type: "action_failed", Kind: "code", TerminationReason: "attempt_limit"}})
				_ = s.ActionMetrics()
			}
		})
	}
	tasks.Wait()
	if got := s.ActionMetrics()[0]; got != (ActionMetrics{Pipeline: "code", AttemptLimit: 4000}) {
		t.Fatal("concurrent counter update lost", got)
	}
}
