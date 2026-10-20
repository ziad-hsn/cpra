//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"time"
)

// WorkerLifecycleFormatVersion adds heartbeat observations and append-only evidence.
const WorkerLifecycleFormatVersion = 23
const workerLifecycleSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-23\n"

type WorkerHeartbeatRequest struct {
	ServerID    string `json:"server_id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
	GrantID     string `json:"grant_id"`
}

type WorkerHeartbeatResponse struct {
	WorkerHeartbeatRequest
	WorkerUID string `json:"worker_uid"`
	Accepted  bool   `json:"accepted"`
}

type WorkerHeartbeatState struct {
	At       time.Time `json:"at"`
	Accepted bool      `json:"accepted"`
}

type WorkerHeartbeatCommand struct {
	Maintenance *WorkerMaintenanceDecision `json:"maintenance,omitempty"`
	NodeID      string                     `json:"node_id"`
	OwnerEpoch  string                     `json:"owner_epoch"`
	Authority   WorkerAuthority            `json:"authority"`
	Request     WorkerHeartbeatRequest     `json:"request"`
}

func (c WorkerHeartbeatCommand) validate(at time.Time) error {
	if c.Maintenance != nil && c.Maintenance.validate(at) != nil {
		return ErrWorkerExecutionInvalid
	}
	for _, id := range []string{c.NodeID, c.OwnerEpoch, c.Request.ServerID, c.Request.SessionID, c.Request.ExecutionID, c.Request.GrantID} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if !c.Authority.sessionShapeValid() || c.Request.ServerID != workerProtocolServerID(c.NodeID, c.Authority.Epoch) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func (f *machine) workerHeartbeatRecord(c WorkerHeartbeatCommand, at time.Time) (WorkerExecutionRecord, error) {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return WorkerExecutionRecord{}, ErrWorkerSessionExpired
	}
	r, err := f.workerResultExecution(c.Authority, c.Request.ServerID, c.Request.ExecutionID, c.Request.GrantID, at)
	if err != nil {
		return WorkerExecutionRecord{}, err
	}
	if r.Lifecycle.SessionID != c.Request.SessionID || at.Before(r.Lifecycle.UpdatedAt) {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	return r, nil
}

func (f *machine) workerHeartbeatAccepted(a WorkerAuthority, r WorkerExecutionRecord, at time.Time) bool {
	l := r.Lifecycle
	if l.Phase != "started" || r.Result != nil || r.Finalization != nil || l.OwnerEpoch != f.image.LocalExecutorSession ||
		l.Heartbeat != nil && !l.Heartbeat.Accepted || f.checkWorkerExecutionPins(r.Intent, at) != nil ||
		f.checkWorkerAuthority(a, workerExecutionScope(r.Intent), at) != nil {
		return false
	}
	m := f.image.Monitors[r.Intent.MonitorID]
	if r.Intent.Category != "check" {
		var err error
		m, err = f.hydrateReviewDependencies(m, r.Intent.ActionID)
		if err != nil {
			return false
		}
	}
	if r.Intent.Category == "check" {
		return m.Generation < r.Intent.Generation
	}
	action, ok := m.Actions[r.Intent.ActionID]
	if !ok || action.State != Started || action.CatalogUID != r.Intent.MonitorUID || action.Revision != r.Intent.MonitorRevision || action.ExecutorKind != "worker" || action.ExecutorSession != l.GrantID {
		return false
	}
	// Acknowledgement does not interrupt work. Dismissal concerns only the
	// notification for that incident; recovery and checks continue.
	if m.LifecycleVersion == LifecycleFormatVersion && r.Intent.Category == "notification" {
		return m.ControlNotification(action) || !m.Ignored && !m.suppressedNotification(action) && m.notificationEligible(action)
	}
	return r.Intent.Category != "notification" || action.IncidentID == m.IncidentID && !m.Dismissed
}

func (f *machine) applyWorkerHeartbeat(c WorkerHeartbeatCommand, at time.Time, versions ...int) Result {
	version := WorkerLifecycleFormatVersion
	if len(versions) > 0 {
		version = versions[0]
	}
	old, err := f.workerHeartbeatRecord(c, at)
	if err != nil {
		return Result{Err: err}
	}
	response := WorkerHeartbeatResponse{WorkerHeartbeatRequest: c.Request, WorkerUID: c.Authority.WorkerUID,
		Accepted: f.workerHeartbeatAccepted(c.Authority, old, at)}
	if lifecycleStorageFormat(version) && !f.workerMaintenanceAllowed(c.Maintenance, old.Intent, at) {
		response.Accepted = false
	}
	// Completed outcomes keep their exact original timestamps and receipts.
	if old.Result == nil && old.Lifecycle.Phase == "started" {
		r := old.Clone()
		r.Lifecycle.Heartbeat = &WorkerHeartbeatState{At: at, Accepted: response.Accepted}
		r.Lifecycle.UpdatedAt = at
		oldCost, err := workerExecutionRecordCost(r.Intent.ID, old)
		if err != nil {
			return Result{Err: err}
		}
		cost, err := workerExecutionRecordCost(r.Intent.ID, r)
		if err != nil || cost != oldCost {
			return Result{Err: ErrWorkerExecutionQuota}
		}
		f.image.WorkerExecutions.Records[r.Intent.ID] = r
	}
	f.image.Version = joinStorageFormat(f.image.Version, version)
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerHeartbeat: &response}}
}

// CommitWorkerHeartbeat records cooperative continuation, never a new grant or
// deadline extension. The original polling session may have gone idle while a
// long-running handler occupies its slot; only its pinned identity is required.
func (s *Store) CommitWorkerHeartbeat(ctx context.Context, a WorkerAuthority, request WorkerHeartbeatRequest, evaluators ...WorkerMaintenanceEvaluator) (WorkerHeartbeatResponse, error) {
	if ctx == nil {
		return WorkerHeartbeatResponse{}, ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return WorkerHeartbeatResponse{}, err
	}
	if s.administrative {
		return WorkerHeartbeatResponse{}, ErrWorkerSessionUnavailable
	}
	c := WorkerHeartbeatCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request}
	at := time.Now().UTC()
	if err := c.validate(at); err != nil {
		return WorkerHeartbeatResponse{}, err
	}
	decision, err := s.prepareWorkerMaintenance(ctx, request.ExecutionID, "heartbeat", at, evaluators)
	if err != nil {
		return WorkerHeartbeatResponse{}, err
	}
	c.Maintenance = decision
	results, err := s.Submit(ctx, []Command{{Kind: "worker_heartbeat", At: at, commandExtensions: commandExtensions{WorkerHeartbeat: &c}}})
	if err != nil {
		return WorkerHeartbeatResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 || !results[0].Allowed || results[0].WorkerHeartbeat == nil {
		if len(results) == 1 && results[0].Err != nil {
			return WorkerHeartbeatResponse{}, results[0].Err
		}
		return WorkerHeartbeatResponse{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	out := *results[0].WorkerHeartbeat
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return WorkerHeartbeatResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	defer unlock()
	r, err := s.fsm.workerHeartbeatRecord(c, time.Now().UTC())
	if err != nil {
		return WorkerHeartbeatResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	monitor := s.fsm.image.Monitors[r.Intent.MonitorID]
	maintenanceBoundary := r.Intent.Category != "check" && !monitor.ControlNotification(monitor.Actions[r.Intent.ActionID]) && (c.Maintenance == nil || !c.Maintenance.Until.IsZero() && !time.Now().Before(c.Maintenance.Until))
	if out.Accepted && (!s.fsm.workerHeartbeatAccepted(a, r, time.Now().UTC()) || maintenanceBoundary) {
		if s.fsm.err != nil {
			return WorkerHeartbeatResponse{}, errControllerUnavailable
		}
		return WorkerHeartbeatResponse{}, ErrWorkerExecutionConflict
	}
	if err := ctx.Err(); err != nil {
		return WorkerHeartbeatResponse{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	return out, nil
}
