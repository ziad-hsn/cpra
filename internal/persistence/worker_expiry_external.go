//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"time"
)

const WorkerExpiryFormatVersion = 29
const workerExpirySnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-29\n"

// WorkerExpireCommand cancels an original queued action that never received an
// assignment. It carries configuration guards, not an execution or provider data.
type WorkerExpireCommand struct {
	MonitorID       string           `json:"monitor_id"`
	MonitorUID      string           `json:"monitor_uid"`
	MonitorRevision string           `json:"monitor_revision"`
	ControlRevision string           `json:"control_revision"`
	ActionID        string           `json:"action_id"`
	OwnerEpoch      string           `json:"owner_epoch"`
	Source          CatalogKey       `json:"source"`
	SourceUID       string           `json:"source_uid"`
	SourceRevision  string           `json:"source_revision"`
	JobType         JobTypeReference `json:"job_type"`
	Guard           CatalogGuard     `json:"guard"`
	Scheduled       time.Time        `json:"scheduled"`
	Deadline        time.Time        `json:"deadline"`
}

func workerExpiryCommand(in WorkerExecutionIntent, owner string) WorkerExpireCommand {
	return WorkerExpireCommand{MonitorID: in.MonitorID, MonitorUID: in.MonitorUID,
		MonitorRevision: in.MonitorRevision, ControlRevision: in.ControlRevision, ActionID: in.ActionID,
		OwnerEpoch: owner, Source: in.Source, SourceUID: in.SourceUID, SourceRevision: in.SourceRevision,
		JobType: in.JobType, Guard: in.Guard.Clone(), Scheduled: in.Scheduled, Deadline: in.Deadline}
}

func (c WorkerExpireCommand) intent() WorkerExecutionIntent {
	return WorkerExecutionIntent{MonitorID: c.MonitorID, MonitorUID: c.MonitorUID,
		MonitorRevision: c.MonitorRevision, ControlRevision: c.ControlRevision, ActionID: c.ActionID,
		Category: c.JobType.Category, Source: c.Source, SourceUID: c.SourceUID, SourceRevision: c.SourceRevision,
		JobType: c.JobType, Guard: c.Guard, Scheduled: c.Scheduled, Deadline: c.Deadline}
}

func (c WorkerExpireCommand) validate(at time.Time) error {
	for _, id := range []string{c.MonitorID, c.MonitorUID, c.MonitorRevision, c.ControlRevision,
		c.ActionID, c.OwnerEpoch, c.SourceUID, c.SourceRevision} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if c.JobType.Validate() != nil || (c.JobType.Category != "recovery" && c.JobType.Category != "notification") ||
		c.Guard.validate(c.MonitorID) != nil || c.Guard.Removed || c.Guard.monitorUID(c.MonitorID) != c.MonitorUID ||
		c.Source.validate() != nil || c.Scheduled.IsZero() || c.Scheduled.Year() < 1 || c.Scheduled.Year() > 9998 ||
		!c.Deadline.After(c.Scheduled) || c.Deadline.Sub(c.Scheduled) > 24*time.Hour || c.Deadline.Year() > 9999 ||
		at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	if c.Source.Kind != "NotificationEndpoint" || c.JobType.Category != "notification" {
		if c.Source != (CatalogKey{Kind: "Monitor", ID: c.MonitorID}) || c.SourceUID != c.MonitorUID {
			return ErrWorkerExecutionInvalid
		}
	}
	for _, condition := range c.Guard.Conditions {
		if condition.Key == c.Source && condition.UID == c.SourceUID && condition.Revision == c.SourceRevision {
			return nil
		}
	}
	return ErrWorkerExecutionInvalid
}

func (f *machine) applyWorkerExpiry(c WorkerExpireCommand, at time.Time) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession || at.Before(c.Deadline) {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	previous, ok := f.image.Monitors[c.MonitorID]
	a, exists := previous.Actions[c.ActionID]
	if !ok || !exists || a.CatalogUID != c.MonitorUID || a.Revision != c.MonitorRevision || !a.NotBefore.Equal(c.Scheduled) {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if a.State == Cancelled && a.Outcome == "worker_admission_expired" {
		// An exact action identity remains final even after later configuration or
		// controls change. Reconciliation does not create a second history event.
		return Result{Allowed: true}
	}
	in := c.intent()
	if err := f.checkWorkerExecutionAuthority(in, at); err != nil {
		return Result{Err: err}
	}
	if err := f.checkWorkerActionIdentity(in); err != nil {
		return Result{Err: err}
	}
	if a.State != Queued || !a.StartedAt.IsZero() || a.ExecutorKind != "" || a.ExecutorSession != "" {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if state := f.image.WorkerExecutions; state != nil {
		if err := state.ensureIndexes(context.Background()); err != nil {
			return Result{Err: err}
		}
		if state.actions[c.ActionID] != "" {
			// Admitted work belongs to the worker lifecycle, even before Start.
			return Result{Err: ErrWorkerExecutionConflict}
		}
	}
	a.State, a.Outcome, a.FinishedAt = Cancelled, "worker_admission_expired", at
	m := previous.Clone()
	m.Actions[a.ID] = a
	events := []Event{{MonitorID: m.ID, CatalogUID: a.CatalogUID, Revision: a.Revision, At: at,
		Type: "action_cancelled", IncidentID: a.IncidentID, ActionID: a.ID, Kind: a.Kind,
		Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome}}
	f.image.Version = max(f.image.Version, WorkerExpiryFormatVersion)
	events = append(events, f.installMonitor(previous, m, at)...)
	f.publishWorkerMonitor(previous, m)
	return Result{Allowed: true, Monitor: &m, Events: events}
}

// ExpireWorkerAction commits cancellation without allocating an execution slot.
// Callers retain the original identity across an unconfirmed reply; retrying that
// identity is conditional and cannot expire an admitted or started action.
func (s *Store) ExpireWorkerAction(ctx context.Context, in WorkerExecutionIntent) error {
	if ctx == nil || in.Category != in.JobType.Category || in.Generation != 0 {
		return ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c := workerExpiryCommand(in, s.executorSession)
	at := time.Now().UTC()
	if err := c.validate(at); err != nil {
		return err
	}
	if err := s.controllerHealthContext(ctx); err != nil {
		return err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_expire", At: at, commandExtensions: commandExtensions{WorkerExpire: &c}}})
	if err != nil {
		return errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 {
		return ErrWorkerExecutionUnavailable
	}
	if results[0].Err != nil {
		return results[0].Err
	}
	if !results[0].Allowed {
		return ErrWorkerExecutionConflict
	}
	return nil
}

func validateWorkerExpiryImage(i image) error {
	for _, m := range i.Monitors {
		for _, a := range m.Actions {
			if a.Outcome != "worker_admission_expired" {
				continue
			}
			if i.Version < WorkerExpiryFormatVersion || a.State != Cancelled || a.CatalogUID == "" ||
				(a.Kind != "intervention" && a.Kind != "code") || !a.StartedAt.IsZero() ||
				a.ExecutorKind != "" || a.ExecutorSession != "" || !a.ExecutorFinishedAt.IsZero() ||
				a.FinishedAt.IsZero() || a.FinishedAt.Before(a.NotBefore) || a.LateEvidence != nil || a.Review != nil || a.ConflictingEvidence != nil {
				return ErrWorkerExecutionInvalid
			}
		}
	}
	return nil
}
