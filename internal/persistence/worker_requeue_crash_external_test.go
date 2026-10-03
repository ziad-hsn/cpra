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

	"github.com/google/uuid"
)

func workerRequeueCrashBoundary(t *testing.T, store *Store, authority WorkerAuthority, poll WorkerPollResponse, in WorkerExecutionIntent, phase string) {
	t.Helper()
	config := store.config
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if phase != "requeue-before" {
		if n, err := store.RequeueWorkerExecutions(t.Context(), time.Now().UTC(), 1); err != nil || n != 1 {
			t.Fatal("requeue after owner restart", n, err)
		}
	}
	if phase == "requeue-snapshot" {
		if err := store.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	r, _, err := store.WorkerExecution(t.Context(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := store.Get(in.MonitorID)
	state := workerResultCrashState{Authority: authority, Start: workerStartFromOffer(poll), Record: r, Monitor: m}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_RESULT_CRASH_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_REQUEUE_BOUNDARY")
	select {}
}

func TestWorkerRequeueSurvivesProcessKill(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		for _, phase := range []string{"requeue-before", "requeue-committed", "requeue-snapshot"} {
			t.Run(category+"/"+phase, func(t *testing.T) {
				config := testConfig(t)
				statePath := filepath.Join(t.TempDir(), "requeue.json")
				cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerResultProcessCrashHelper$")
				cmd.Env = append(os.Environ(), "CPRA_WORKER_RESULT_CRASH_PHASE="+phase,
					"CPRA_WORKER_RESULT_CRASH_CATEGORY="+category, "CPRA_WORKER_RESULT_CRASH_DIRECTORY="+config.Storage.Directory,
					"CPRA_WORKER_RESULT_CRASH_STATE="+statePath)
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
						if scanner.Text() == "WORKER_REQUEUE_BOUNDARY" {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case ok := <-ready:
					if !ok {
						t.Fatal("child exited before requeue boundary")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("child did not reach requeue boundary")
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
				store, err := Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				retained, _, err := store.WorkerExecution(t.Context(), saved.Record.Intent.ID)
				if err != nil || !reflect.DeepEqual(retained, saved.Record) {
					t.Fatal("replay changed the committed requeue boundary", err)
				}
				if n, err := store.RequeueWorkerExecutions(t.Context(), time.Now().UTC(), 1); err != nil || n != 1 {
					t.Fatal("new owner could not adopt safe queued work", n, err)
				}
				queued, _, _ := store.WorkerExecution(t.Context(), saved.Record.Intent.ID)
				if queued.Lifecycle != nil || len(queued.RetiredOffers) != 1 || !reflect.DeepEqual(queued.Intent, saved.Record.Intent) {
					t.Fatal("recovery lost lease fence or original payload")
				}
				for _, mode := range []string{"begin", "reconcile"} {
					start := saved.Start
					start.Mode = mode
					result, err := store.CommitWorkerStart(t.Context(), saved.Authority, start)
					if err != nil || result.Disposition != "rejected" || result.GrantID != "" || result.ReceiptID != "" {
						t.Fatal("old lease executable after process kill", err)
					}
				}
				in := queued.Intent
				poll, err := store.CommitWorkerPoll(t.Context(), saved.Authority, WorkerPollRequest{ServerID: saved.Start.ServerID, WorkerID: saved.Authority.WorkerID,
					ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: []WorkerSessionCapability{{JobTypeID: in.JobType.JobTypeID, Version: in.JobType.Version, Category: category}}})
				if err != nil || len(poll.Offers) != 1 || poll.Offers[0].LeaseID == saved.Start.LeaseID {
					t.Fatal("new offer after recovery", err)
				}
				start := workerStartFromOffer(poll)
				grant, err := store.CommitWorkerStart(t.Context(), saved.Authority, start)
				if err != nil || grant.Disposition != "granted" {
					t.Fatal("replacement start after recovery", err)
				}
				repeat, err := store.CommitWorkerStart(t.Context(), saved.Authority, start)
				if err != nil || repeat.Disposition != "started" || repeat.GrantID != grant.GrantID {
					t.Fatal("replacement start issued twice", err)
				}
				m, _ := store.Get(in.MonitorID)
				if m.TotalChecks != saved.Monitor.TotalChecks || m.LastOutcome != saved.Monitor.LastOutcome {
					t.Fatal("requeue/replay invented a health observation")
				}
				if category != "check" && (m.Actions[in.ActionID].State != Started || m.Actions[in.ActionID].ExecutorSession != grant.GrantID) {
					t.Fatal("action does not belong to its single replacement grant")
				}
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
