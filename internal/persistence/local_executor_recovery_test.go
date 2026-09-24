package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

type completionRecoveryJob struct {
	jobs.Job
	execute func() jobs.Result
}

func (j *completionRecoveryJob) Copy() jobs.Job       { c := *j; return &c }
func (j *completionRecoveryJob) Execute() jobs.Result { return j.execute() }

func TestLocalExecutorLeadershipRecoveryDoesNotReinvokeProvider(t *testing.T) {
	s, err := Open(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m, guard := manualFixture(t, s)
	at := time.Now().UTC()
	m = *mustResult(t, s, manualCommand(m, guard, "completion-recovery", at)).Monitor
	a := latestManual(t, m)
	called := 0
	dispatch := jobs.NewDispatch(&completionRecoveryJob{execute: func() jobs.Result {
		called++
		controllerFollower(t, s)
		return jobs.Result{}
	}}, ecs.Entity{}, "intervention", "", 1, 0)
	var handle *LocalExecution
	dispatch.Authorize = func(ctx context.Context) (func() error, error) {
		var err error
		handle, err = s.BeginLocalAction(ctx, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: guard, At: at})
		if handle == nil {
			return nil, err
		}
		return func() error { return handle.Finish(context.Background()) }, err
	}
	result := dispatch.Execute()
	if called != 1 || result.Err != nil || !IsLeadershipUnavailable(result.FinalizationErr) || result.ExecutionStart.IsZero() {
		t.Fatal("completion failure changed provider fact", called, result)
	}
	returnedAt := *handle.returnedAt.Load()
	status, err := s.RetryReturnedExecutions(context.Background())
	if !IsLeadershipUnavailable(err) || status != (LocalExecutorStatus{Returned: 1}) {
		t.Fatal("follower lost returned claim", status, err)
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("unconfirmed completion released storage", err)
	}
	waitBudgetCondition(t, "executor leadership recovery", func() bool { return s.ControllerHealth() == nil })
	status, err = s.RetryReturnedExecutions(context.Background())
	if err != nil || status != (LocalExecutorStatus{}) || called != 1 || result.Err != nil {
		t.Fatal("marker retry re-executed provider or lost success", status, err, called)
	}
	m, _ = s.Get(m.ID)
	got := m.Actions[a.ID]
	if got.State != Started || !got.ExecutorFinishedAt.Equal(returnedAt) {
		t.Fatal("marker invented provider evidence or changed original return time", got)
	}
	// The owner still commits the retained provider observation separately.
	committed := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: result.ExecutionEnd, Outcome: "success"})[0]
	if committed.Err != nil || committed.Monitor == nil {
		t.Fatal("retained provider result rejected", committed.Err)
	}
	m = *committed.Monitor
	if m.Actions[a.ID].State != Succeeded || called != 1 {
		t.Fatal("retained provider result did not commit once", m.Actions[a.ID])
	}
}

func TestLocalExecutorCompletionLostReplyRetainsClaimAndExactMarker(t *testing.T) {
	s, run := dormantCatalogStore(t)
	at := time.Now().UTC()
	m := testMonitor()
	m = *transition(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}).Monitor
	m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at}).Monitor
	a := m.Actions[sortedActions(m.Actions)[0]]
	s.executorSession = "completion-lost-reply"
	s.fsm.image.LocalExecutorSession = s.executorSession
	s.fsm.image.Version = CatalogFormatVersion
	s.fsm.image.Monitors[m.ID] = m
	startCtx, cancelStart := context.WithCancel(context.Background())
	type startResult struct {
		handle *LocalExecution
		err    error
	}
	started := make(chan startResult, 1)
	go func() {
		h, err := s.BeginLocalAction(startCtx, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at})
		started <- startResult{h, err}
	}()
	waitBudgetCondition(t, "queued start", func() bool { return len(s.requests) == 1 })
	cancelStart()
	start := <-started
	if start.handle == nil || !errors.Is(start.err, ErrCommitUnconfirmed) {
		t.Fatal("uncertain start lost handle", start.err)
	}
	finishCtx, cancelFinish := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- start.handle.Finish(finishCtx) }()
	waitBudgetCondition(t, "queued completion", func() bool { return len(s.requests) == 2 })
	cancelFinish()
	if err := <-finished; !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("completion did not cross uncertainty boundary", err)
	}
	returnedAt := *start.handle.returnedAt.Load()
	if status := s.localExecutorStatus(); status != (LocalExecutorStatus{Returned: 1}) {
		t.Fatal("lost completion reply lost ownership", status)
	}
	run()
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(m.ID)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if err := start.handle.Finish(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	after, _ := s.Get(m.ID)
	if status := s.localExecutorStatus(); status != (LocalExecutorStatus{}) || !after.Actions[a.ID].ExecutorFinishedAt.Equal(returnedAt) || !reflect.DeepEqual(after.Actions[a.ID], before.Actions[a.ID]) {
		t.Fatal("concurrent exact completion retries changed committed evidence", status, after.Actions[a.ID])
	}
}

