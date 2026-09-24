package persistence

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestLocalExecutorCanceledAdmittedStartReturnsClaim(t *testing.T) {
	s, run := dormantCatalogStore(t)
	at := time.Now().UTC()
	m := testMonitor()
	m = *transition(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}).Monitor
	m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at}).Monitor
	a := m.Actions[sortedActions(m.Actions)[0]]
	s.executorSession = "local-test-session"
	s.fsm.image.LocalExecutorSession = s.executorSession
	s.fsm.image.Version = CatalogFormatVersion
	s.fsm.image.Monitors[m.ID] = m
	ctx, cancel := context.WithCancel(context.Background())
	type begun struct {
		h   *LocalExecution
		err error
	}
	finished := make(chan begun, 1)
	var effects atomic.Int64
	go func() {
		h, err := s.BeginLocalAction(ctx, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at})
		if err == nil {
			effects.Add(1)
		}
		finished <- begun{h, err}
	}()
	waitBudgetCondition(t, "admitted start", func() bool { return len(s.requests) == 1 })
	cancel()
	got := <-finished
	if got.h == nil || !errors.Is(got.err, ErrCommitUnconfirmed) || effects.Load() != 0 {
		t.Fatal("uncertain start lost claim or permitted invocation", got.err)
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("uncertain start released data ownership", err)
	}
	// The invocation has returned. A completion marker queued after the uncertain
	// start must wait for that same ordered command stream before dropping claim.
	completion := make(chan error, 1)
	go func() { completion <- got.h.Finish(context.Background()) }()
	waitBudgetCondition(t, "ordered completion marker", func() bool { return len(s.requests) == 2 })
	run()
	if err := <-completion; err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	a = m.Actions[a.ID]
	if a.ExecutorFinishedAt.IsZero() || a.State != Started || effects.Load() != 0 {
		t.Fatal("executor completion invented provider result", a)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestLocalExecutorCancelledFinishRetainsClaimUntilRetry(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, _, h := localUnknown(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("uncommitted completion released lock", err)
	}
	m, _ = s.Get(m.ID)
	if !m.Actions[a.ID].ExecutorFinishedAt.IsZero() {
		t.Fatal("cancelled marker committed")
	}
	if err := s.FinishReturnedExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal("idempotent completion", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalExecutorProcessHelper(t *testing.T) {
	mode := os.Getenv("CPRA_EXECUTOR_FENCE_HELPER")
	if mode == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = os.Getenv("CPRA_EXECUTOR_FENCE_DIR")
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	m, g := manualFixture(t, s)
	at := time.Now().UTC()
	m = *mustResult(t, s, manualCommand(m, g, "native-manual", at)).Monitor
	a := latestManual(t, m)
	h, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: g, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "finish-failure" {
		// Preserve a committed known provider fact, then inject an actual unavailable
		// history writer before the executor completion commit.
		r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Millisecond), Outcome: "success"})[0]
		if r.Monitor == nil || r.Monitor.Actions[a.ID].State != Succeeded {
			t.Fatal("known result missing")
		}
		if err := s.History().Close(); err != nil {
			t.Fatal(err)
		}
		if err := h.Finish(context.Background()); err == nil {
			t.Fatal("closed history completion succeeded")
		}
		if s.Status().Ready {
			t.Fatal("failed completion remained ready")
		}
	}
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("unfenced executor released native store", err)
	}
	fmt.Println("EXECUTOR_LOCK_HELD")
	select {}
}
func TestLocalExecutorNativeProcessFenceAndFailedCompletion(t *testing.T) {
	for _, mode := range []string{"active", "finish-failure"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig(t)
			child := exec.Command(os.Args[0], "-test.run=^TestLocalExecutorProcessHelper$")
			child.Env = append(os.Environ(), "CPRA_EXECUTOR_FENCE_HELPER="+mode, "CPRA_EXECUTOR_FENCE_DIR="+c.Storage.Directory)
			out, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(out)
				for scan.Scan() {
					if scan.Text() == "EXECUTOR_LOCK_HELD" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited before fence")
				}
			case <-time.After(20 * time.Second):
				t.Fatal("child fence deadline")
			}
			if second, err := Open(context.Background(), c); err == nil {
				_ = second.Close()
				t.Fatal("live executor admitted a second owner")
			}
			_ = child.Process.Kill()
			_ = child.Wait()
			s, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			m, _ := s.Get("manual")
			a := latestManual(t, m)
			row, ok, err := s.Action(a.ID)
			if err != nil || !ok || !row.ExecutorFenced {
				t.Fatal("exclusive restart did not fence old local session", row, err)
			}
			if mode == "active" {
				if a.State != Unknown || !a.Held() {
					t.Fatal("interrupted executor did not remain unknown", a)
				}
				r := mustResult(t, s, reviewCommand(m, a, "post-fence-review", "inconclusive", time.Now().UTC()))
				a = r.Monitor.Actions[a.ID]
				if !a.Held() {
					t.Fatal("inconclusive review released hold")
				}
				r = mustResult(t, s, reviewCommand(*r.Monitor, a, "post-fence-resolution", "rejected", time.Now().UTC()))
				if r.Monitor.Actions[a.ID].Held() || r.Monitor.Actions[a.ID].State != Unknown {
					t.Fatal("fenced assertion changed provider facts")
				}
			} else if a.State != Succeeded || a.Outcome != "accepted" {
				t.Fatal("failed finish discarded known provider fact", a)
			}
		})
	}
}

func TestLocalExecutorCompletionSerializationHonorsDeadline(t *testing.T) {
	s := openCatalogMemory(t)
	_, _, _, h := localUnknown(t, s)
	// Hold completion serialization, as another still-waiting Finish call would.
	h.serial <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Finish(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion serialization ignored deadline")
	}
	<-h.serial
	if err := s.Close(); !errors.Is(err, ErrLocalExecutorActive) {
		t.Fatal("timeout dropped executor claim", err)
	}
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
}
