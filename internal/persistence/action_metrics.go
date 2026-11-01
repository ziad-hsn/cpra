package persistence

import "sync/atomic"

// ActionMetrics counts terminal transitions committed since this Store opened.
// It excludes snapshot/log replay and is not a count of retained ledger records.
type ActionMetrics struct {
	Pipeline           string
	Cancelled          uint64
	AttemptLimit       uint64
	ExecutionDeadline  uint64
	PermanentRejection uint64
}

type actionCounters struct {
	cancelled, attemptLimit, executionDeadline, permanentRejection atomic.Uint64
}

type actionMetrics struct{ pipelines [2]actionCounters }

func (m *actionMetrics) record(events []Event) {
	if m == nil {
		return
	}
	for _, event := range events {
		if event.Type == "recovery_exhausted" && event.Kind == "intervention" && event.TerminationReason == "execution_deadline" {
			// A later observation can exhaust a previously retryable result.
			// That committed assessment is distinct from the original failure.
			m.pipelines[1].executionDeadline.Add(1)
			continue
		}
		if event.Type != "action_cancelled" && event.Type != "action_failed" {
			continue
		}
		var n int
		switch event.Kind {
		case "code":
		case "intervention":
			n = 1
		default:
			continue
		}
		c := &m.pipelines[n]
		if event.Type == "action_cancelled" {
			c.cancelled.Add(1)
		}
		// An uncertain effect is never classified as exhausted or canceled.
		// A retryable failed attempt without a terminal reason is not exhaustion.
		reason := event.TerminationReason
		if reason == "" && event.Kind == "code" && event.Outcome == "notification_deadline" {
			// Both built-in and worker expiry paths use this committed outcome.
			// Offer expiry and other "not started" outcomes are not exhaustion.
			reason = "execution_deadline"
		}
		switch reason {
		case "attempt_limit":
			c.attemptLimit.Add(1)
		case "execution_deadline":
			c.executionDeadline.Add(1)
		case "permanent_rejection":
			c.permanentRejection.Add(1)
		}
	}
}

// ActionMetrics reads bounded counters without acquiring the FSM or ledger lock.
// The only labels are the two built-in pipeline names and fixed reason names.
func (s *Store) ActionMetrics() [2]ActionMetrics {
	rows := [2]ActionMetrics{{Pipeline: "code"}, {Pipeline: "intervention"}}
	m := s.fsm.actionMetrics.Load()
	if m == nil {
		return rows
	}
	for n := range rows {
		c := &m.pipelines[n]
		rows[n].Cancelled = c.cancelled.Load()
		rows[n].AttemptLimit = c.attemptLimit.Load()
		rows[n].ExecutionDeadline = c.executionDeadline.Load()
		rows[n].PermanentRejection = c.permanentRejection.Load()
	}
	return rows
}
