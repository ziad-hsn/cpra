package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

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
	Deadline time.Time
	parent   context.Context
	Job
	Ent         ecs.Entity
	Kind, Color string
	Generation  uint64
	Endpoint    int
	ID          uuid.UUID
}

func NewDispatch(job Job, ent ecs.Entity, kind, color string, generation uint64, endpoint int) *Dispatch {
	return &Dispatch{Job: job.Copy(), Ent: ent, Kind: kind, Color: color, Generation: generation, Endpoint: endpoint, ID: uuid.New()}
}
func (d *Dispatch) SetContext(ctx context.Context) { d.parent = ctx }
func (d *Dispatch) Execute() (r Result) {
	defer func() {
		if recover() != nil {
			r.Err = fmt.Errorf("%s job panicked", d.Kind)
		}
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
	if j, ok := d.Job.(interface{ SetContext(context.Context) }); ok {
		j.SetContext(ctx)
	}
	return d.Job.Execute()
}
func (d *Dispatch) Copy() Job { c := *d; c.Job = d.Job.Copy(); return &c }
