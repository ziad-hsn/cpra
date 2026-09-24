//go:build externaljobs

package persistence

import (
	"context"
	"reflect"
	"slices"
	"time"
)

const WorkerOfferFormatVersion = 21
const workerOfferSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-21\n"
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
// Rejected leases are retained and never reassigned by this protocol revision.
type WorkerExecutionLifecycle struct {
	Phase         string    `json:"phase"`
	ServerID      string    `json:"server_id"`
	WorkerID      string    `json:"worker_id"`
	WorkerUID     string    `json:"worker_uid"`
	SessionID     string    `json:"session_id"`
	LeaseID       string    `json:"lease_id"`
	OwnerEpoch    string    `json:"owner_epoch"`
	OfferedAt     time.Time `json:"offered_at"`
	OfferDeadline time.Time `json:"offer_deadline"`
	GrantID       string    `json:"grant_id,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func workerExecutionScope(in WorkerExecutionIntent) WorkerScope {
	return WorkerScope{JobTypeID: in.JobType.JobTypeID, JobTypeUID: in.JobType.JobTypeUID, Version: in.JobType.Version, Category: in.Category, ResourceKind: in.Source.Kind, ResourceID: in.Source.ID}
}
func (r WorkerPollResponse) clone() WorkerPollResponse { r.Offers = slices.Clone(r.Offers); return r }
func workerRecordOffer(r WorkerExecutionRecord) WorkerOffer {
	return WorkerOffer{ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, LeaseID: r.Lifecycle.LeaseID, Deadline: r.Intent.Deadline}
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
		if r.Lifecycle != nil {
			usedLeases[r.Lifecycle.LeaseID] = true
			if r.Lifecycle.WorkerUID == c.Authority.WorkerUID && r.Lifecycle.Phase != "rejected" {
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
		tree := state.ready[jobTypeReferenceKey(ref)]
		if tree == nil {
			continue
		}
		var failed error
		tree.Ascend(func(id string) bool {
			if len(records) >= limit {
				return false
			}
			old := state.Records[id]
			if old.Lifecycle != nil || old.OwnerEpoch != c.OwnerEpoch || f.checkWorkerExecution(old.Intent, at) != nil || f.checkWorkerAuthority(c.Authority, workerExecutionScope(old.Intent), at) != nil {
				return true
			}
			cost := len(old.Intent.Payload.Ciphertext) + workerOfferWireOverhead
			if cost > workerOfferWireBudget-wire {
				return true
			}
			lease := c.ProposedLeaseIDs[len(records)]
			if usedLeases[lease] {
				failed = ErrWorkerExecutionConflict
				return false
			}
			usedLeases[lease] = true
			r := old.Clone()
			deadline := at.Add(WorkerOfferLifetime)
			if r.Intent.Deadline.Before(deadline) {
				deadline = r.Intent.Deadline
			}
			r.Lifecycle = &WorkerExecutionLifecycle{Phase: "offered", ServerID: response.ServerID, WorkerID: c.Authority.WorkerID, WorkerUID: c.Authority.WorkerUID, SessionID: response.SessionID, LeaseID: lease, OwnerEpoch: c.OwnerEpoch, OfferedAt: at, OfferDeadline: deadline, UpdatedAt: at}
			oldCost, err := workerExecutionRecordCost(id, old)
			if err != nil {
				failed = err
				return false
			}
			newCost, err := workerExecutionRecordCost(id, r)
			if err != nil {
				failed = err
				return false
			}
			delta += newCost - oldCost
			if delta > MaxWorkerExecutionBytes-state.EncodedBytes {
				failed = ErrWorkerExecutionQuota
				return false
			}
			records = append(records, r)
			offers = append(offers, workerRecordOffer(r))
			wire += cost
			return true
		})
		if failed != nil {
			return nil, nil, 0, failed
		}
		if len(records) == limit {
			break
		}
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
	if !ok || session.OwnerEpoch != f.image.LocalExecutorSession || !at.Before(session.Response.SessionExpiresAt) || !reflect.DeepEqual(session.Response, response) {
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
	switch l.Phase {
	case "offered", "rejected":
		if l.GrantID != "" || !l.StartedAt.IsZero() {
			return ErrWorkerExecutionInvalid
		}
	case "started", "unknown":
		if !catalogIdentifier(l.GrantID, 256) || l.StartedAt.Before(l.OfferedAt) || !l.StartedAt.Before(l.OfferDeadline) || l.UpdatedAt.Before(l.StartedAt) {
			return ErrWorkerExecutionInvalid
		}
		if r.Intent.Category != "check" {
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
	if i.Version < WorkerOfferFormatVersion || a.ExecutorKind != "worker" || a.State != Started && a.State != Unknown || !a.ExecutorFinishedAt.IsZero() || i.WorkerExecutions == nil {
		return false
	}
	for _, r := range i.WorkerExecutions.Records {
		l := r.Lifecycle
		if l != nil && (l.Phase == "started" || l.Phase == "unknown") && r.Intent.ActionID == a.ID && r.Intent.MonitorUID == a.CatalogUID && r.Intent.MonitorRevision == a.Revision && l.GrantID == a.ExecutorSession && l.StartedAt.Equal(a.StartedAt) {
			return true
		}
	}
	return false
}
