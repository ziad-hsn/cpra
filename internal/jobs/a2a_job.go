package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

var errA2AActionRequired = errors.New("A2A execution requires a committed action and receipt writer")

// A2AConfig selects one operator-configured agent interface. Runtime credentials
// must be encrypted before retention; public observations contain receipts only.
type A2AConfig struct {
	URL, Token, Tenant string
	RequestTimeout     time.Duration
	TaskTimeout        time.Duration
	PollInterval       time.Duration
	AllowInsecureHTTP  bool
}

func (A2AConfig) String() string     { return "A2A connection (credentials omitted)" }
func (c A2AConfig) GoString() string { return c.String() }

// ObserveA2ATask reads an existing task through the original connection. It
// cannot send a message, follow a redirect, cancel a task or resolve an outcome.
func ObserveA2ATask(ctx context.Context, config A2AConfig, original A2AReceipt) (A2AReceipt, error) {
	if ctx == nil {
		return A2AReceipt{}, errors.New("A2A task observation requires a context")
	}
	client, err := newA2AClient(a2aConnection{URL: config.URL, Token: config.Token, Tenant: config.Tenant,
		Timeout: config.RequestTimeout, AllowInsecureHTTP: config.AllowInsecureHTTP})
	if err != nil {
		return A2AReceipt{}, err
	}
	return client.Task(ctx, original)
}

// A2AJob sends one intervention or notification. Its immutable transport may
// be shared by private job copies; execution authority never belongs to a copy.
type A2AJob struct {
	connection        A2AConfig
	client            *a2aClient
	kind              string
	taskTimeout       time.Duration
	pollInterval      time.Duration
	enqueued, started time.Time
}

func NewA2AJob(config A2AConfig, kind string) (*A2AJob, error) {
	if kind != "intervention" && kind != "code" {
		return nil, errors.New("A2A supports intervention and notification jobs")
	}
	if err := (&manifest.InterventionTargetA2A{URL: config.URL, Token: config.Token, Tenant: config.Tenant,
		RequestTimeout: config.RequestTimeout, TaskTimeout: config.TaskTimeout, PollInterval: config.PollInterval,
		AllowInsecureHTTP: config.AllowInsecureHTTP}).Validate(); err != nil {
		return nil, err
	}
	if config.TaskTimeout == 0 {
		config.TaskTimeout = time.Minute
	}
	if config.PollInterval == 0 {
		config.PollInterval = min(5*time.Second, config.TaskTimeout)
	}
	client, err := newA2AClient(a2aConnection{URL: config.URL, Token: config.Token, Tenant: config.Tenant,
		Timeout: config.RequestTimeout, AllowInsecureHTTP: config.AllowInsecureHTTP})
	if err != nil {
		return nil, err
	}
	return &A2AJob{client: client, connection: config, kind: kind, taskTimeout: config.TaskTimeout, pollInterval: config.PollInterval}, nil
}

// A2AConnection copies the immutable connection of the actual selected job.
// It performs no provider request. The owner encrypts it before a durable start.
func A2AConnection(job Job) (A2AConfig, bool) {
	if agent := agentJob(job); agent != nil {
		return agent.connection, true
	}
	return A2AConfig{}, false
}

// agentAction is an allowlist of committed incident context, not a manifest or
// an instruction assembled from provider error messages or agent output.
type agentAction struct {
	Notification    *notificationIdentity `json:"notification,omitempty"`
	Failure         *agentFailure         `json:"failure,omitempty"`
	RecoveryFailure *RecoveryFailure      `json:"recoveryFailure,omitempty"`
	SchemaVersion   string                `json:"schemaVersion"`
	MonitorID       string                `json:"monitorId"`
	MonitorUID      string                `json:"monitorUid,omitempty"`
	Revision        string                `json:"configurationRevision"`
	IncidentID      string                `json:"incidentId,omitempty"`
	ActionID        string                `json:"actionId"`
	Role            string                `json:"role"`
	Color           string                `json:"color,omitempty"`
	Attempt         int                   `json:"attempt"`
}

type agentFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d *Dispatch) agentAction() agentAction {
	role := "intervention"
	if d.Kind == "code" {
		role = "notification"
	}
	action := agentAction{SchemaVersion: "1", MonitorID: d.MonitorID, MonitorUID: d.CatalogUID,
		Revision: d.Revision, IncidentID: d.IncidentID, ActionID: d.ActionID, Role: role, Color: d.Color, Attempt: d.Attempt, RecoveryFailure: d.RecoveryFailure}
	if d.RecoveryFailure != nil {
		failure := *d.RecoveryFailure
		failure.FailedAt = failure.FailedAt.UTC()
		action.RecoveryFailure = &failure
	}
	if d.Kind == "code" && d.FailureCode != "" {
		action.Failure = &agentFailure{Code: d.FailureCode, Message: d.FailureMessage}
	}
	if d.Kind == "code" {
		action.Notification = &notificationIdentity{MonitorID: d.MonitorID, IncarnationID: d.CatalogUID, IncidentID: d.IncidentID, EpisodeID: d.NotificationCorrelation,
			ActionID: d.ActionID, DeliveryID: d.DeliveryID, EndpointID: d.EndpointID, At: d.NotificationAt.UTC(), Review: d.ReviewNotification, Resolution: d.ResolutionNotification}
		if action.Notification.IncarnationID == "" {
			action.Notification.IncarnationID = d.FileIncarnation
		}
	}
	return action
}

func (j *A2AJob) validAction(action agentAction) bool {
	if j == nil || j.client == nil || action.SchemaVersion != "1" || action.Attempt < 1 ||
		!validA2AIdentity(action.MonitorID) || !validA2AIdentity(action.Revision) || !validA2AIdentity(action.ActionID) {
		return false
	}
	for _, value := range []string{action.IncidentID, action.MonitorUID} {
		if value != "" && !validA2AIdentity(value) {
			return false
		}
	}
	if action.Color != "" && action.Color != "red" && action.Color != "yellow" && action.Color != "green" && action.Color != "cyan" && action.Color != "blue" && action.Color != "gray" {
		return false
	}
	if n := action.Notification; n != nil {
		if action.Role != "notification" || n.MonitorID != action.MonitorID || n.ActionID != action.ActionID || n.IncidentID != action.IncidentID {
			return false
		}
		for _, id := range []string{n.IncarnationID, n.DeliveryID, n.EndpointID, n.EpisodeID} {
			if id != "" && !validA2AIdentity(id) {
				return false
			}
		}
		if review := n.Review; review != nil {
			if action.Color != "blue" || !validA2AIdentity(review.AlertID) || !validA2AIdentity(review.ActionID) || review.ActionID == action.ActionID || review.AlertID != n.EpisodeID ||
				(review.Phase != "open" && review.Phase != "close") || (review.Health != "healthy" && review.Health != "unhealthy" && review.Health != "unknown") ||
				(review.Phase == "open" && review.Reason != "" || review.Phase == "close" && review.Reason != "health_recovered" && review.Reason != "action_reviewed") {
				return false
			}
		}
	}
	if action.Color == "blue" && (action.Notification == nil || action.Notification.Review == nil || action.Notification.DeliveryID == "" || action.Notification.EndpointID == "" || action.Notification.At.IsZero()) {
		return false
	}
	if failure := action.Failure; failure != nil {
		if action.Role != "notification" || !validA2AIdentity(failure.Code) || len(failure.Code) > 64 || failure.Message == "" || len(failure.Message) > 256 || strings.IndexFunc(failure.Message, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			return false
		}
	}
	if f := action.RecoveryFailure; f != nil {
		if action.Role != "notification" || action.Color != "red" || action.IncidentID == "" || !validA2AIdentity(f.ActionID) || f.ActionID == action.ActionID || !validA2AIdentity(f.Driver) || len(f.Driver) > 128 || f.Attempt < 1 || f.Attempt > math.MaxInt32 || f.MaxAttempts < 1 || f.MaxAttempts > math.MaxInt32 || f.FailedAt.IsZero() || f.Outcome != "failure" ||
			(f.Trigger != "recovery_failed" && f.Trigger != "recovery_exhausted") ||
			(f.Trigger == "recovery_exhausted" && f.Attempt < f.MaxAttempts && f.TerminationReason != "permanent_rejection" && f.TerminationReason != "execution_deadline") ||
			(f.TerminationReason != "" && f.TerminationReason != "attempt_limit" && f.TerminationReason != "execution_deadline" && f.TerminationReason != "permanent_rejection") {
			return false
		}
	}
	return (j.kind == "intervention" && action.Role == "intervention") || (j.kind == "code" && action.Role == "notification")
}

