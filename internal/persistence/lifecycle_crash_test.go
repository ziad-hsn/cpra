package persistence

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestQueuedRecoveryCrashHelper(t *testing.T) {
	directory := os.Getenv("CPRA_QUEUED_RECOVERY_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := testMonitor()
	m.Policy = lifecyclePolicy()
	m.Policy.Intervention, m.Policy.Endpoints = true, nil
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: now})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: now.Add(time.Millisecond), Generation: 1, Outcome: "failure"})
	fmt.Println("QUEUED_RECOVERY_COMMITTED")
	select {}
}

func TestRealProcessQueuedRecoveryCrash(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(fmt.Sprint("healthy=", healthy), func(t *testing.T) {
			var checks, effects atomic.Int32
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					effects.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				checks.Add(1)
				if healthy {
					w.WriteHeader(http.StatusNoContent)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer receiver.Close()
			cfg := testConfig(t)
			child := exec.Command(os.Args[0], "-test.run=^TestQueuedRecoveryCrashHelper$")
			child.Env = append(os.Environ(), "CPRA_QUEUED_RECOVERY_CRASH_DIRECTORY="+cfg.Storage.Directory)
			out, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(out)
				for scan.Scan() {
					if scan.Text() == "QUEUED_RECOVERY_COMMITTED" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited before committing recovery intent")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("child did not reach crash boundary")
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			m, ok := s.Get(testMonitor().ID)
			if !ok || m.RecoveryCheckAfter.IsZero() || effects.Load() != 0 || checks.Load() != 0 {
				t.Fatal("restart lost recovery barrier or executed work during replay")
			}
			a := interventionAction(t, m)
			start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC()}
			if submit(t, s, start)[0].Allowed {
				t.Fatal("restart allowed intervention without a fresh check")
			}
			began := time.Now().UTC()
			check := (&jobs.PulseHTTPJob{URL: receiver.URL, Method: http.MethodGet, ExpectedStatus: []int{http.StatusNoContent}, Timeout: time.Second}).Execute()
			outcome := "success"
			if check.Err != nil {
				outcome = "failure"
			}
			m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), ExecutionStart: began, Generation: m.Generation + 1, Outcome: outcome})[0].Monitor
			start.At = time.Now().UTC()
			allowed := submit(t, s, start)[0].Allowed
			if allowed == healthy || checks.Load() != 1 {
				t.Fatal("fresh network check did not govern original recovery intent")
			}
			if allowed {
				result := (&jobs.InterventionWebhookJob{URL: receiver.URL, Timeout: time.Second}).Execute()
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "success"})
			}
			if healthy && (effects.Load() != 0 || m.Actions[a.ID].State != Cancelled) || !healthy && effects.Load() != 1 {
				t.Fatal("unexpected recovery count after crash")
			}
		})
	}
}
