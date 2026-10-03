//go:build externaljobs

package persistence

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type workerExpiryCrashState struct {
	Intent  WorkerExecutionIntent
	Monitor Monitor
}

func TestWorkerExpiryProcessHelper(t *testing.T) {
	phase := os.Getenv("CPRA_WORKER_EXPIRY_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = os.Getenv("CPRA_WORKER_EXPIRY_DIRECTORY")
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := workerExpiryFixture(t, s, os.Getenv("CPRA_WORKER_EXPIRY_CATEGORY"))
	if phase != "before" {
		if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "snapshot" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := s.Get(in.MonitorID)
	raw, err := json.Marshal(workerExpiryCrashState{Intent: in, Monitor: m})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_EXPIRY_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_EXPIRY_BOUNDARY")
	select {}
}

func TestWorkerExpirySurvivesProcessKill(t *testing.T) {
	for _, category := range []string{"recovery", "notification", "inline-notification"} {
		for _, phase := range []string{"before", "committed", "snapshot"} {
			t.Run(category+"/"+phase, func(t *testing.T) {
				cfg := testConfig(t)
				statePath := filepath.Join(t.TempDir(), "expiry.json")
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerExpiryProcessHelper$")
				cmd.Env = append(os.Environ(), "CPRA_WORKER_EXPIRY_PHASE="+phase, "CPRA_WORKER_EXPIRY_CATEGORY="+category,
					"CPRA_WORKER_EXPIRY_DIRECTORY="+cfg.Storage.Directory, "CPRA_WORKER_EXPIRY_STATE="+statePath)
				out, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				ready := make(chan bool, 1)
				go func() {
					scanner := bufio.NewScanner(out)
					for scanner.Scan() {
						if scanner.Text() == "WORKER_EXPIRY_BOUNDARY" {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case ok := <-ready:
					if !ok {
						t.Fatal("child exited before expiry boundary")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("child did not reach expiry boundary")
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				raw, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				var saved workerExpiryCrashState
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				s, err := Open(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				m, _ := s.Get(saved.Monitor.ID)
				got, _ := json.Marshal(m)
				want, _ := json.Marshal(saved.Monitor)
				if string(got) != string(want) {
					t.Fatal("replay changed the committed action or monitor")
				}
				wantEvents := 1
				if phase == "before" {
					wantEvents = 0
				}
				if len(workerExpiryEvents(t, s, m.ID)) != wantEvents {
					t.Fatal("replay changed history at the crash boundary")
				}
				for range 2 {
					if err := s.ExpireWorkerAction(t.Context(), saved.Intent); err != nil {
						t.Fatal("new owner failed to settle the original action", err)
					}
				}
				m, _ = s.Get(m.ID)
				a := m.Actions[saved.Intent.ActionID]
				if a.State != Cancelled || a.Outcome != "worker_admission_expired" || !a.StartedAt.IsZero() || len(workerExpiryEvents(t, s, m.ID)) != 1 {
					t.Fatal("recovery repeated or invented an invocation")
				}
				page, err := s.WorkerExecutions(t.Context(), "", 10)
				if err != nil || len(page.Items) != 0 {
					t.Fatal("replay created executable work", err)
				}
			})
		}
	}
}
