package systems

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type agentCrashBoundary struct {
	MonitorID, Revision, ActionID, Stage string
	QueuedAt, Deadline                   time.Time
	ExecutionBudget                      time.Duration
}

func TestA2AControllerCrashProcess(t *testing.T) {
	stage := os.Getenv("CPRA_AGENT_TEST_STAGE")
	if stage == "" {
		return
	}
	s, dispatch, results := agentControllerAt(t, os.Getenv("CPRA_AGENT_TEST_URL"), "intervention", os.Getenv("CPRA_AGENT_TEST_DIRECTORY"))
	boundary := func() {
		m, ok := monitorWithActionHistory(t, s.store, dispatch.MonitorID)
		a := m.Actions[dispatch.ActionID]
		if !ok || a.QueuedAt.IsZero() || a.ExecutionBudget <= 0 || stage != "before-admission" && a.Deadline.IsZero() || stage == "before-admission" && !a.Deadline.IsZero() {
			t.Fatal("crash boundary lost original action budget")
		}
		data, err := json.Marshal(agentCrashBoundary{MonitorID: dispatch.MonitorID, Revision: dispatch.Revision, ActionID: dispatch.ActionID, Stage: stage, QueuedAt: a.QueuedAt, Deadline: a.Deadline, ExecutionBudget: a.ExecutionBudget})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("CPRA_AGENT_TEST_BOUNDARY"), data, 0600); err != nil {
			t.Fatal(err)
		}
		select {} // The parent kills this process at the confirmed boundary.
	}
	authorize := dispatch.Authorize
	dispatch.Authorize = func(ctx context.Context) (jobs.ActionExecution, error) {
		if stage == "before-admission" {
			boundary()
		}
		e, err := authorize(ctx)
		if err != nil {
			return e, err
		}
		if stage == "after-start" {
			boundary()
		}
		record := e.RecordAgentReceipt
		e.RecordAgentReceipt = func(ctx context.Context, receipt jobs.A2AReceipt) error {
			if stage == "after-send" {
				boundary()
			}
			err := record(ctx, receipt)
			if err == nil && stage == "after-receipt" {
				boundary()
			}
			return err
		}
		return e, nil
	}
	r := dispatch.Execute()
	if r.Err != nil || r.FinalizationErr != nil {
		t.Fatal("unexpected provider outcome", r)
	}
	if stage == "before-result" {
		boundary()
	}
	results <- []jobs.Result{r}
	if !s.drain() {
		t.Fatal(s.lastError)
	}
	if stage == "after-result" {
		boundary()
	}
	t.Fatal("invalid crash boundary")
}

func TestA2AControllerProcessCrashRecovery(t *testing.T) {
	for _, stage := range []string{"before-admission", "after-start", "after-send", "after-receipt", "before-result", "after-result"} {
		t.Run(stage, func(t *testing.T) {
			var sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("unexpected poll or cancellation")
				}
				sends.Add(1)
				state := "TASK_STATE_COMPLETED"
				if stage == "after-receipt" {
					state = "TASK_STATE_WORKING"
				}
				w.Header().Set("Content-Type", "application/a2a+json")
				_, _ = w.Write([]byte(`{"task":{"id":"task-before-crash","status":{"state":"` + state + `"}}}`))
			}))
			defer server.Close()
			directory, record := t.TempDir(), filepath.Join(t.TempDir(), "boundary.json")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestA2AControllerCrashProcess$", "-test.timeout=25s")
			command.Env = append(os.Environ(), "CPRA_AGENT_TEST_STAGE="+stage, "CPRA_AGENT_TEST_URL="+server.URL,
				"CPRA_AGENT_TEST_DIRECTORY="+directory, "CPRA_AGENT_TEST_BOUNDARY="+record)
			log, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = command.Process.Kill()
					<-done
				}
			})
			var boundary agentCrashBoundary
			timer := time.NewTimer(12 * time.Second)
			defer timer.Stop()
			for boundary.ActionID == "" {
				if data, err := os.ReadFile(record); err == nil {
					_ = json.Unmarshal(data, &boundary)
				}
				if boundary.ActionID != "" {
					break
				}
				select {
				case err := <-done:
					waited = true
					logs, _ := os.ReadFile(log.Name())
					t.Fatalf("child exited before boundary: %v\n%s", err, logs)
				case <-timer.C:
					t.Fatal("child did not reach boundary")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if boundary.Stage != stage {
				t.Fatal("wrong process boundary")
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("process exited without forced termination")
			}
			waited = true
			wantSends := int32(1)
			if stage == "before-admission" || stage == "after-start" {
				wantSends = 0
			}
			if sends.Load() != wantSends {
				t.Fatal("wrong independently observed send count", sends.Load())
			}
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = directory
			store, err := persistence.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			m, ok := monitorWithActionHistory(t, store, boundary.MonitorID)
			a := m.Actions[boundary.ActionID]
			wantState := persistence.Unknown
			if stage == "before-admission" {
				wantState = persistence.Queued
			}
			if stage == "after-result" {
				wantState = persistence.Succeeded
			}
			if !ok || a.State != wantState || a.Revision != boundary.Revision || sends.Load() != wantSends {
				t.Fatal("restart changed original action or sent another message", a, sends.Load())
			}
			if !a.QueuedAt.Equal(boundary.QueuedAt) || !a.Deadline.Equal(boundary.Deadline) || a.ExecutionBudget != boundary.ExecutionBudget {
				t.Fatal("restart changed the original action deadline")
			}
			wantReceipt := stage == "after-receipt" || stage == "before-result" || stage == "after-result"
			if wantReceipt != (a.AgentReceipt != nil) {
				t.Fatal("receipt durability boundary violated", a)
			}
			if stage != "before-admission" {
				h, err := store.BeginLocalAction(context.Background(), persistence.Command{Kind: "start", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, Driver: "a2a", At: time.Now().UTC()})
				if h != nil {
					_ = h.Finish(context.Background())
				}
				if err == nil {
					t.Fatal("restart granted repeated invocation")
				}
			} else {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				resumed, dispatch, results := agentControllerAt(t, server.URL, "intervention", directory)
				if dispatch.ActionID != boundary.ActionID || !dispatch.Deadline.Equal(boundary.Deadline) {
					t.Fatal("resume changed the original action identity or deadline")
				}
				current, _ := monitorWithActionHistory(t, resumed.store, boundary.MonitorID)
				action := current.Actions[boundary.ActionID]
				if !action.QueuedAt.Equal(boundary.QueuedAt) || !action.Deadline.Equal(boundary.Deadline) {
					t.Fatal("fresh observation renewed the original action deadline")
				}
				r := dispatch.Execute()
				results <- []jobs.Result{r}
				if r.Err != nil || !resumed.drain() || sends.Load() != 1 {
					t.Fatal("safe queued action did not resume exactly once", r)
				}
				current, _ = monitorWithActionHistory(t, resumed.store, boundary.MonitorID)
				action = current.Actions[boundary.ActionID]
				if action.ID != boundary.ActionID || action.State != persistence.Succeeded ||
					!action.QueuedAt.Equal(boundary.QueuedAt) || !action.Deadline.Equal(action.StartedAt.Add(boundary.ExecutionBudget)) || action.ExecutionBudget != boundary.ExecutionBudget {
					t.Fatal("execution or result commit changed the original action budget")
				}
			}
		})
	}
}
