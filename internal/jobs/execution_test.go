package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/mlange-42/ark/ecs"
)

type projectionFixture struct {
	Job
	called *int
	panics bool
	err    error
}

func TestDispatchFinalizesEachReservedInvocationWithoutChangingProviderFacts(t *testing.T) {
	for _, mode := range []string{"success", "provider panic", "rejected start", "failed finish", "panicking finish"} {
		t.Run(mode, func(t *testing.T) {
			called, finished := 0, 0
			d := NewDispatch(&projectionFixture{called: &called, panics: mode == "provider panic"}, ecs.Entity{}, "intervention", "", 1, 0)
			var finishAt time.Time
			d.Authorize = func(context.Context) (func() error, error) {
				finish := func() error {
					finished++
					finishAt = time.Now()
					if mode == "failed finish" {
						return errors.New("marker unavailable")
					}
					if mode == "panicking finish" {
						panic("private marker error")
					}
					return nil
				}
				if mode == "rejected start" {
					return finish, errors.New("start unconfirmed")
				}
				return finish, nil
			}
			r := d.Execute()
			if finished != 1 {
				t.Fatal("reserved invocation completion callback was lost")
			}
			if (mode == "rejected start") != (called == 0) {
				t.Fatal("start grant did not control provider invocation")
			}
			if (mode == "rejected start" || mode == "provider panic") != (r.Err != nil) {
				t.Fatal("finalization changed provider outcome", r.Err)
			}
			if (mode == "failed finish" || mode == "panicking finish") != (r.FinalizationErr != nil) {
				t.Fatal("completion evidence error lost")
			}
			if r.ExecutionEnd.After(finishAt) {
				t.Fatal("provider execution duration included finalization")
			}
		})
	}
}

type concurrentDispatchFixture struct{ Job }

func (j *concurrentDispatchFixture) Copy() Job       { return &concurrentDispatchFixture{} }
func (j *concurrentDispatchFixture) Execute() Result { return Result{} }

func TestDispatchCopiesKeepInvocationCompletionHandlesIndependent(t *testing.T) {
	d := NewDispatch(&concurrentDispatchFixture{}, ecs.Entity{}, "code", "red", 1, 0)
	var assigned atomic.Int64
	var finished sync.Map
	d.Authorize = func(context.Context) (func() error, error) {
		id := assigned.Add(1)
		return func() error {
			if _, duplicate := finished.LoadOrStore(id, true); duplicate {
				return errors.New("completion handle reused")
			}
			return nil
		}, nil
	}
	var group sync.WaitGroup
	for range 32 {
		copy := d.Copy()
		group.Go(func() {
			r := copy.Execute()
			if r.Err != nil || r.FinalizationErr != nil {
				t.Error("dispatch completion failed", r.Err, r.FinalizationErr)
			}
		})
	}
	group.Wait()
	count := 0
	finished.Range(func(_, _ any) bool { count++; return true })
	if count != 32 {
		t.Fatal("copied dispatches shared a mutable completion handle", count)
	}
}

func TestDispatchPreservesProviderOutcomeWhenCompletionLosesLeadership(t *testing.T) {
	for _, providerErr := range []error{nil, errors.New("provider rejected operation"), context.DeadlineExceeded} {
		name := "success"
		if providerErr != nil {
			name = providerErr.Error()
		}
		t.Run(name, func(t *testing.T) {
			called, finalized := 0, 0
			dispatch := NewDispatch(&projectionFixture{called: &called, err: providerErr}, ecs.Entity{}, "intervention", "", 1, 0)
			markerErr := errors.Join(errors.New("completion unconfirmed"), raft.ErrLeadershipLost)
			dispatch.Authorize = func(context.Context) (func() error, error) {
				return func() error { finalized++; return markerErr }, nil
			}
			result := dispatch.Execute()
			if result.Err != providerErr || result.FinalizationErr != markerErr || called != 1 || finalized != 1 {
				t.Fatal("completion failure replaced or retried provider outcome", result, called, finalized)
			}
			if result.ExecutionStart.IsZero() || result.ExecutionEnd.Before(result.ExecutionStart) {
				t.Fatal("completion failure lost execution timing", result)
			}
		})
	}
}

func (j *projectionFixture) Copy() Job { copy := *j; return &copy }
func (j *projectionFixture) Execute() Result {
	*j.called++
	if j.panics {
		panic("private provider failure")
	}
	return Result{ProjectionVersion: "driver-controlled", MonitorID: "wrong-monitor", Err: j.err}
}

func TestDispatchPreservesOwnerProjectionIdentityAcrossEveryOutcome(t *testing.T) {
	for _, state := range []string{"success", "panic", "guard rejection", "cancellation"} {
		t.Run(state, func(t *testing.T) {
			called := 0
			dispatch := NewDispatch(&projectionFixture{called: &called, panics: state == "panic"}, ecs.Entity{}, "pulse", "", 1, 0)
			dispatch.ProjectionVersion, dispatch.MonitorID = "frozen-preparation", "owner-monitor"
			if state == "guard rejection" {
				dispatch.Before = func(context.Context) error { return errors.New("configuration changed") }
			}
			if state == "cancellation" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				dispatch.SetContext(ctx)
			}
			copy := dispatch.Copy().(*Dispatch)
			result := copy.Execute()
			if result.ProjectionVersion != "frozen-preparation" || result.MonitorID != "owner-monitor" || result.ID != dispatch.ID {
				t.Fatal("provider or exceptional outcome replaced dispatch identity")
			}
			if (state == "success") != (result.Err == nil) {
				t.Fatal("dispatch outcome changed", result.Err)
			}
			if (state == "guard rejection" || state == "cancellation") && (called != 0 || !result.ExecutionStart.IsZero()) {
				t.Fatal("rejected dispatch invoked provider")
			}
		})
	}
}
