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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type workerQueueCrashState struct {
	Authority WorkerAuthority
	Request   WorkerPollRequest
	Response  WorkerPollResponse
	Records   []WorkerExecutionRecord
	Started   bool
}

func TestWorkerQueueProcessHelper(t *testing.T) {
	phase := os.Getenv("CPRA_WORKER_QUEUE_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = os.Getenv("CPRA_WORKER_QUEUE_DIRECTORY")
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, capabilities, types := workerQueueTypes(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	admin := openAuthenticationAdmin(t, cfg)
	commitWorker(t, admin, workerCommand(t, admin, "bootstrap", p))
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state := workerQueueCrashState{
		Authority: a,
		Request: WorkerPollRequest{ServerID: server.ServerID, WorkerID: p.ID,
			ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: capabilities},
		Records: []WorkerExecutionRecord{
			commitWorkerQueueItem(t, s, types[1], "z-oldest-recovery"),
			commitWorkerQueueItem(t, s, types[2], "y-notification"),
			commitWorkerQueueItem(t, s, types[0], "x-old-check"),
			commitWorkerQueueItem(t, s, types[0], "a-new-check"),
		},
		Started: strings.Contains(phase, "started"),
	}
	if phase != "queued" {
		state.Response, err = s.CommitWorkerPoll(t.Context(), a, state.Request)
		if err != nil || len(state.Response.Offers) != 1 || state.Response.Offers[0].ExecutionID != state.Records[0].Intent.ID {
			t.Fatal("oldest intent not assigned before kill", err)
		}
		if state.Started {
			grant, err := s.CommitWorkerStart(t.Context(), a, workerStartFromOffer(state.Response))
			if err != nil || grant.Disposition != "granted" {
				t.Fatal("start before kill", err, grant.Disposition)
			}
		}
	}
	if strings.HasPrefix(phase, "snapshot-") {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	for i, record := range state.Records {
		state.Records[i], _, err = s.WorkerExecution(t.Context(), record.Intent.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_QUEUE_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_QUEUE_BOUNDARY")
	select {}
}

func TestWorkerQueueOrderSurvivesProcessKill(t *testing.T) {
	for _, phase := range []string{"queued", "offered", "started", "snapshot-offered", "snapshot-started"} {
		t.Run(phase, func(t *testing.T) {
			cfg := testConfig(t)
			statePath := filepath.Join(t.TempDir(), "queue.json")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerQueueProcessHelper$", "-test.timeout=1m")
			cmd.Env = append(os.Environ(), "CPRA_WORKER_QUEUE_PHASE="+phase, "CPRA_WORKER_QUEUE_DIRECTORY="+cfg.Storage.Directory, "CPRA_WORKER_QUEUE_STATE="+statePath)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready, scanned := make(chan struct{}, 1), make(chan struct{})
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(out)
				for scanner.Scan() {
					if scanner.Text() == "WORKER_QUEUE_BOUNDARY" {
						select {
						case ready <- struct{}{}:
						default:
						}
					}
				}
			}()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-done
				}
				<-scanned
			})
			select {
			case <-ready:
			case err := <-done:
				waited = true
				t.Fatalf("child exited before queue boundary: %v\n%s", err, stderr.String())
			case <-time.After(30 * time.Second):
				t.Fatal("child did not reach queue boundary")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-done
			waited = true
			<-scanned
			if strings.Contains(stderr.String(), "DATA RACE") {
				t.Fatalf("child race: %s", stderr.String())
			}
			raw, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var saved workerQueueCrashState
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			admin := openAuthenticationAdmin(t, cfg)
			for _, record := range saved.Records {
				got, ok, err := admin.WorkerExecution(t.Context(), record.Intent.ID)
				if err != nil || !ok || !reflect.DeepEqual(got, record) {
					_ = admin.Close()
					t.Fatal("stopped replay changed committed queue", err)
				}
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := saved.Records
			if saved.Started {
				original := saved.Records[0]
				m, _ := s.Get(original.Intent.MonitorID)
				if a := m.Actions[original.Intent.ActionID]; a.State != Unknown || !a.Held() {
					t.Fatal("restart did not hold uncertain action")
				}
				start := workerStartFromOffer(saved.Response)
				start.Mode = "reconcile"
				got, err := s.CommitWorkerStart(t.Context(), saved.Authority, start)
				if err != nil || got.Disposition != "unknown" {
					t.Fatal("interrupted action became executable", err, got.Disposition)
				}
				want = want[1:]
			}
			adoptable := len(want)
			if !saved.Started {
				adoptable-- // The restored recovery still requires a fresh check.
			}
			if n, err := s.RequeueWorkerExecutions(t.Context(), time.Now().UTC(), 100); err != nil || n != adoptable {
				t.Fatal("safe queue adoption", n, err)
			}
			if !saved.Started {
				original := saved.Records[0]
				blocked, ok, err := s.WorkerExecution(t.Context(), original.Intent.ID)
				if err != nil || !ok || !reflect.DeepEqual(blocked, original) {
					t.Fatal("bulk adoption changed the blocked recovery", err)
				}
				workerRequeueFreshCheck(t, s, original.Intent)
				if n, err := s.RequeueWorkerExecutions(t.Context(), time.Now().UTC(), 100); err != nil || n != 1 {
					t.Fatal("fresh check did not release only the original recovery", n, err)
				}
				adopted, ok, err := s.WorkerExecution(t.Context(), original.Intent.ID)
				if err != nil || !ok || !reflect.DeepEqual(adopted.Intent, original.Intent) || adopted.CommittedIndex != original.CommittedIndex {
					t.Fatal("recovery adoption changed the original intent or commit position", err)
				}
			}
			job, ok, err := s.JobType(t.Context(), "queue-check")
			if err != nil || !ok {
				t.Fatal("recovered type", err)
			}
			want = append(want, commitWorkerQueueItem(t, s, job, "a-newest-after-restart"))
			request := saved.Request
			request.ClientSessionID = uuid.NewString()
			request.Capacity, request.Limit = 100, 100
			response, err := s.CommitWorkerPoll(t.Context(), saved.Authority, request)
			if err != nil || len(response.Offers) != len(want) {
				t.Fatal("recovered queue assignment", err, len(response.Offers), len(want))
			}
			for i, record := range want {
				if response.Offers[i].ExecutionID != record.Intent.ID {
					t.Fatalf("restart reordered item %d: got %s, want %s", i, response.Offers[i].ExecutionID, record.Intent.ID)
				}
			}
			if len(saved.Response.Offers) > 0 && !saved.Started {
				oldStart, err := s.CommitWorkerStart(t.Context(), saved.Authority, workerStartFromOffer(saved.Response))
				if err != nil || oldStart.Disposition != "rejected" {
					t.Fatal("retired offer became executable", err, oldStart.Disposition)
				}
			}
		})
	}
}
