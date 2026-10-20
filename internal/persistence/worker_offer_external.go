//go:build externaljobs

package persistence

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"time"
)

const WorkerOfferFormatVersion = 21
const workerOfferSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-21\n"

// WorkerQueueFormatVersion records oldest-committed-first assignment selection.
// Earlier poll commands retain their original ordering during log replay.
const WorkerQueueFormatVersion = 37
const workerQueueSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-37\n"
const WorkerOfferLifetime = 30 * time.Second
const workerOfferWireBudget = (4 << 20) - (64 << 10)
const workerOfferWireOverhead = 32 << 10

type WorkerOffer struct {
	ExecutionID       string    `json:"execution_id"`
	ExecutionRevision string    `json:"execution_revision"`
	LeaseID           string    `json:"lease_id"`
	Deadline          time.Time `json:"deadline"`
}

// WorkerExecutionLifecycle preserves an exact lease. Offers are not start grants.
// Requeue retains rejected lease identities; a new offer receives a fresh lease.
type WorkerExecutionLifecycle struct {
	Phase         string                `json:"phase"`
	ServerID      string                `json:"server_id"`
	WorkerID      string                `json:"worker_id"`
	WorkerUID     string                `json:"worker_uid"`
	SessionID     string                `json:"session_id"`
	LeaseID       string                `json:"lease_id"`
	OwnerEpoch    string                `json:"owner_epoch"`
	OfferedAt     time.Time             `json:"offered_at"`
	OfferDeadline time.Time             `json:"offer_deadline"`
	GrantID       string                `json:"grant_id,omitempty"`
	StartedAt     time.Time             `json:"started_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at"`
	Heartbeat     *WorkerHeartbeatState `json:"heartbeat,omitempty"`
}

func workerExecutionScope(in WorkerExecutionIntent) WorkerScope {
	return WorkerScope{JobTypeID: in.JobType.JobTypeID, JobTypeUID: in.JobType.JobTypeUID, Version: in.JobType.Version, Category: in.Category, ResourceKind: in.Source.Kind, ResourceID: in.Source.ID}
}
func (r WorkerPollResponse) clone() WorkerPollResponse { r.Offers = slices.Clone(r.Offers); return r }
func workerRecordOffer(r WorkerExecutionRecord) WorkerOffer {
	return WorkerOffer{ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, LeaseID: r.Lifecycle.LeaseID, Deadline: r.Intent.Deadline}
}

// workerAssignmentEligible does not reserve work. Both the waiting read and the
// committed assignment recheck these original execution and authority fences.
func (f *machine) workerAssignmentEligible(r WorkerExecutionRecord, authority WorkerAuthority, owner string, at time.Time) bool {
	return r.Lifecycle == nil && r.Finalization == nil &&
		(len(r.RetiredOffers) == 0 || !at.Before(r.RetiredOffers[len(r.RetiredOffers)-1].UpdatedAt)) &&
		r.OwnerEpoch == owner && f.checkWorkerExecution(r.Intent, at) == nil &&
		f.checkWorkerAuthority(authority, workerExecutionScope(r.Intent), at) == nil
}

type workerOfferCandidate struct {
	id    string
	index uint64
}

// workerOfferOrder holds only IDs and log positions from the bounded ready
// indexes. Committed order prevents fresh work from overtaking older eligible
// work across types; IDs break ties within a log entry. Version zero preserves
// the type/ID order of already committed poll commands.
func (f *machine) workerOfferOrder(c WorkerSessionCommand) []workerOfferCandidate {
	state := f.image.WorkerExecutions
	var candidates []workerOfferCandidate
	for _, cap := range c.Request.Capabilities {
		typ, ok := f.image.JobTypes.Records[cap.JobTypeID]
		if !ok || typ.Current.Record.Removed {
			continue
		}
		version, ok := typ.Versions[cap.Version]
		if !ok || version.Record.UID != typ.Current.Record.UID || version.Category != cap.Category {
			continue
		}
		ref := JobTypeReference{JobTypeID: cap.JobTypeID, JobTypeUID: version.Record.UID, Version: cap.Version, Revision: version.Record.Revision, Category: cap.Category}
		if tree := state.ready[jobTypeReferenceKey(ref)]; tree != nil {
			tree.Ascend(func(id string) bool {
				candidates = append(candidates, workerOfferCandidate{id: id, index: state.Records[id].CommittedIndex})
				return true
			})
		}
	}
	if c.SelectionVersion >= 1 {
		slices.SortFunc(candidates, func(a, b workerOfferCandidate) int {
			if order := cmp.Compare(a.index, b.index); order != 0 {
				return order
			}
			return cmp.Compare(a.id, b.id)
		})
	}
	return candidates
}