func TestLocalExecutorRetryHonorsCallerDeadline(t *testing.T) {
	for _, blocked := range []string{"completion serialization", "store health", "fsm health"} {
		t.Run(blocked, func(t *testing.T) {
			s := openCatalogMemory(t)
			_, _, _, handle := localUnknown(t, s)
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if err := handle.Finish(canceled); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			var release func()
			switch blocked {
			case "completion serialization":
				handle.serial <- struct{}{}
				release = func() { <-handle.serial }
			case "store health":
				s.mu.Lock()
				release = s.mu.Unlock
			case "fsm health":
				s.fsm.mu.Lock()
				release = s.fsm.mu.Unlock
			}
			ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer stop()
			type retryResult struct {
				status LocalExecutorStatus
				err    error
			}
			completed := make(chan retryResult, 1)
			go func() { status, err := s.RetryReturnedExecutions(ctx); completed <- retryResult{status, err} }()
			select {
			case result := <-completed:
				release()
				if !errors.Is(result.err, context.DeadlineExceeded) || result.status != (LocalExecutorStatus{Returned: 1}) {
					t.Fatal("retry ignored deadline or lost returned claim", result)
				}
			case <-time.After(time.Second):
				release()
				<-completed
				t.Fatal("retry blocked beyond caller deadline")
			}
			status, err := s.RetryReturnedExecutions(context.Background())
			if err != nil || status != (LocalExecutorStatus{}) {
				t.Fatal("timed-out completion could not recover", status, err)
			}
		})
	}
}

func TestLocalExecutorRetryBatchSeparatesReturnedAndRunningClaims(t *testing.T) {
	s := openCatalogMemory(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var running *LocalExecution
	for i := range 130 {
		h, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: fmt.Sprintf("missing-%d", i), Revision: "r1", ActionID: fmt.Sprintf("action-%d", i), At: time.Now().UTC()})
		if h == nil || !errors.Is(err, ErrRecoveryIneligible) {
			t.Fatal("ungranted reservation", err)
		}
		if i == 129 {
			running = h
			continue
		}
		if err := h.Finish(canceled); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	status, err := s.RetryReturnedExecutions(context.Background())
	if err != nil || status != (LocalExecutorStatus{Running: 1, Returned: 1}) {
		t.Fatal("retry batch exceeded its limit or finished a running claim", status, err)
	}
	status, err = s.RetryReturnedExecutions(context.Background())
	if err != nil || status != (LocalExecutorStatus{Running: 1}) {
		t.Fatal("remaining returned marker did not drain", status, err)
	}
	if err := s.FinishReturnedExecutions(context.Background()); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("shutdown accepted a running handler", err)
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("running handler lost storage ownership", err)
	}
	if err := running.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReturnedExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalExecutorRetryPreservesUnknownProviderOutcome(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, _, handle := localUnknown(t, s)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := handle.Finish(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status, err := s.RetryReturnedExecutions(context.Background())
	if err != nil || status != (LocalExecutorStatus{}) {
		t.Fatal(status, err)
	}
	m, _ = s.Get(m.ID)
	got := m.Actions[a.ID]
	if got.State != Unknown || !got.Held() || got.Outcome != a.Outcome || got.ExecutorFinishedAt.IsZero() {
		t.Fatal("completion recovery resolved unknown external effect", got)
	}
}
