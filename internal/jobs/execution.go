package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

// ActionExecution belongs to one committed start. The owner supplies fresh
// callbacks for every invocation, including completion after an uncertain start.
type ActionExecution struct {
	Deadline           time.Time
	Finish             func() error
	RecordAgentReceipt func(context.Context, A2AReceipt) error
}

// Execution carries the owner context into a private, dispatched job copy.
// Direct Execute calls use a background parent; each transport still supplies
// its own finite operation timeout.
type Execution struct {
	parent                      context.Context
	failureCode, failureMessage string
	controlNotice               string
	notification                notificationIdentity
}

func (e *Execution) SetControlNotification(enabled bool, at time.Time) {
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	e.controlNotice = fmt.Sprintf("Monitor %s at %s. This is a configuration change; observed service health is unchanged.", state, at.UTC().Format(time.RFC3339))
}

func (e *Execution) SetFailureDiagnostic(code, message string) {
	e.failureCode, e.failureMessage = code, message
}

func (e *Execution) SetNotificationIdentity(identity notificationIdentity) {
	e.notification = identity
}

func (e *Execution) notificationMessage(message string) string {
	if e.controlNotice != "" {
		return e.controlNotice
	}
	if n := e.notification.Resolution; n != nil {
		message = "Earlier incident recovered at " + n.RecoveredAt.UTC().Format(time.RFC3339) + ". Current service health: " + n.Health + "."
		message += "\nMonitor: " + e.notification.MonitorID + "\nEarlier incident: " + e.notification.IncidentID + "\nEpisode: " + e.notification.EpisodeID
	}
	if n := e.notification.Review; n != nil {
		message = "Recovery outcome unknown — operator review required."
		if n.Phase == "close" {
			message = "Health recovered; earlier recovery outcome remains unknown."
			if n.Health != "healthy" {
				message = "The earlier outage recovered; its recovery action outcome remains unknown. Current service health: " + n.Health + "."
			}
			if n.Reason == "action_reviewed" {
				message = "Recovery action reviewed; review-needed alert closed. Observed service health: " + n.Health + "."
			}
		}
		message += "\nMonitor: " + e.notification.MonitorID + "\nIncident: " + e.notification.IncidentID + "\nRecovery action: " + n.ActionID + "\nReview alert: " + n.AlertID
	}
	if failure := e.notification.RecoveryFailure; failure != nil {
		message += fmt.Sprintf("\nRecovery attempt %d of %d failed. Recovery action: %s.", failure.Attempt, failure.MaxAttempts, failure.ActionID)
		switch failure.TerminationReason {
		case "execution_deadline":
			message += " The original recovery deadline has elapsed; automatic recovery is exhausted."
		case "permanent_rejection":
			message += " Recovery was permanently rejected; automatic recovery requires correction."
		case "attempt_limit":
			message += " The configured recovery attempt limit is exhausted."
		}
	}
	if e.failureCode != "" && e.failureMessage != "" {
		return message + "\nCause: " + e.failureCode + " — " + e.failureMessage
	}
	return message
}

func (e *Execution) resolvesNotification(color string) bool {
	return color == "green" || color == "blue" && e.notification.Review != nil && e.notification.Review.Phase == "close"
}

func (e *Execution) SetContext(ctx context.Context) { e.parent = ctx }
func (e *Execution) Context() context.Context {
	if e.parent != nil {
		return e.parent
	}
	return context.Background()
}

func operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

// Dispatch identifies one accepted operation, independently of the reusable
// monitor template. Its result is correlated even when a transport panics.
type Dispatch struct {
	ProviderKey                                     string
	ResolutionNotification                          *ResolutionNotification
	ReviewNotification                              *ReviewNotification
	ProviderCorrelationVersion                      int
	NotificationLog                                 *NotificationLog
	ControlChanged                                  bool
	ControlEnabled                                  bool
	ControlAt                                       time.Time
	FailureCode, FailureMessage                     string
	RecoveryFailure                                 *RecoveryFailure
	ProjectionVersion                               string
	CheckControlRevision                            string
	Scheduled                                       time.Time
	NotificationAt                                  time.Time
	MonitorID, Revision, ActionID, Driver           string
	IncidentID, CatalogUID                          string
	FileIncarnation                                 string
	FlowKey                                         uint64
	DeliveryID, EndpointID, NotificationCorrelation string
	Attempt                                         int
	Before                                          func(context.Context) error
	// Authorize returns an invocation-local completion callback, including on
	// uncertain admission. Dispatch copies never share mutable execution handles.
	Authorize func(context.Context) (ActionExecution, error)
	Deadline  time.Time
	parent    context.Context
	Job
	Ent         ecs.Entity
	Kind, Color string
	Generation  uint64
	Endpoint    int
	ID          uuid.UUID
}

// QueueIdentity supplies an internal grouping key, never a public target label.
func (d *Dispatch) QueueIdentity() (string, uint64) { return d.Driver, d.FlowKey }