// prepareWorkerOffers computes bounded updates before either namespace changes.
func (f *machine) prepareWorkerOffers(c WorkerSessionCommand, response WorkerPollResponse, at time.Time) ([]WorkerExecutionRecord, []WorkerOffer, int64, error) {
	state := f.image.WorkerExecutions
	if len(c.ProposedLeaseIDs) == 0 || state == nil {
		return nil, nil, 0, nil
	}
	if err := state.ensureIndexes(context.Background()); err != nil {
		return nil, nil, 0, err
	}
	active := 0
	usedLeases := make(map[string]bool, len(state.Records))
	for _, r := range state.Records {
		for _, retired := range r.RetiredOffers {
			usedLeases[retired.LeaseID] = true
		}
		if r.Lifecycle != nil {
			usedLeases[r.Lifecycle.LeaseID] = true
			if r.Lifecycle.WorkerUID == c.Authority.WorkerUID && r.Lifecycle.Phase != "rejected" && r.Lifecycle.Phase != "finished" {
				active++
			}
		}
	}
	limit := min(c.Request.Limit, c.Request.Capacity, 100-active)
	if limit <= 0 {
		return nil, nil, 0, nil
	}
	var records []WorkerExecutionRecord
	var offers []WorkerOffer
	var delta int64
	wire := 0
	for _, candidate := range f.workerOfferOrder(c) {
		if len(records) >= limit {
			break
		}
		id := candidate.id
		old := state.Records[id]
		if c.SelectionVersion == 2 {
			var decision *WorkerMaintenanceDecision
			for i := range c.Maintenance {
				if c.Maintenance[i].ExecutionID == id {
					decision = &c.Maintenance[i]
					break
				}
			}
			if !f.workerMaintenanceAllowed(decision, old.Intent, at) {
				continue
			}
		}
		if !f.workerAssignmentEligible(old, c.Authority, c.OwnerEpoch, at) {
			continue
		}
		cost := len(old.Intent.Payload.Ciphertext) + workerOfferWireOverhead
		if cost > workerOfferWireBudget-wire {
			continue
		}
		lease := c.ProposedLeaseIDs[len(records)]
		if usedLeases[lease] {
			return nil, nil, 0, ErrWorkerExecutionConflict
		}
		usedLeases[lease] = true
		r := old.Clone()
		deadline := at.Add(WorkerOfferLifetime)
		if r.Intent.Deadline.Before(deadline) {
			deadline = r.Intent.Deadline
		}
		if c.SelectionVersion == 2 && r.Intent.Category != "check" && !f.image.Monitors[r.Intent.MonitorID].ControlNotification(f.image.Monitors[r.Intent.MonitorID].Actions[r.Intent.ActionID]) {
			for _, d := range c.Maintenance {
				if d.ExecutionID == id && !d.Until.IsZero() && d.Until.Before(deadline) {
					deadline = d.Until
					break
				}
			}
		}
		r.Lifecycle = &WorkerExecutionLifecycle{Phase: "offered", ServerID: response.ServerID, WorkerID: c.Authority.WorkerID, WorkerUID: c.Authority.WorkerUID, SessionID: response.SessionID, LeaseID: lease, OwnerEpoch: c.OwnerEpoch, OfferedAt: at, OfferDeadline: deadline, UpdatedAt: at}
		oldCost, err := workerExecutionRecordCost(id, old)
		if err != nil {
			return nil, nil, 0, err
		}
		newCost, err := workerExecutionRecordCost(id, r)
		if err != nil {
			return nil, nil, 0, err
		}
		delta += newCost - oldCost
		if delta > MaxWorkerExecutionBytes-state.EncodedBytes {
			return nil, nil, 0, ErrWorkerExecutionQuota
		}
		records = append(records, r)
		offers = append(offers, workerRecordOffer(r))
		wire += cost
	}
	return records, offers, delta, nil
}

func (f *machine) verifyWorkerPollResponse(a WorkerAuthority, response WorkerPollResponse, at time.Time) error {
	if err := f.workerPolicyReady(at); err != nil {
		return err
	}
	sessions := f.image.WorkerSessions
	if sessions == nil {
		return ErrWorkerSessionExpired
	}
	session, ok := sessions.Sessions[a.WorkerUID]
	if !ok || session.ReplyExpiredAt != nil || session.OwnerEpoch != f.image.LocalExecutorSession || !at.Before(session.Response.SessionExpiresAt) || !reflect.DeepEqual(session.Response, response) {
		return ErrWorkerSessionExpired
	}
	if err := f.checkWorkerPollAuthority(a, session.Request, at); err != nil {
		return err
	}
	for _, offer := range response.Offers {
		if f.image.WorkerExecutions == nil {
			return ErrWorkerExecutionUnavailable
		}
		r, ok := f.image.WorkerExecutions.Records[offer.ExecutionID]
		if !ok || r.Lifecycle == nil || r.Lifecycle.Phase != "offered" || r.OwnerEpoch != f.image.LocalExecutorSession || r.Lifecycle.WorkerUID != a.WorkerUID || r.Lifecycle.SessionID != response.SessionID || workerRecordOffer(r) != offer || !at.Before(r.Lifecycle.OfferDeadline) {
			return ErrWorkerExecutionExpired
		}
		if err := f.checkWorkerAuthority(a, workerExecutionScope(r.Intent), at); err != nil {
			return err
		}
		if err := f.checkWorkerExecution(r.Intent, at); err != nil {
			return err
		}
	}
	return nil
}

