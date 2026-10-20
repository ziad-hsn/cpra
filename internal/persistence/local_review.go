package persistence

import "context"

// StoppedActions returns a detached bounded-page view under exclusive
// administrative ownership. It does not enable controller admission.
func (s *Store) StoppedActions(ctx context.Context) (ActionView, error) {
	if s == nil || !s.administrative {
		return ActionView{}, ErrAuthenticationAdminRequired
	}
	unlock, err := s.lockStateContext(ctx, true)
	if err != nil {
		return ActionView{}, err
	}
	defer unlock()
	select {
	case <-s.stop:
		return ActionView{}, errControllerUnavailable
	default:
	}
	if s.opening || s.err != nil || s.fsm.err != nil {
		return ActionView{}, errControllerUnavailable
	}
	return s.actionSnapshotLocked(ctx)
}

// ReviewStoppedAction records an assertion under exclusive local store ownership.
// This fences local execution, not remote worker or agent execution, and never
// schedules, retries or invokes the reviewed operation.
func (s *Store) ReviewStoppedAction(ctx context.Context, c Command) (Result, error) {
	if !s.administrative || c.Kind != "local_action_review" || c.ActionReview == nil {
		return Result{}, ErrAuthenticationAdminRequired
	}
	c.ExecutorSession = s.executorSession
	results, err := s.Submit(ctx, []Command{c})
	if err != nil {
		return Result{}, err
	}
	if len(results) != 1 {
		return Result{}, ErrCommitUnconfirmed
	}
	return results[0], results[0].Err
}

func (f *machine) applyLocalActionReview(c Command, index uint64, version int) Result {
	previous, ok := f.image.Monitors[c.MonitorID]
	a, found := previous.Actions[c.ActionReview.ActionID]
	if !ok || !found || actionMonitorIdentity(c.MonitorID, a) != c.ActionReview.MonitorUID ||
		ActionReviewRevision(a) != c.ActionReview.ExpectedRevision || c.At.Before(a.StartedAt) || (a.State != Unknown && a.State != Started) {
		return Result{Err: ErrActionReviewConflict}
	}
	if a.State == Started && a.ExecutorKind != "local" && a.ExecutorKind != "agent" {
		return Result{Err: ErrExecutorUnfenced}
	}
	m := previous.Clone()
	m.upgradeLifecycle()
	var events []Event
	if a.State == Started {
		a.State, a.Outcome, a.FinishedAt = Unknown, "interrupted_after_start", c.At
		m.Actions[a.ID] = a
		events = append(events, Event{MonitorID: m.ID, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: a.Revision,
			At: c.At, Type: "action_unknown", ActionID: a.ID, IncidentID: a.IncidentID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome})
	}
	// The request compares the observation the administrator actually read.
	// Any required Started->Unknown conversion and assertion commit atomically.
	body := *c.ActionReview
	body.ExpectedRevision = ActionReviewRevision(a)
	c.ActionReview = &body
	oldSession := f.image.LocalExecutorSession
	f.image.LocalExecutorSession = c.ExecutorSession
	f.image.Monitors[m.ID] = m
	r := f.applyActionReview(c, index, version)
	if r.Err != nil || !r.Allowed {
		f.image.LocalExecutorSession = oldSession
		f.image.Monitors[m.ID] = previous
		return r
	}
	// Preserve the precondition supplied by the administrator, including when
	// the same transaction first marks an interrupted start unknown.
	if r.Operation != nil {
		r.Operation.OldVersion = ActionReviewRevision(previous.Actions[c.ActionReview.ActionID])
		// A stopped review has no asynchronous controller projection. Complete
		// its receipt in this transaction; retained history owns the audit record.
		r.Operation.State, r.Operation.Outcome = "completed", "applied"
		f.deleteOperation(r.Operation.ID)
		for n := range r.Events {
			if r.Events[n].Operation != nil && r.Events[n].Operation.ID == r.Operation.ID {
				r.Events[n] = receiptEvent(*r.Operation)
			}
		}
	}
	f.image.Version = joinStorageFormat(f.image.Version, LifecycleFormatVersion)
	r.Events = append(events, r.Events...)
	return r
}

// ReviewMonitorIdentity is the stable subject of an action review. It retains
// the original file incarnation after removal/re-add; catalog UIDs are unchanged.
func (a ActionRecord) ReviewMonitorIdentity() string {
	return actionMonitorIdentity(a.MonitorID, a.Action)
}
