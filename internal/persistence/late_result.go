package persistence

import (
	"errors"
	"time"
)

var ErrLateEvidenceConflict = errors.New("late action evidence contradicts the retained outcome")

// LateActionEvidence preserves an observed result without resolving the held
// action. It contains no provider response body, error, target or credentials.
type LateActionEvidence struct {
	Outcome        string    `json:"outcome"`
	ExecutionStart time.Time `json:"execution_start"`
	ExecutionEnd   time.Time `json:"execution_end"`
	RecordedAt     time.Time `json:"recorded_at"`
}

func validateLateResult(c Command) error {
	if !catalogIdentifier(c.ActionID, 256) || (c.Outcome != "success" && c.Outcome != "failure") ||
		c.ExecutionStart.IsZero() || c.ExecutionEnd.Before(c.ExecutionStart) || c.At.Before(c.ExecutionEnd) {
		return errors.New("invalid late action evidence")
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.Revision, extra.ActionID, extra.Outcome = "", "", "", "", ""
	extra.At, extra.ExecutionStart, extra.ExecutionEnd = time.Time{}, time.Time{}, time.Time{}
	if extra != (Command{}) {
		return errors.New("late action evidence contains unrelated fields")
	}
	return nil
}

func (e LateActionEvidence) validate(a Action) error {
	if a.State != Unknown || (a.Kind != "intervention" && a.Kind != "code") || a.StartedAt.IsZero() || e.ExecutionStart.IsZero() ||
		e.ExecutionStart.Before(a.StartedAt) || e.ExecutionEnd.Before(e.ExecutionStart) || e.RecordedAt.Before(e.ExecutionEnd) ||
		(e.Outcome != "accepted" && e.Outcome != "rejected") {
		return errors.New("invalid retained late action evidence")
	}
	return nil
}

// The original action identity is authoritative even after removal or a new
// monitor incarnation. Recording evidence must not transfer that action to the
// current configuration, change incident ownership, or grant another attempt.
func applyLateResult(previous Monitor, c Command) Result {
	action, ok := previous.Actions[c.ActionID]
	if previous.ID != c.MonitorID || !ok || action.ID != c.ActionID || action.Revision != c.Revision ||
		action.State != Unknown || action.StartedAt.IsZero() {
		return Result{}
	}
	outcome := "accepted"
	if c.Outcome == "failure" {
		outcome = "rejected"
	}
	evidence := LateActionEvidence{Outcome: outcome, ExecutionStart: c.ExecutionStart, ExecutionEnd: c.ExecutionEnd, RecordedAt: c.At}
	if err := evidence.validate(action); err != nil {
		return Result{Err: err}
	}
	if action.LateEvidence != nil && action.LateEvidence.Outcome == outcome {
		return Result{Allowed: true}
	}
	if action.ConflictingEvidence != nil && action.ConflictingEvidence.Outcome == outcome {
		return Result{Allowed: true}
	}
	if action.LateEvidence != nil && action.Review == nil {
		return Result{Err: ErrLateEvidenceConflict}
	}
	m := previous.Clone()
	action = action.Clone()
	if action.LateEvidence == nil {
		action.LateEvidence = &evidence
	} else {
		action.ConflictingEvidence = &evidence
	}
	conflict := action.Review != nil && action.Review.Resolution != "inconclusive" && (action.Review.Resolution != outcome || action.ConflictingEvidence != nil)
	if conflict {
		action.Review.Conflict = true
	}
	m.Actions[action.ID] = action
	event := Event{MonitorID: previous.ID, CatalogUID: action.CatalogUID, Revision: action.Revision, At: c.At,
		Type: "action_late_evidence", IncidentID: action.IncidentID, ActionID: action.ID, Kind: action.Kind, Color: action.Color, Endpoint: action.Endpoint, Outcome: outcome}
	events := []Event{event}
	if conflict {
		e := event
		e.Type = "action_review_conflict"
		e.Actor = action.Review.Actor
		e.ControlRevision = action.Review.Revision
		events = append(events, e)
		// A later review must not rearm a request that was queued before the
		// contradictory evidence. Already-started invocations remain factual.
		for _, id := range sortedActions(m.Actions) {
			queued := m.Actions[id]
			if queued.CatalogUID != action.CatalogUID || queued.Kind != "intervention" || queued.State != Queued {
				continue
			}
			queued.State, queued.Outcome, queued.FinishedAt = Cancelled, "late_evidence_conflict", c.At
			m.Actions[id] = queued
			events = append(events, Event{MonitorID: m.ID, CatalogUID: queued.CatalogUID, Revision: queued.Revision, At: c.At, Type: "action_cancelled", ActionID: queued.ID, IncidentID: queued.IncidentID, Kind: queued.Kind, Outcome: queued.Outcome, Reason: "late_evidence_conflict"})
		}
	}
	return Result{Monitor: &m, Allowed: true, Events: events}
}