// VerifyWorkerPollResponse fences plaintext projection against current committed
// policy, session, source and lease state. Call it before and after decryption.
func (s *Store) VerifyWorkerPollResponse(ctx context.Context, a WorkerAuthority, response WorkerPollResponse) error {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return err
	}
	defer unlock()
	if err = s.fsm.verifyWorkerPollResponse(a, response, time.Now().UTC()); err != nil {
		return err
	}
	return ctx.Err()
}

func validateWorkerExecutionLifecycle(i image, r WorkerExecutionRecord) error {
	l := r.Lifecycle
	if l == nil {
		return nil
	}
	if i.Version < WorkerOfferFormatVersion || l.OwnerEpoch != r.OwnerEpoch || !catalogIdentifier(l.ServerID, 256) || !validAuthenticationID(l.WorkerID) || !validAuthenticationID(l.WorkerUID) || !catalogIdentifier(l.SessionID, 256) || !catalogIdentifier(l.LeaseID, 256) || l.OfferedAt.Before(r.CreatedAt) || !l.OfferDeadline.After(l.OfferedAt) || l.OfferDeadline.After(r.Intent.Deadline) || l.OfferDeadline.After(l.OfferedAt.Add(WorkerOfferLifetime)) || l.UpdatedAt.Before(l.OfferedAt) || l.UpdatedAt.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	if i.WorkerPolicy == nil {
		return ErrWorkerExecutionInvalid
	}
	worker, ok := i.WorkerPolicy.Workers[l.WorkerID]
	if !ok || worker.UID != l.WorkerUID {
		return ErrWorkerExecutionInvalid
	}
	if h := l.Heartbeat; h != nil {
		if i.Version < WorkerLifecycleFormatVersion || l.GrantID == "" || h.At.IsZero() || h.At.Before(l.StartedAt) || h.At.After(l.UpdatedAt) || h.Accepted && !h.At.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
	}
	switch l.Phase {
	case "offered", "rejected":
		if l.GrantID != "" || !l.StartedAt.IsZero() {
			return ErrWorkerExecutionInvalid
		}
	case "started", "unknown", "finished":
		if !catalogIdentifier(l.GrantID, 256) || l.StartedAt.Before(l.OfferedAt) || !l.StartedAt.Before(l.OfferDeadline) || l.UpdatedAt.Before(l.StartedAt) {
			return ErrWorkerExecutionInvalid
		}
		if l.Phase == "finished" && r.Result == nil && r.Finalization == nil {
			return ErrWorkerExecutionInvalid
		}
		if r.Intent.Category != "check" && r.Result == nil && r.Finalization == nil {
			m, ok := i.Monitors[r.Intent.MonitorID]
			if !ok {
				return ErrWorkerExecutionInvalid
			}
			a, ok := m.Actions[r.Intent.ActionID]
			if !ok || a.CatalogUID != r.Intent.MonitorUID || a.Revision != r.Intent.MonitorRevision || a.ExecutorKind != "worker" || a.ExecutorSession != l.GrantID || !a.StartedAt.Equal(l.StartedAt) || a.State != Started && a.State != Unknown {
				return ErrWorkerExecutionInvalid
			}
		}

	default:
		return ErrWorkerExecutionInvalid
	}
	return nil
}
func validExternalActionExecutor(i image, a Action) bool {
	if i.Version < WorkerOfferFormatVersion || a.ExecutorKind != "worker" || a.State == Queued || a.State == Cancelled || !a.ExecutorFinishedAt.IsZero() || i.WorkerExecutions == nil {
		return false
	}
	for _, r := range i.WorkerExecutions.Records {
		l := r.Lifecycle
		if l != nil && r.Intent.ActionID == a.ID && r.Intent.MonitorUID == a.CatalogUID && r.Intent.MonitorRevision == a.Revision && l.GrantID == a.ExecutorSession && l.StartedAt.Equal(a.StartedAt) {
			if r.Result == nil {
				return (l.Phase == "started" || l.Phase == "unknown") && (a.State == Started || a.State == Unknown)
			}
			return i.Version >= WorkerResultFormatVersion && ((l.Phase == "unknown" && a.State == Unknown) ||
				(l.Phase == "finished" && ((a.State == Succeeded && a.Outcome == r.Result.Input.Status) || (a.State == Failed && r.Result.Input.Status == "rejected"))))
		}
	}
	return false
}

// A terminal action keeps its executor identity after the bounded execution
// receipt has been retired. If a receipt still exists it must agree exactly.
func validArchivedWorkerAction(i image, a Action) bool {
	if !externalStorageFormat(i.Version) || a.ExecutorKind != "worker" ||
		(a.State != Succeeded && a.State != Failed) || !catalogIdentifier(a.ExecutorSession, 256) ||
		a.StartedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || !a.ExecutorFinishedAt.IsZero() {
		return false
	}
	if i.WorkerExecutions != nil {
		for _, r := range i.WorkerExecutions.Records {
			if r.Intent.ActionID == a.ID {
				return validExternalActionExecutor(i, a)
			}
		}
	}
	return (a.State == Failed && a.Outcome == "rejected") ||
		(a.State == Succeeded && (a.Outcome == "accepted" || a.Outcome == "completed"))
}