// Execute alone carries no committed identity or authority and performs no I/O.
func (*A2AJob) Execute() Result { return Result{Err: errA2AActionRequired} }

func (j *A2AJob) executeAction(parent context.Context, action agentAction, record func(context.Context, A2AReceipt) error) Result {
	ctx, cancel := context.WithTimeout(parent, j.taskTimeout)
	defer cancel()
	body, err := json.Marshal(action)
	if err != nil {
		return Result{Err: errA2AActionRequired}
	}
	instruction := "CPRa requests the configured intervention for this action. Use only the authority assigned to this agent."
	if j.kind == "code" {
		instruction = "CPRa supplies incident context for investigation. This notification grants no intervention or CPRa management authority."
	}
	receipt, err := j.client.Send(ctx, action.ActionID, []a2aPart{{Text: instruction}, {Data: body}})
	if err != nil {
		return Result{Err: err}
	}
	var recorded A2AReceipt
	for {
		if receipt != recorded {
			if err := record(ctx, receipt); err != nil {
				// A canceled receipt write leaves this action unknown. Completion
				// owns its separate marker; caller cancellation alone must not
				// make an otherwise usable store permanently unavailable.
				return Result{Err: errors.New("A2A receipt commit unconfirmed; remote outcome is unknown")}
			}
			recorded = receipt
		}
		if j.kind == "code" && (receipt.MessageID != "" || receipt.State == "TASK_STATE_SUBMITTED" || receipt.State == "TASK_STATE_WORKING" || receipt.State == "TASK_STATE_COMPLETED") {
			return Result{}
		}
		switch receipt.State {
		case "TASK_STATE_COMPLETED":
			return Result{}
		case "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING":
			// One worker owns a bounded operation. Polling only reads the pinned
			// task; neither a read failure nor expiry permits another send.
		default:
			return Result{Err: errors.New("A2A task completion is unconfirmed; operator review is required")}
		}
		timer := time.NewTimer(j.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{Err: a2aNetworkError(ctx.Err())}
		case <-timer.C:
		}
		receipt, err = j.client.Task(ctx, recorded)
		if err != nil {
			return Result{Err: err}
		}
	}
}

func (j *A2AJob) Copy() Job                   { c := *j; return &c }
func (j *A2AJob) GetEnqueueTime() time.Time   { return j.enqueued }
func (j *A2AJob) SetEnqueueTime(at time.Time) { j.enqueued = at }
func (j *A2AJob) GetStartTime() time.Time     { return j.started }
func (j *A2AJob) SetStartTime(at time.Time)   { j.started = at }
func (j *A2AJob) IsNil() bool                 { return j == nil }

// RecoveryFailure carries the original committed rejection into an agent notification.
// Targets, credentials and provider response text are never included.
type RecoveryFailure struct {
	TerminationReason string    `json:"termination_reason,omitempty"`
	ActionID          string    `json:"actionId"`
	Driver            string    `json:"driver"`
	Attempt           int       `json:"attempt"`
	MaxAttempts       int       `json:"maxAttempts"`
	Trigger           string    `json:"trigger"`
	Outcome           string    `json:"outcome"`
	FailedAt          time.Time `json:"failedAt"`
}
