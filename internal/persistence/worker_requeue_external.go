//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"slices"
	"time"
)

const WorkerSchedulingFormatVersion = 26
const workerSchedulingSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-26\n"
const MaxWorkerRetiredOffers = 8
const MaxWorkerRequeuesPerTick = 128

// WorkerRequeueCommand names the precise unstarted lease or previous owner's
// queued record. It never changes the execution, payload, schedule or deadline.
type WorkerRequeueCommand struct {
	ExecutionID        string `json:"execution_id"`
	ExecutionRevision  string `json:"execution_revision"`
	ExpectedOwnerEpoch string `json:"expected_owner_epoch"`
	LeaseID            string `json:"lease_id,omitempty"`
	OwnerEpoch         string `json:"owner_epoch"`
}

func (c WorkerRequeueCommand) validate(at time.Time) error {
	for _, id := range []string{c.ExecutionID, c.ExecutionRevision, c.ExpectedOwnerEpoch, c.OwnerEpoch} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if c.LeaseID != "" && !catalogIdentifier(c.LeaseID, 256) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func workerRequeueDue(r WorkerExecutionRecord, owner string, at time.Time) bool {
	if _, terminal := r.TerminalReceipt(); terminal || at.Before(r.CreatedAt) || !at.Before(r.Intent.Deadline) {
		return false
	}
	if n := len(r.RetiredOffers); n > 0 && at.Before(r.RetiredOffers[n-1].UpdatedAt) {
		return false
	}
	l := r.Lifecycle
	if l == nil {
		return r.OwnerEpoch != owner
	}
	if l.GrantID != "" || !l.StartedAt.IsZero() || l.Heartbeat != nil || at.Before(l.UpdatedAt) || len(r.RetiredOffers) >= MaxWorkerRetiredOffers {
		return false
	}
	return l.Phase == "rejected" || l.Phase == "offered" && (r.OwnerEpoch != owner || !at.Before(l.OfferDeadline))
}

func workerRequeueCommand(r WorkerExecutionRecord, owner string) WorkerRequeueCommand {
	c := WorkerRequeueCommand{ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, ExpectedOwnerEpoch: r.OwnerEpoch, OwnerEpoch: owner}
	if r.Lifecycle != nil {
		c.LeaseID = r.Lifecycle.LeaseID
	}
	return c
}

func (f *machine) applyWorkerRequeue(c WorkerRequeueCommand, at time.Time) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	state := f.image.WorkerExecutions
	if state == nil {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	old, ok := state.Records[c.ExecutionID]
	if !ok || old.Intent.Revision != c.ExecutionRevision {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	// A replay acknowledges only the recorded retirement. It cannot clear a
	// later offer or grant, including one committed after a lost requeue reply.
	for _, retired := range old.RetiredOffers {
		if retired.LeaseID == c.LeaseID && retired.OwnerEpoch == c.ExpectedOwnerEpoch {
			return Result{Allowed: true}
		}
	}
	if old.OwnerEpoch != c.ExpectedOwnerEpoch || workerRequeueCommand(old, c.OwnerEpoch).LeaseID != c.LeaseID ||
		!workerRequeueDue(old, c.OwnerEpoch, at) {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if err := f.checkWorkerExecution(old.Intent, at); err != nil {
		return Result{Err: err}
	}
	r := old.Clone()
	if l := r.Lifecycle; l != nil {
		l.Phase, l.UpdatedAt = "rejected", at
		r.RetiredOffers = append(r.RetiredOffers, *l)
	}
	r.OwnerEpoch, r.Lifecycle = c.OwnerEpoch, nil
	oldCost, err := workerExecutionRecordCost(r.Intent.ID, old)
	if err != nil {
		return Result{Err: err}
	}
	cost, err := workerExecutionRecordCost(r.Intent.ID, r)
	if err != nil || cost > oldCost {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	state.replaceRecord(r)
	state.EncodedBytes += cost - oldCost
	f.image.Version = max(f.image.Version, WorkerSchedulingFormatVersion)
	return Result{Allowed: true}
}

func workerLease(r WorkerExecutionRecord, leaseID string) (*WorkerExecutionLifecycle, bool) {
	if r.Lifecycle != nil && r.Lifecycle.LeaseID == leaseID {
		return r.Lifecycle, false
	}
	for n := range r.RetiredOffers {
		if r.RetiredOffers[n].LeaseID == leaseID {
			return &r.RetiredOffers[n], true
		}
	}
	return nil, false
}

func validateWorkerRetiredOffers(i image, r WorkerExecutionRecord) error {
	if len(r.RetiredOffers) > MaxWorkerRetiredOffers || len(r.RetiredOffers) > 0 && i.Version < WorkerSchedulingFormatVersion {
		return ErrWorkerExecutionInvalid
	}
	previous := r.CreatedAt
	for n := range r.RetiredOffers {
		l := &r.RetiredOffers[n]
		if l.Phase != "rejected" || l.GrantID != "" || !l.StartedAt.IsZero() || l.Heartbeat != nil ||
			l.OfferedAt.Before(previous) || !l.UpdatedAt.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
		copy := r
		copy.OwnerEpoch, copy.Lifecycle, copy.Result, copy.Finalization = l.OwnerEpoch, l, nil, nil
		if err := validateWorkerExecutionLifecycle(i, copy); err != nil {
			return err
		}
		previous = l.UpdatedAt
	}
	if r.Lifecycle != nil && r.Lifecycle.OfferedAt.Before(previous) {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

// RequeueWorkerExecutions performs bounded maintenance under the current owner.
// Current controls, configuration and original action limits are checked again
// inside Apply. No handler runs and no new observation or action is created.
func (s *Store) RequeueWorkerExecutions(ctx context.Context, at time.Time, limit int) (int, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9998 || limit < 1 || limit > MaxWorkerRequeuesPerTick {
		return 0, ErrWorkerExecutionInvalid
	}
	if s.administrative {
		return 0, ErrWorkerSessionUnavailable
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return 0, err
	}
	state := s.fsm.image.WorkerExecutions
	var ids []string
	if state != nil {
		if len(state.Records) > MaxWorkerExecutions {
			unlock()
			return 0, ErrWorkerExecutionQuota
		}
		for id, r := range state.Records {
			if err := ctx.Err(); err != nil {
				unlock()
				return 0, err
			}
			if workerRequeueDue(r, s.executorSession, at) && s.fsm.checkWorkerExecution(r.Intent, at) == nil {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	commands := make([]Command, 0, min(len(ids), limit, s.config.Storage.BatchSize))
	for _, id := range ids[:min(len(ids), limit, s.config.Storage.BatchSize)] {
		c := workerRequeueCommand(state.Records[id], s.executorSession)
		commands = append(commands, Command{Kind: "worker_requeue", At: at, commandExtensions: commandExtensions{WorkerRequeue: &c}})
	}
	unlock()
	if len(commands) == 0 {
		return 0, ctx.Err()
	}
	results, err := s.Submit(ctx, commands)
	if err != nil {
		return 0, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != len(commands) {
		return 0, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	count := 0
	for _, result := range results {
		if result.Err != nil {
			return count, result.Err
		}
		if !result.Allowed {
			return count, ErrWorkerExecutionUnavailable
		}
		count++
	}
	return count, nil
}
