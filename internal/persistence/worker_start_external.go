//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type WorkerStartRequest struct {
	ServerID          string `json:"server_id"`
	SessionID         string `json:"session_id"`
	ExecutionID       string `json:"execution_id"`
	ExecutionRevision string `json:"execution_revision"`
	LeaseID           string `json:"lease_id"`
	Mode              string `json:"mode"`
}
type WorkerStartResponse struct {
	ServerID          string    `json:"server_id"`
	SessionID         string    `json:"session_id"`
	ExecutionID       string    `json:"execution_id"`
	ExecutionRevision string    `json:"execution_revision"`
	LeaseID           string    `json:"lease_id"`
	WorkerUID         string    `json:"worker_uid"`
	Disposition       string    `json:"disposition"`
	GrantID           string    `json:"grant_id,omitempty"`
	ReceiptID         string    `json:"receipt_id,omitempty"`
	Deadline          time.Time `json:"deadline,omitempty"`
}
type WorkerStartCommand struct {
	NodeID          string             `json:"node_id"`
	OwnerEpoch      string             `json:"owner_epoch"`
	Authority       WorkerAuthority    `json:"authority"`
	Request         WorkerStartRequest `json:"request"`
	ProposedGrantID string             `json:"proposed_grant_id"`
}

func (r WorkerStartRequest) validate() error {
	for _, v := range []string{r.ServerID, r.SessionID, r.ExecutionID, r.ExecutionRevision, r.LeaseID} {
		if !catalogIdentifier(v, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if r.Mode != "begin" && r.Mode != "reconcile" {
		return ErrWorkerExecutionInvalid
	}
	return nil
}
func (c WorkerStartCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.NodeID, 256) || !catalogIdentifier(c.OwnerEpoch, 256) || !catalogIdentifier(c.ProposedGrantID, 256) || !c.Authority.sessionShapeValid() || c.Request.validate() != nil || at.IsZero() || at.Year() < 1 || at.Year() > 9998 || c.Request.ServerID != workerProtocolServerID(c.NodeID, c.Authority.Epoch) {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

// checkWorkerCredential fences previously assigned work by current credential
// identity only. Grant revisions restrict new starts, not original reconciliation.
func (f *machine) checkWorkerCredential(a WorkerAuthority, at time.Time) error {
	if err := f.workerPolicyReady(at); err != nil {
		return err
	}
	w, ok := f.image.WorkerPolicy.Workers[a.WorkerID]
	if !ok || !workerCredentialEligible(w, at) || a.Epoch != f.image.WorkerPolicy.Epoch || a.WorkerUID != w.UID || a.CredentialRevision != w.CredentialRevision {
		return ErrWorkerAuthorityDenied
	}
	return nil
}
func workerStartResponse(r WorkerExecutionRecord, disposition string) WorkerStartResponse {
	l := r.Lifecycle
	out := WorkerStartResponse{ServerID: l.ServerID, SessionID: l.SessionID, ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, LeaseID: l.LeaseID, WorkerUID: l.WorkerUID, Disposition: disposition}
	if disposition == "granted" || disposition == "started" || disposition == "unknown" {
		out.GrantID = l.GrantID
		out.Deadline = r.Intent.Deadline
	}
	return out
}
func (f *machine) workerStartRecord(c WorkerStartCommand, at time.Time) (WorkerExecutionRecord, error) {
	if err := f.checkWorkerCredential(c.Authority, at); err != nil {
		return WorkerExecutionRecord{}, err
	}
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return WorkerExecutionRecord{}, ErrWorkerSessionExpired
	}
	state := f.image.WorkerExecutions
	if state == nil {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	r, ok := state.Records[c.Request.ExecutionID]
	l := r.Lifecycle
	if !ok || l == nil || r.Intent.Revision != c.Request.ExecutionRevision || l.WorkerUID != c.Authority.WorkerUID || l.WorkerID != c.Authority.WorkerID || l.ServerID != c.Request.ServerID || l.SessionID != c.Request.SessionID || l.LeaseID != c.Request.LeaseID {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	if at.Before(l.UpdatedAt) {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	return r, nil
}
func (f *machine) workerOfferEligible(c WorkerStartCommand, r WorkerExecutionRecord, at time.Time) error {
	l := r.Lifecycle
	if l.OwnerEpoch != f.image.LocalExecutorSession || !at.Before(l.OfferDeadline) {
		return ErrWorkerExecutionExpired
	}
	sessions := f.image.WorkerSessions
	if sessions == nil {
		return ErrWorkerSessionExpired
	}
	session, ok := sessions.Sessions[l.WorkerUID]
	if !ok || session.OwnerEpoch != l.OwnerEpoch || session.Response.SessionID != l.SessionID || !at.Before(session.Response.SessionExpiresAt) {
		return ErrWorkerSessionExpired
	}
	if err := f.checkWorkerAuthority(c.Authority, workerExecutionScope(r.Intent), at); err != nil {
		return err
	}
	return f.checkWorkerExecution(r.Intent, at)
}
func (f *machine) applyWorkerStart(c WorkerStartCommand, at time.Time) Result {
	old, err := f.workerStartRecord(c, at)
	if err != nil {
		return Result{Err: err}
	}
	r := old.Clone()
	l := r.Lifecycle
	disposition := l.Phase
	var monitor *Monitor
	var events []Event
	switch l.Phase {
	case "offered":
		if err = f.workerOfferEligible(c, r, at); err != nil {
			l.Phase = "rejected"
			disposition = "rejected"
		} else if c.Request.Mode == "reconcile" {
			disposition = "pending"
		} else {
			// The grant is disclosed only by this first committed transition. A repeated
			// begin returns started, including after the first HTTP response was lost.
			for _, other := range f.image.WorkerExecutions.Records {
				if other.Lifecycle != nil && other.Lifecycle.GrantID == c.ProposedGrantID {
					return Result{Err: ErrWorkerExecutionConflict}
				}
			}
			l.Phase = "started"
			l.GrantID = c.ProposedGrantID
			l.StartedAt = at
			disposition = "granted"
			if r.Intent.Category != "check" {
				previous := f.image.Monitors[r.Intent.MonitorID]
				result := transition(previous, Command{Kind: "start", MonitorID: previous.ID, Revision: r.Intent.MonitorRevision, ActionID: r.Intent.ActionID, At: at})
				if !result.Allowed || result.Monitor == nil || result.Err != nil {
					return Result{Err: ErrWorkerExecutionConflict}
				}
				action := result.Monitor.Actions[r.Intent.ActionID]
				action.ExecutorKind = "worker"
				action.ExecutorSession = l.GrantID
				result.Monitor.Actions[action.ID] = action
				monitor = result.Monitor
				events = result.Events
			}
		}
	case "started":
		if l.OwnerEpoch != f.image.LocalExecutorSession || !at.Before(r.Intent.Deadline) {
			l.Phase = "unknown"
			disposition = "unknown"
		} else {
			disposition = "started"
		}
	case "unknown":
		disposition = "unknown"
	case "rejected":
		disposition = "rejected"
	default:
		return Result{Err: ErrWorkerExecutionInvalid}
	}
	if l.Phase != old.Lifecycle.Phase {
		l.UpdatedAt = at
		if l.Phase == "unknown" && r.Intent.Category != "check" {
			previous := f.image.Monitors[r.Intent.MonitorID]
			action, ok := previous.Actions[r.Intent.ActionID]
			if ok && action.State == Started && action.ExecutorKind == "worker" && action.ExecutorSession == l.GrantID {
				next := previous.Clone()
				action.State = Unknown
				action.Outcome = "worker_outcome_unknown"
				action.FinishedAt = at
				next.Actions[action.ID] = action
				monitor = &next
				events = append(events, Event{MonitorID: previous.ID, CatalogUID: action.CatalogUID, Revision: action.Revision, At: at, Type: "action_unknown", IncidentID: action.IncidentID, ActionID: action.ID, Kind: action.Kind, Color: action.Color, Endpoint: action.Endpoint, Outcome: action.Outcome})
			}
		}
		oldCost, err := workerExecutionRecordCost(r.Intent.ID, old)
		if err != nil {
			return Result{Err: err}
		}
		newCost, err := workerExecutionRecordCost(r.Intent.ID, r)
		if err != nil {
			return Result{Err: err}
		}
		state := f.image.WorkerExecutions
		if newCost-oldCost > MaxWorkerExecutionBytes-state.EncodedBytes {
			return Result{Err: ErrWorkerExecutionQuota}
		}
		state.Records[r.Intent.ID] = r
		state.EncodedBytes += newCost - oldCost
		if monitor != nil {
			events = append(events, f.installMonitor(f.image.Monitors[monitor.ID], *monitor, at)...)
		}
		f.image.Version = max(f.image.Version, WorkerOfferFormatVersion)
	}
	response := workerStartResponse(r, disposition)
	return Result{Allowed: true, Monitor: monitor, Events: events, resultExtensions: resultExtensions{WorkerStart: &response}}
}

func (s *Store) CommitWorkerStart(ctx context.Context, a WorkerAuthority, request WorkerStartRequest) (WorkerStartResponse, error) {
	if ctx == nil {
		return WorkerStartResponse{}, ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return WorkerStartResponse{}, err
	}
	if s.administrative {
		return WorkerStartResponse{}, ErrWorkerSessionUnavailable
	}
	c := WorkerStartCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request, ProposedGrantID: uuid.NewString()}
	at := time.Now().UTC()
	if err := c.validate(at); err != nil {
		return WorkerStartResponse{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_start", At: at, commandExtensions: commandExtensions{WorkerStart: &c}}})
	if err != nil {
		return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 || !results[0].Allowed || results[0].WorkerStart == nil {
		if len(results) == 1 && results[0].Err != nil {
			return WorkerStartResponse{}, results[0].Err
		}
		return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	out := *results[0].WorkerStart
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	defer unlock()
	if err = s.fsm.checkWorkerCredential(a, time.Now().UTC()); err != nil {
		return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if out.Disposition == "granted" {
		if !time.Now().Before(out.Deadline) || s.executorSession != s.fsm.image.LocalExecutorSession {
			return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionExpired)
		}
		r := s.fsm.image.WorkerExecutions.Records[request.ExecutionID]
		if r.Lifecycle == nil || r.Lifecycle.Phase != "started" || r.Lifecycle.GrantID != out.GrantID {
			return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionConflict)
		}
		if err = s.fsm.checkWorkerExecutionPins(r.Intent, time.Now().UTC()); err != nil {
			return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
		}
		if err = s.fsm.checkWorkerAuthority(a, workerExecutionScope(r.Intent), time.Now().UTC()); err != nil {
			return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
		}
	}
	if err = ctx.Err(); err != nil {
		return WorkerStartResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	return out, nil
}
