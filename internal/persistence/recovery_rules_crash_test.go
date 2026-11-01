package persistence

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type ruleCrashBoundary struct {
	Monitor  string
	Action   Action
	Decision RecoveryDecision
	Attempts int
}

// The child stops only at a committed boundary. The parent owns the independent
// HTTP receiver and kills the process without calling Store.Close.
func TestRecoveryRulesCrashHelper(t *testing.T) {
	stage := os.Getenv("CPRA_RULE_CRASH_STAGE")
	if stage == "" {
		return
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = os.Getenv("CPRA_RULE_CRASH_DIRECTORY")
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := ruleMonitor(t, 1)
	at := time.Now().UTC()
	if result := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})[0]; result.Err != nil {
		t.Fatal(result.Err)
	}
	boundary := ruleCrashBoundary{Monitor: m.ID}
	if stage != "before_intent" {
		failure, err := api.CanonicalFailure("1", "network.connection_refused", "connect")
		if err != nil {
			t.Fatal(err)
		}
		result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Millisecond), Generation: 1, Outcome: "failure", Failure: CheckFailure(failure)})[0]
		if result.Err != nil || result.Monitor == nil {
			t.Fatal("intent", result.Err)
		}
		m = *result.Monitor
		boundary.Action = queuedRuleAction(t, m)
		if stage != "intent" {
			a := boundary.Action
			result = submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "webhook", At: time.Now().UTC(), NoticeRevision: ActionNoticeRevision(a)})[0]
			if result.Err != nil || !result.Allowed {
				t.Fatal("start", result.Err)
			}
			if stage != "start" {
				effect := (&jobs.InterventionWebhookJob{URL: os.Getenv("CPRA_RULE_CRASH_RECEIVER"), Timeout: time.Second}).Execute()
				if effect.Err != nil {
					t.Fatal(effect.Err)
				}
				if stage != "effect" {
					result = submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "success"})[0]
					if result.Err != nil {
						t.Fatal("result", result.Err)
					}
				}
			}
		}
	}
	if stage == "snapshot" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	committed, ok := s.Get(m.ID)
	if !ok {
		t.Fatal("committed monitor missing")
	}
	boundary.Decision, boundary.Attempts = committed.RecoveryDecision, committed.recoveryAttempts()
	if boundary.Action.ID != "" {
		record, found, err := s.Action(boundary.Action.ID)
		if err != nil || !found {
			t.Fatal("committed action missing", err)
		}
		boundary.Action = record.Action
	}
	raw, _ := json.Marshal(boundary)
	fmt.Println("RULE_CRASH_READY " + string(raw))
	select {}
}

func TestRecoveryRulesSurviveProcessKill(t *testing.T) {
	for _, stage := range []string{"before_intent", "intent", "start", "effect", "result", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			var effects atomic.Int32
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("unexpected provider request")
				}
				effects.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer receiver.Close()
			cfg := testConfig(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryRulesCrashHelper$")
			child.Env = append(os.Environ(), "CPRA_RULE_CRASH_STAGE="+stage, "CPRA_RULE_CRASH_DIRECTORY="+cfg.Storage.Directory, "CPRA_RULE_CRASH_RECEIVER="+receiver.URL)
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if line, ok := strings.CutPrefix(scanner.Text(), "RULE_CRASH_READY "); ok {
						ready <- line
						return
					}
				}
				ready <- ""
			}()
			var boundary ruleCrashBoundary
			select {
			case raw := <-ready:
				if json.Unmarshal([]byte(raw), &boundary) != nil || boundary.Monitor == "" {
					t.Fatal("child did not reach committed boundary")
				}
			case <-ctx.Done():
				t.Fatal("child did not reach committed boundary", ctx.Err())
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err == nil || child.ProcessState.Success() {
				t.Fatal("child completed instead of being killed")
			}
			wantEffects := int32(0)
			if stage == "effect" || stage == "result" || stage == "snapshot" {
				wantEffects = 1
			}
			s, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			m, ok := s.Get(boundary.Monitor)
			if !ok || m.Policy.RecoveryRules.Digest() != ruleMonitor(t, 1).Policy.RecoveryRules.Digest() || effects.Load() != wantEffects {
				t.Fatal("replay changed policy or invoked a provider")
			}
			if m.RecoveryDecision.Selection != boundary.Decision.Selection || m.RecoveryDecision.Deadline != boundary.Decision.Deadline || m.recoveryAttempts() != boundary.Attempts {
				t.Fatal("restart lost incident selection, deadline or consumed attempts")
			}
			if stage == "before_intent" {
				if len(m.Actions) != 0 || m.RecoveryDecision.Count != 0 {
					t.Fatal("restart invented qualification or intent")
				}
				return
			}
			a := boundary.Action
			retained, found, err := s.Action(a.ID)
			if err != nil || !found || retained.Action.Rule != a.Rule || retained.Action.Deadline != a.Deadline {
				t.Fatal("restart changed original selection or budget", err)
			}
			wantState := Unknown
			if stage == "intent" {
				wantState = Queued
			} else if stage == "result" || stage == "snapshot" {
				wantState = Succeeded
			}
			if retained.Action.State != wantState {
				t.Fatal("restart changed outcome", retained.Action.State, wantState)
			}
			start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "webhook", At: time.Now().UTC(), NoticeRevision: ActionNoticeRevision(retained.Action)}
			if result := submit(t, s, start)[0]; result.Allowed {
				t.Fatal("restart repeated work or bypassed fresh observation")
			}
			// Fresh successful observations close the incident; neither they nor replay
			// establish that an interrupted intervention completed successfully.
			for range m.Policy.Healthy {
				began := time.Now().UTC()
				result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), ExecutionStart: began, Generation: m.Generation + 1, Outcome: "success"})[0]
				if result.Err != nil || result.Monitor == nil {
					t.Fatal(result.Err)
				}
				m = *result.Monitor
			}
			retained, found, err = s.Action(a.ID)
			if stage == "intent" {
				wantState = Cancelled
			}
			if err != nil || !found || m.Incident || effects.Load() != wantEffects || retained.Action.State != wantState || retained.Action.Rule != a.Rule || retained.Action.Deadline != a.Deadline || wantState == Unknown && !retained.Action.Held() {
				t.Fatal("health progression changed effect evidence or replayed intent", err)
			}
		})
	}
}