// NotificationBudget is the overall controller-owned delivery budget. Each
// built-in HTTP call still has its shorter transport timeout. Agent jobs retain
// the operator's task budget instead of inheriting the ordinary HTTP limit.
func NotificationBudget(job Job) time.Duration {
	if agent := agentJob(job); agent != nil {
		return agent.taskTimeout
	}
	return 45 * time.Second
}

// NotificationConfigBudget is pure and does not construct a provider client.
// External workers select their immutable JobType budget in management.
func NotificationConfigBudget(config manifest.CodeNotification) time.Duration {
	if agent, ok := config.(*manifest.CodeNotificationA2A); ok {
		if agent.Timeout > 0 {
			return agent.Timeout
		}
		return time.Minute
	}
	return 45 * time.Second
}

func NewDispatch(job Job, ent ecs.Entity, kind, color string, generation uint64, endpoint int) *Dispatch {
	d := &Dispatch{ProviderCorrelationVersion: 1, Job: job.Copy(), Ent: ent, Kind: kind, Color: color, Generation: generation, Endpoint: endpoint, ID: uuid.New()}
	if agentJob(d.Job) != nil {
		d.Driver = "a2a"
	}
	return d
}
func (d *Dispatch) SetContext(ctx context.Context) { d.parent = ctx }
func (d *Dispatch) Execute() (r Result) {
	var started time.Time
	var finished func() error
	defer func() {
		if recover() != nil {
			r.Err = ErrJobPanic
		}
		ended := time.Now()
		if finished != nil {
			r.FinalizationErr = finalizeInvocation(finished)
		}
		r.Scheduled, r.ExecutionStart, r.ExecutionEnd = d.Scheduled, started, ended
		r.MonitorID, r.Revision, r.ActionID, r.Driver = d.MonitorID, d.Revision, d.ActionID, d.Driver
		r.ProjectionVersion = d.ProjectionVersion
		r.CheckControlRevision = d.CheckControlRevision
		r.Ent, r.Type, r.ID = d.Ent, d.Kind, d.ID
		r.Generation, r.Endpoint, r.Color = d.Generation, d.Endpoint, d.Color
	}()
	ctx := d.parent
	if ctx == nil {
		ctx = context.Background()
	}
	if !d.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.Deadline)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return Result{Err: err}
	}
	agent := agentJob(d.Job)
	isAgent := agent != nil
	if isAgent && (agent == nil || d.Kind != agent.kind || d.Driver != "a2a" || d.Authorize == nil || !agent.validAction(d.agentAction())) {
		return Result{Err: errA2AActionRequired}
	}
	if d.Before != nil {
		if err := d.Before(ctx); err != nil {
			return Result{Err: err}
		}
	}
	var execution ActionExecution
	if d.Authorize != nil {
		var err error
		execution, err = d.Authorize(ctx)
		finished = execution.Finish
		if err != nil {
			return Result{Err: err}
		}
		if !execution.Deadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, execution.Deadline)
			defer cancel()
			d.Deadline = execution.Deadline
		}
	}
	if isAgent && (execution.Finish == nil || execution.RecordAgentReceipt == nil) {
		return Result{Err: errA2AActionRequired}
	}
	started = time.Now()
	// This interval begins at the granted execution boundary. A rejected final
	// deadline check still has a correlated result proving no provider invocation.
	if err := ctx.Err(); err != nil {
		return Result{Err: noEffect(err, false)}
	}
	if isAgent {
		return agent.executeAction(ctx, d.agentAction(), execution.RecordAgentReceipt)
	}
	if j, ok := d.Job.(interface{ SetContext(context.Context) }); ok {
		j.SetContext(ctx)
	}
	bindNotificationContext(d.Job, d)
	bindFailureDiagnostic(d.Job, d)
	return d.Job.Execute()
}

// Entity binding wraps a template to annotate its result. Dispatch supplies
// that correlation itself and must still recognize the underlying action's
// committed-start requirement. Keep traversal bounded for malformed wrappers.
func agentJob(job Job) *A2AJob {
	for range 8 {
		switch wrapped := job.(type) {
		case *A2AJob:
			return wrapped
		case interface{ Unwrap() Job }:
			job = wrapped.Unwrap()
		default:
			return nil
		}
	}
	return nil
}

func finalizeInvocation(finish func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("execution completion marker panicked")
		}
	}()
	return finish()
}
func (d *Dispatch) Copy() Job {
	c := *d
	c.Job = d.Job.Copy()
	if d.ResolutionNotification != nil {
		v := *d.ResolutionNotification
		c.ResolutionNotification = &v
	}
	if d.RecoveryFailure != nil {
		failure := *d.RecoveryFailure
		c.RecoveryFailure = &failure
	}
	if d.ReviewNotification != nil {
		notice := *d.ReviewNotification
		c.ReviewNotification = &notice
	}
	return &c
}
