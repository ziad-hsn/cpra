//go:build externaljobs

package persistence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func workerCheckRetirementCrashBoundary(t *testing.T, s *Store, a WorkerAuthority, start WorkerStartRequest, input WorkerResultInput, phase string) {
	t.Helper()
	record, ok, err := s.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil || !ok || record.Result == nil {
		t.Fatal(err)
	}
	monitor, _ := s.Get(record.Intent.MonitorID)
	saved := workerResultCrashState{Authority: a, Start: start, Input: input, Record: record, Monitor: monitor, SLO: cloneImageExtensions(s.fsm.image).WorkerSLO}
	if phase != "retire-before" {
		if n, err := s.RetireWorkerChecks(t.Context(), record.Result.RecordedAt.Add(WorkerCheckReceiptLifetime), 1); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	}
	if phase == "retire-snapshot" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_RESULT_CRASH_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_RETIREMENT_BOUNDARY")
	select {}
}

func TestWorkerCheckRetentionSurvivesProcessKill(t *testing.T) {
	for _, phase := range []string{"retire-before", "retire-committed", "retire-snapshot"} {
		t.Run(phase, func(t *testing.T) {
			cfg := testConfig(t)
			statePath := filepath.Join(t.TempDir(), "retirement.json")
			cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerResultProcessCrashHelper$")
			cmd.Env = append(os.Environ(), "CPRA_WORKER_RESULT_CRASH_PHASE="+phase, "CPRA_WORKER_RESULT_CRASH_CATEGORY=check", "CPRA_WORKER_RESULT_CRASH_DIRECTORY="+cfg.Storage.Directory, "CPRA_WORKER_RESULT_CRASH_STATE="+statePath)
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
				scan := bufio.NewScanner(out)
				for scan.Scan() {
					if scan.Text() == "WORKER_RETIREMENT_BOUNDARY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited before retirement boundary")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("child did not reach retirement boundary")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			raw, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var saved workerResultCrashState
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r, found, err := s.WorkerExecution(t.Context(), saved.Input.ExecutionID)
			if err != nil || found != (phase == "retire-before") {
				t.Fatal("replay boundary", found, err)
			}
			if found && !reflect.DeepEqual(r, saved.Record) {
				t.Fatal("replay changed retained record")
			}
			m, _ := s.Get(saved.Monitor.ID)
			// Compare every persisted field, allowing omitempty maps to decode as nil.
			gotMonitor, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			wantMonitor, err := json.Marshal(saved.Monitor)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotMonitor, wantMonitor) || !reflect.DeepEqual(s.fsm.image.WorkerSLO, saved.SLO) {
				t.Fatal("crash changed health or SLO")
			}
			want := 0
			if found {
				want = 1
			}
			if n, err := s.RetireWorkerChecks(t.Context(), saved.Record.Result.RecordedAt.Add(WorkerCheckReceiptLifetime), 1); err != nil || n != want {
				t.Fatal("restart retirement", n, err)
			}
			if _, err := s.CommitWorkerResult(t.Context(), saved.Authority, saved.Input); !errors.Is(err, ErrWorkerCheckExpired) {
				t.Fatal("lost result reply", err)
			}
			if _, err := s.CommitWorkerStart(t.Context(), saved.Authority, saved.Start); !errors.Is(err, ErrWorkerCheckExpired) {
				t.Fatal("expired check restarted", err)
			}
			requireWorkerProjectionView(t, s, saved.Monitor.ID)
			if err := validateImageExtensions(s.fsm.image); err != nil {
				t.Fatal(err)
			}
			next, err := s.AllocateWorkerCheckID(t.Context())
			if err != nil || next == saved.Input.ExecutionID {
				t.Fatal("reused identity", err)
			}
		})
	}
}
