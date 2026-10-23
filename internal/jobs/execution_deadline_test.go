package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

type committedDeadlineJob struct {
	Job
	ctx      context.Context
	observed *time.Time
}

func (j *committedDeadlineJob) Copy() Job                      { c := *j; return &c }
func (j *committedDeadlineJob) SetContext(ctx context.Context) { j.ctx = ctx }
func (j *committedDeadlineJob) Execute() Result {
	*j.observed, _ = j.ctx.Deadline()
	return Result{}
}

func TestDispatchUsesCommittedStartDeadline(t *testing.T) {
	var observed time.Time
	d := NewDispatch(&committedDeadlineJob{observed: &observed}, ecs.Entity{}, "code", "red", 1, 0)
	deadline := time.Now().Add(45 * time.Second)
	d.Authorize = func(ctx context.Context) (ActionExecution, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("pending condition acquired a pre-start budget")
		}
		return ActionExecution{Deadline: deadline, Finish: func() error { return nil }}, nil
	}
	if r := d.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if observed != deadline {
		t.Fatal("provider did not receive the committed deadline")
	}
}

func TestDispatchExpiredGrantNeverInvokesProvider(t *testing.T) {
	var observed time.Time
	d := NewDispatch(&committedDeadlineJob{observed: &observed}, ecs.Entity{}, "code", "red", 1, 0)
	finished := false
	d.Authorize = func(context.Context) (ActionExecution, error) {
		return ActionExecution{Deadline: time.Now().Add(-time.Second), Finish: func() error { finished = true; return nil }}, nil
	}
	r := d.Execute()
	if !errors.Is(r.Err, context.DeadlineExceeded) || !observed.IsZero() || !finished {
		t.Fatal("expired grant invoked provider or lost completion", r.Err, observed, finished)
	}
}
