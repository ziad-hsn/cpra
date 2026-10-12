package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// ActionExecution belongs to one committed start. The owner supplies fresh
// callbacks for every invocation, including completion after an uncertain start.
type ActionExecution struct {
	Finish             func() error
	RecordAgentReceipt func(context.Context, A2AReceipt) error
}

// Execution carries the owner context into a private, dispatched job copy.
// Direct Execute calls use a background parent; each transport still supplies
// its own finite operation timeout.
type Execution struct{ parent context.Context }

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
	RecoveryFailure                       *RecoveryFailure
	ProjectionVersion                     string
	CheckControlRevision                  string
	Scheduled                             time.Time
	MonitorID, Revision, ActionID, Driver string
	IncidentID, CatalogUID                string
	Attempt                               int
	Before                                func(context.Context) error
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

// NotificationBudget is the overall controller-owned delivery budget. Each
// built-in HTTP call still has its shorter transport timeout. Agent jobs retain
// the operator's task budget instead of inheriting the ordinary HTTP limit.
func NotificationBudget(job Job) time.Duration {
	if agent := agentJob(job); agent != nil {
		return agent.taskTimeout
	}
	return 45 * time.Second
}

func NewDispatch(job Job, ent ecs.Entity, kind, color string, generation uint64, endpoint int) *Dispatch {
	d := &Dispatch{Job: job.Copy(), Ent: ent, Kind: kind, Color: color, Generation: generation, Endpoint: endpoint, ID: uuid.New()}
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
			r.Err = fmt.Errorf("%s job panicked", d.Kind)
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
	}
	if isAgent && (execution.Finish == nil || execution.RecordAgentReceipt == nil) {
		return Result{Err: errA2AActionRequired}
	}
	started = time.Now()
	if isAgent {
		return agent.executeAction(ctx, d.agentAction(), execution.RecordAgentReceipt)
	}
	if j, ok := d.Job.(interface{ SetContext(context.Context) }); ok {
		j.SetContext(ctx)
	}
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
	if d.RecoveryFailure != nil {
		failure := *d.RecoveryFailure
		c.RecoveryFailure = &failure
	}
	return &c
}
