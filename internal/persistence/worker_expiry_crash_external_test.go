//go:build externaljobs

package persistence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
				admin := openAuthenticationAdmin(t, cfg)
				replayed, ok := admin.Get(saved.Monitor.ID)
				got, _ := json.Marshal(replayed)
				want, _ := json.Marshal(saved.Monitor)
				if !ok || string(got) != string(want) {
					_ = admin.Close()
					t.Fatal("stopped replay changed the committed action or monitor")
				}
				if err := admin.Close(); err != nil {
					t.Fatal(err)
				}
				openedAt := time.Now().UTC()
				s, err := Open(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				m, _ := s.Get(saved.Monitor.ID)
				expected := saved.Monitor.Clone()
				// The fixture replays format 45 with an absolute action deadline.
				// Current recovery adds notification-state and installation metadata;
				// the comparison below still checks every original action field.
				if expected.Notifications == nil || expected.Notifications.Version != 0 {
					t.Fatal("fixture must retain pre-condition notification state")
				}
				expected.Notifications.Version = NotificationConditionFormatVersion
				if expected.InstallationID != "" || s.nodeID != admin.nodeID {
					t.Fatal("fixture must migrate the original unnamespaced installation")
				}
				expected.InstallationID = identity("installation/" + admin.nodeID)
				if category == "recovery" && phase == "before" {
					// Recovering a still-queued action upgrades the monitor's dispatch
					// semantics, while the original action and absolute deadline stay intact.
					if expected.ActionVersion != ActionFormatVersion {
						t.Fatal("fixture must retain the historical action state version")
					}
					expected.ActionVersion = DispatchFormatVersion
					if m.RecoveryCheckAfter.Before(openedAt) || m.RecoveryCheckAfter.After(time.Now().UTC()) {
						t.Fatal("restart did not require a fresh check for the queued recovery")
					}
					expected.RecoveryCheckAfter = m.RecoveryCheckAfter
				}
				got, _ = json.Marshal(m)
				want, _ = json.Marshal(expected)
				if string(got) != string(want) {
					var actual, original map[string]json.RawMessage
					if err := json.Unmarshal(got, &actual); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(want, &original); err != nil {
						t.Fatal(err)
					}
					var fields []string
					for key, value := range actual {
						if !bytes.Equal(value, original[key]) {
							fields = append(fields, key)
						}
					}
					for key := range original {
						if _, ok := actual[key]; !ok {
							fields = append(fields, key)
						}
					}
					slices.Sort(fields)
					t.Fatalf("restart changed fields beyond the recovery check barrier: %v", fields)
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
				a := retainedAction(t, s, saved.Intent.ActionID)
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
