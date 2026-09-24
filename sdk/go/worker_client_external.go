//go:build externaljobs

package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ErrWorkerSessionExpired identifies an explicit expired-session response.
// A caller may establish a fresh session; it must preserve old execution records.
var ErrWorkerSessionExpired = errors.New("worker session expired")

const (
	workerPollBytes    = 4 << 20
	workerOutcomeBytes = 128 << 10
	workerReplyBytes   = 64 << 10
)

// WorkerClient uses a worker-scoped credential, separate from operator services.
// Servers must additionally enforce scoped worker permissions and runtime enablement.
type WorkerClient struct{ c *Client }

// NewWorkerClient constructs a protocol client with required authentication.
// Construction makes no request and grants no permission to execute work.
func NewWorkerClient(cfg Config) (*WorkerClient, error) {
	if cfg.AuthToken == "" && cfg.TokenSource == nil {
		return nil, errors.New("worker authentication source is required")
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &WorkerClient{c}, nil
}

// JobTypes manages immutable versioned descriptors; it never loads executable code.
func (c *Client) JobTypes() *JobTypesService { return &JobTypesService{c} }

// Poll requests bounded assignments for available capacity. An assignment is
// not permission to invoke a handler; obtain a granted Start disposition first.
func (c *WorkerClient) Poll(ctx context.Context, req api.PollRequest) (*api.Assignments, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if !workerIDs(req.WorkerID, req.ServerID, req.ClientSessionID) ||
		req.SessionID != "" && !workerIDs(req.SessionID) || req.PollSequence < 1 || req.SessionID == "" && req.PollSequence != 1 ||
		req.Capacity < 1 || req.Capacity > 100 || req.Limit < 1 || req.Limit > 100 || req.Limit > req.Capacity || req.WaitSeconds < 0 || req.WaitSeconds > 25 ||
		len(req.Capabilities) < 1 || len(req.Capabilities) > 64 {
		return nil, workerRequestError()
	}
	capabilities := make(map[api.WorkerCapability]bool, len(req.Capabilities))
	for _, capability := range req.Capabilities {
		if !workerIDs(capability.JobTypeID, capability.Version) || !workerKind(capability.Kind) || capabilities[capability] {
			return nil, workerRequestError()
		}
		capabilities[capability] = true
	}
	ctx = context.WithValue(ctx, timeoutKey{}, time.Duration(req.WaitSeconds+5)*time.Second)
	r, e := c.c.generated.WorkerPoll(ctx, req)
	v, e := responseBounded[api.Assignments](c.c, r, e, false, workerPollBytes)
	if e != nil {
		var problem *Error
		if errors.As(e, &problem) && problem.StatusCode == http.StatusConflict && problem.Problem.Code == "workerSessionExpired" {
			return nil, errors.Join(ErrWorkerSessionExpired, e)
		}
		return nil, e
	}
	a := v.Data
	if v.StatusCode != http.StatusOK || a.ServerID != req.ServerID || a.ClientSessionID != req.ClientSessionID || a.PollSequence != req.PollSequence ||
		!workerIDs(a.WorkerUID, a.SessionID) || req.SessionID != "" && a.SessionID != req.SessionID || !workerDeadline(a.SessionExpiresAt) ||
		int64(len(a.Items)) > req.Limit || int64(len(a.Items)) > req.Capacity {
		return nil, workerResponseError(false)
	}
	executions, leases := make(map[string]bool, len(a.Items)), make(map[string]bool, len(a.Items))
	for _, item := range a.Items {
		if item.ServerID != a.ServerID || item.WorkerUID != a.WorkerUID || item.SessionID != a.SessionID ||
			!workerIDs(item.ExecutionID, item.ExecutionRevision, item.LeaseID, item.MonitorID, item.IncarnationUID, item.JobTypeID, item.JobTypeUID, item.JobTypeVersion) ||
			!workerDeadline(item.Deadline) || !workerText(item.CredentialProfile, 256) || len(item.Parameters) > workerOutcomeBytes || !json.Valid(item.Parameters) ||
			!capabilities[api.WorkerCapability{JobTypeID: item.JobTypeID, Version: item.JobTypeVersion, Kind: item.Kind}] || executions[item.ExecutionID] || leases[item.LeaseID] {
			return nil, workerResponseError(false)
		}
		executions[item.ExecutionID], leases[item.LeaseID] = true, true
	}
	if err := workerCompletion(ctx, false); err != nil {
		return nil, err
	}
	return &v.Data, nil
}

// Start authorizes one execution only when Disposition is granted. Repeated
// terminal requests never confer permission to execute again.
func (c *WorkerClient) Start(ctx context.Context, req api.StartRequest) (*api.StartResponse, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if !workerIDs(req.ExecutionID, req.ExecutionRevision, req.LeaseID, req.ServerID, req.SessionID) || req.Mode != api.StartModeBegin && req.Mode != api.StartModeReconcile {
		return nil, workerRequestError()
	}
	r, e := c.c.generated.WorkerStart(ctx, req)
	v, e := responseBounded[api.StartResponse](c.c, r, e, true, workerReplyBytes)
	if e != nil {
		return nil, e
	}
	s := v.Data
	if v.StatusCode != http.StatusOK || s.ServerID != req.ServerID || s.SessionID != req.SessionID || s.ExecutionID != req.ExecutionID ||
		s.ExecutionRevision != req.ExecutionRevision || s.LeaseID != req.LeaseID || !workerIDs(s.WorkerUID) || !workerStartDisposition(req.Mode, s) {
		return nil, workerResponseError(true)
	}
	if err := workerCompletion(ctx, true); err != nil {
		return nil, err
	}
	return &v.Data, nil
}

// Heartbeat reports continued ownership of an execution. Callers must stop
// cooperative work when the server response revokes permission.
func (c *WorkerClient) Heartbeat(ctx context.Context, req api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if !workerIDs(req.WorkerID, req.ExecutionID, req.GrantID, req.ServerID, req.SessionID) {
		return nil, workerRequestError()
	}
	r, e := c.c.generated.WorkerHeartbeat(ctx, req)
	v, e := responseBounded[api.HeartbeatResponse](c.c, r, e, true, workerReplyBytes)
	if e != nil {
		return nil, e
	}
	h := v.Data
	if v.StatusCode != http.StatusOK || h.ServerID != req.ServerID || h.SessionID != req.SessionID || h.ExecutionID != req.ExecutionID || h.GrantID != req.GrantID || !workerIDs(h.WorkerUID) {
		return nil, workerResponseError(true)
	}
	if err := workerCompletion(ctx, true); err != nil {
		return nil, err
	}
	return &v.Data, nil
}

// Result reports the same server-owned outcome envelope until a durable receipt.
func (c *WorkerClient) Result(ctx context.Context, req api.Outcome) (*api.Receipt, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if !workerIDs(req.ServerID, req.WorkerUID, req.ExecutionID, req.GrantID) || !workerOutcome(req.Kind, req.Status) ||
		!workerText(req.RejectionCode, 128) || !workerEvidence(req.Diagnostic, req.Evidence) || len(req.Data) > workerOutcomeBytes || !workerEnvelope(req) {
		return nil, workerRequestError()
	}
	r, e := c.c.generated.WorkerResult(ctx, req)
	v, e := responseBounded[api.Receipt](c.c, r, e, true, workerReplyBytes)
	if e != nil {
		return nil, e
	}
	if v.StatusCode != http.StatusOK || !workerReceipt(v.Data, req.ServerID, req.WorkerUID, req.ExecutionID) {
		return nil, workerResponseError(true)
	}
	if err := workerCompletion(ctx, true); err != nil {
		return nil, err
	}
	return &v.Data, nil
}

// LateEvidence appends evidence without replacing an outcome or operator review.
func (c *WorkerClient) LateEvidence(ctx context.Context, req api.LateEvidenceRequest) (*api.Receipt, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if !workerIDs(req.ServerID, req.WorkerUID, req.ExecutionID, req.EvidenceID, req.OriginalReceiptID) ||
		!workerEvidence(req.Diagnostic, req.Evidence) || len(req.Data) > workerOutcomeBytes || !workerEnvelope(req) {
		return nil, workerRequestError()
	}
	r, e := c.c.generated.WorkerLateEvidence(ctx, req)
	v, e := responseBounded[api.Receipt](c.c, r, e, true, workerReplyBytes)
	if e != nil {
		return nil, e
	}
	if v.StatusCode != http.StatusOK || !workerReceipt(v.Data, req.ServerID, req.WorkerUID, req.ExecutionID) {
		return nil, workerResponseError(true)
	}
	if err := workerCompletion(ctx, true); err != nil {
		return nil, err
	}
	return &v.Data, nil
}

func workerCompletion(ctx context.Context, mutation bool) error {
	if err := ctx.Err(); err != nil {
		if mutation {
			return &AmbiguousError{Cause: err}
		}
		return err
	}
	return nil
}

func workerContext(ctx context.Context) error {
	if ctx == nil {
		return workerRequestError()
	}
	return ctx.Err()
}

func workerRequestError() error {
	return errors.Join(ErrInvalid, errors.New("invalid worker protocol request"))
}

func workerResponseError(mutation bool) error {
	err := errors.New("invalid worker protocol response")
	if mutation {
		return &AmbiguousError{Cause: err}
	}
	return err
}

func workerText(s string, maxBytes int) bool {
	return len(s) <= maxBytes && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func workerIDs(ids ...string) bool {
	for _, id := range ids {
		if id == "" || !workerText(id, 256) {
			return false
		}
	}
	return true
}

func workerDeadline(at time.Time) bool { return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 }

func workerKind(kind string) bool {
	return kind == "check" || kind == "recovery" || kind == "notification"
}

func workerStartDisposition(mode api.StartMode, s api.StartResponse) bool {
	switch s.Disposition {
	case api.StartDispositionGranted:
		return mode == api.StartModeBegin && workerIDs(s.GrantID) && workerDeadline(s.Deadline) && s.ReceiptID == ""
	case api.StartDispositionStarted:
		return workerIDs(s.GrantID) && workerDeadline(s.Deadline) && s.ReceiptID == ""
	case api.StartDispositionPending, api.StartDispositionRejected:
		return s.GrantID == "" && s.ReceiptID == "" && s.Deadline.IsZero()
	case api.StartDispositionUnknown:
		return workerIDs(s.GrantID) && workerDeadline(s.Deadline) && (s.ReceiptID == "" || workerIDs(s.ReceiptID))
	case api.StartDispositionTerminal:
		return s.GrantID == "" && s.Deadline.IsZero() && workerIDs(s.ReceiptID)
	default:
		return false
	}
}

func workerOutcome(kind, status string) bool {
	switch kind {
	case "check":
		return status == "success" || status == "failure" || status == "noData"
	case "recovery":
		return status == "accepted" || status == "completed" || status == "unknown" || status == "rejected"
	case "notification":
		return status == "accepted" || status == "delivered" || status == "unknown" || status == "rejected"
	default:
		return false
	}
}

func workerEvidence(diagnostic string, evidence []string) bool {
	if evidence == nil || len(diagnostic) > 64<<10 || !utf8.ValidString(diagnostic) || len(evidence) > 64 {
		return false
	}
	for _, value := range evidence {
		if !workerText(value, 4096) {
			return false
		}
	}
	return true
}

func workerEnvelope(value any) bool {
	encoded, err := json.Marshal(value)
	defer clear(encoded)
	return err == nil && len(encoded) <= workerOutcomeBytes
}

func workerReceipt(r api.Receipt, server, uid, execution string) bool {
	return r.ServerID == server && r.WorkerUID == uid && r.ExecutionID == execution && workerIDs(r.ReceiptID) &&
		(r.Disposition == api.ReceiptDispositionAccepted || r.Disposition == api.ReceiptDispositionFinalized || r.Disposition == api.ReceiptDispositionUnknown)
}

// CloseIdleConnections closes idle transport connections without cancelling
// an active handler, request, or server execution.
func (c *WorkerClient) CloseIdleConnections() { c.c.CloseIdleConnections() }
