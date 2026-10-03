//go:build externaljobs

package persistence

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestWorkerScheduleProcessHelper(t *testing.T) {
	phase := os.Getenv("CPRA_WORKER_SCHEDULE_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = os.Getenv("CPRA_WORKER_SCHEDULE_DIRECTORY")
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.ScheduleWorkerCheck(time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if phase != "before" {
		commitWorkerSchedule(t, s, s.WorkerScheduleCheckpoint(time.Now()))
	}
	if phase == "snapshot" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	s.fsm.mu.RLock()
	raw, err := json.Marshal(s.fsm.image.WorkerSLO)
	s.fsm.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_SCHEDULE_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_SCHEDULE_BOUNDARY")
	select {}
}

func TestWorkerScheduleSurvivesProcessKill(t *testing.T) {
	for _, phase := range []string{"before", "committed", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			cfg := testConfig(t)
			state := filepath.Join(t.TempDir(), "schedule.json")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerScheduleProcessHelper$")
			cmd.Env = append(os.Environ(), "CPRA_WORKER_SCHEDULE_PHASE="+phase, "CPRA_WORKER_SCHEDULE_DIRECTORY="+cfg.Storage.Directory, "CPRA_WORKER_SCHEDULE_STATE="+state)
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
					if scanner.Text() == "WORKER_SCHEDULE_BOUNDARY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited before schedule boundary")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("child did not reach schedule boundary")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			raw, err := os.ReadFile(state)
			if err != nil {
				t.Fatal(err)
			}
			var saved *workerSLOImage
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.fsm.mu.RLock()
			matches := reflect.DeepEqual(saved, s.fsm.image.WorkerSLO)
			s.fsm.mu.RUnlock()
			if !matches {
				t.Fatal("crash recovery lost or duplicated committed obligations")
			}
			if s.WorkerScheduleCheckpoint(time.Now()) != nil {
				t.Fatal("new owner reused old counter identity")
			}
			view := s.SLOView(time.Now())
			if view.CoverageComplete {
				t.Fatal("crash recovery claimed complete measurement coverage")
			}
			if phase == "before" {
				if len(view.Reports) != 0 {
					t.Fatal("uncommitted counts were invented")
				}
			} else if len(view.Reports) != 1 || view.Reports[0].Expected+view.Reports[0].Pending != 3 {
				t.Fatal("wrong recovered denominator", view)
			}
		})
	}
}
