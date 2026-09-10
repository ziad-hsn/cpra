package durable

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

	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/runtimeconfig"
	"github.com/mlange-42/ark/ecs"
)

func TestActionCrashHelper(t *testing.T) {
	phase := os.Getenv("CPRA_ACTION_CRASH_PHASE")
	if phase == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = os.Getenv("CPRA_ACTION_CRASH_DIRECTORY")
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	now := time.Now().UTC()
	if phase != "before_admission" {
		submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
		m, _ := s.Get("stable-one")
		id := sortedActions(m.Actions)[0]
		if phase != "after_intent" {
			submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now})
			if phase == "after_external_success" || phase == "after_result_commit" {
				code := schema.CodeConfig{Notify: "webhook", Dispatch: true, Config: &schema.CodeNotificationWebhook{URL: os.Getenv("CPRA_ACTION_CRASH_TARGET")}}
				list, err := jobs.CreateCodeJobs("crash-fixture", code, ecs.Entity{}, "red", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if result := list[0].Execute(); result.Err != nil {
					t.Fatal(result.Err)
				}
				if phase == "after_result_commit" {
					submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: time.Now().UTC(), Outcome: "success"})
				}
			}
		}
	}
	fmt.Println("CRASH_BOUNDARY")
	select {}
}

func TestRealProcessActionCrashBoundaries(t *testing.T) {
	for _, phase := range []string{"before_admission", "after_intent", "after_started", "after_external_success", "after_result_commit"} {
		t.Run(phase, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
			defer target.Close()
			c := testConfig(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestActionCrashHelper$")
			cmd.Env = append(os.Environ(), "CPRA_ACTION_CRASH_PHASE="+phase, "CPRA_ACTION_CRASH_DIRECTORY="+c.Storage.Directory, "CPRA_ACTION_CRASH_TARGET="+target.URL)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(out)
				for scan.Scan() {
					if scan.Text() == "CRASH_BOUNDARY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("crash boundary not reached")
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			before := received.Load()
			s, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			m, _ := s.Get("stable-one")
			if received.Load() != before {
				t.Fatal("restore/replay invoked external work")
			}
			if phase == "before_admission" {
				if len(m.Actions) != 0 {
					t.Fatal("uncommitted intent recovered")
				}
				return
			}
			ids := sortedActions(m.Actions)
			if len(ids) != 2 {
				t.Fatal(m.Actions)
			}
			expected := Queued
			if phase == "after_started" || phase == "after_external_success" {
				expected = Unknown
			}
			if phase == "after_result_commit" {
				expected = Succeeded
			}
			if m.Actions[ids[0]].State != expected || m.Actions[ids[1]].State != Queued {
				t.Fatalf("phase %s: %+v", phase, m.Actions)
			}
			if (phase == "after_external_success" || phase == "after_result_commit") && before != 1 {
				t.Fatal("real provider path was not exercised")
			}
		})
	}
}
