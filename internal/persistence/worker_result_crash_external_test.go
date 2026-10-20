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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

type workerResultCrashState struct {
	Authority WorkerAuthority
	Start     WorkerStartRequest
	Input     WorkerResultInput
	Record    WorkerExecutionRecord
	Monitor   Monitor
	Evidence  WorkerEvidenceInput
	SLO       *workerSLOImage
}

func TestWorkerResultProcessCrashHelper(t *testing.T) {
	phase := os.Getenv("CPRA_WORKER_RESULT_CRASH_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = os.Getenv("CPRA_WORKER_RESULT_CRASH_DIRECTORY")
	store, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	category := os.Getenv("CPRA_WORKER_RESULT_CRASH_CATEGORY")
	var in WorkerExecutionIntent
	var job JobTypeState
	if category == "inline-notification" {
		category = "notification"
		in, job = inlineNotificationFixture(t, store)
	} else {
		in, job = workerExecutionFixture(t, store, category)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	admin := openAuthenticationAdmin(t, cfg)
	worker := workerPrincipal("result-crash-worker")
	worker.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID,
		Version: job.Current.Version, Category: category, ResourceKind: in.Source.Kind, ResourceIDs: []string{in.Source.ID}}}
	commitWorker(t, admin, workerCommand(t, admin, "bootstrap", worker))
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(phase, "retire-") {
		in.ID, err = store.AllocateWorkerCheckID(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(store.nodeID), []byte("private-crash-parameters"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if category == "recovery" {
		// Restarted queued recovery needs a new health observation before any
		// worker may start the retained action.
		m, _ := monitorWithActionHistory(t, store, in.MonitorID)
		at := time.Now().UTC()
		submit(t, store, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: &in.Guard,
			Generation: m.Generation + 1, At: at, ExecutionStart: at, Outcome: "failure"})
	}
	if _, err := store.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	identity, err := store.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	poll, err := store.CommitWorkerPoll(t.Context(), authority, WorkerPollRequest{ServerID: identity.ServerID, WorkerID: worker.ID,
		ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1,
		Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: category}}})
	if err != nil || len(poll.Offers) != 1 {
		t.Fatal("offer", err)
	}
	if strings.HasPrefix(phase, "requeue-") {
		workerRequeueCrashBoundary(t, store, authority, poll, in, phase)
		return
	}
	start := workerStartFromOffer(poll)
	grant, err := store.CommitWorkerStart(t.Context(), authority, start)
	if err != nil || grant.Disposition != "granted" {
		t.Fatal("grant", err)
	}
	payload, err := catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: store.nodeID, Kind: "WorkerExecution", ID: in.ID,
		UID: worker.UID, Revision: in.Revision, Purpose: "worker-result-crash-test"}, []byte("private-crash-result-canary"))
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category]
	withEvidence := strings.Contains(phase, "evidence")
	if withEvidence {
		status = "unknown"
		if category == "check" {
			status = "noData"
		}
	}
	input := WorkerResultInput{ServerID: identity.ServerID, ExecutionID: in.ID, ExecutionRevision: in.Revision,
		GrantID: grant.GrantID, Status: status, Digest: authenticationVerifier("original-result"), Payload: payload}
	if phase == "heartbeat" || withEvidence {
		response, err := store.CommitWorkerHeartbeat(t.Context(), authority, heartbeatFromResult(start, input))
		if err != nil || !response.Accepted {
			t.Fatal("heartbeat boundary", err)
		}
	}
	finalizationPhase := strings.Contains(phase, "finalize")
	if finalizationPhase && phase != "before-finalize" {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = Open(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if count, err := store.FinalizeWorkerExecutions(t.Context(), time.Now().UTC(), 1); err != nil || count != 1 {
			t.Fatal("finalization after owner restart", count, err)
		}
	}
	if phase != "before-result" && phase != "heartbeat" && !finalizationPhase {
		if phase == "before-observation" {
			commitWorkerResultWithoutObservation(t, store, authority, input)
		} else if _, err := store.CommitWorkerResult(t.Context(), authority, input); err != nil {
			t.Fatal(err)
		}
	}
	if strings.HasPrefix(phase, "retire-") {
		workerCheckRetirementCrashBoundary(t, store, authority, start, input, phase)
		return
	}
	var evidence WorkerEvidenceInput
	if withEvidence {
		r, _, err := store.WorkerExecution(t.Context(), in.ID)
		if err != nil {
			t.Fatal(err)
		}
		evidence = WorkerEvidenceInput{ServerID: identity.ServerID, ExecutionID: in.ID, ExecutionRevision: in.Revision,
			GrantID: grant.GrantID, OriginalReceiptID: r.Result.Receipt.ReceiptID, EvidenceID: uuid.NewString(), Digest: authenticationVerifier("original-evidence"), Payload: payload.Clone()}
		if category != "check" {
			m, _ := monitorWithActionHistory(t, store, in.MonitorID)
			mustResult(t, store, reviewCommand(m, m.Actions[in.ActionID], uuid.NewString(), "inconclusive", time.Now().UTC()))
		}
		if phase != "before-evidence" {
			if _, err := store.CommitWorkerEvidence(t.Context(), authority, evidence); err != nil {
				t.Fatal(err)
			}
		}
	}
	if phase == "snapshot" || phase == "observation-snapshot" || phase == "evidence-snapshot" || phase == "finalize-snapshot" {
		if err := store.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	record, ok, err := store.WorkerExecution(t.Context(), in.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	monitor, _ := monitorWithActionHistory(t, store, in.MonitorID)
	raw, err := json.Marshal(workerResultCrashState{Authority: authority, Start: start, Input: input, Record: record, Monitor: monitor, Evidence: evidence, SLO: store.fsm.image.WorkerSLO})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CPRA_WORKER_RESULT_CRASH_STATE"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("WORKER_RESULT_BOUNDARY")
	select {}
}

func TestWorkerSLOSurvivesProcessKill(t *testing.T) {
	workerResultCrashCases(t, []string{"before-observation", "observation-snapshot"})
}

func TestWorkerResultSurvivesProcessKill(t *testing.T) {
	workerResultCrashCases(t, []string{"before-result", "after-result", "snapshot"})
}

func TestWorkerLifecycleSurvivesProcessKill(t *testing.T) {
	workerResultCrashCases(t, []string{"heartbeat", "before-evidence", "evidence", "evidence-snapshot"})
}

func TestWorkerFinalizationSurvivesProcessKill(t *testing.T) {
	workerResultCrashCases(t, []string{"before-finalize", "finalize", "finalize-snapshot"})
}

func workerResultCrashCases(t *testing.T, phases []string) {
	t.Helper()
	for _, category := range []string{"check", "recovery", "notification", "inline-notification"} {
		for _, phase := range phases {
			t.Run(category+"/"+phase, func(t *testing.T) {
				cfg := testConfig(t)
				statePath := filepath.Join(t.TempDir(), "boundary.json")
				cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerResultProcessCrashHelper$")
				cmd.Env = append(os.Environ(), "CPRA_WORKER_RESULT_CRASH_PHASE="+phase,
					"CPRA_WORKER_RESULT_CRASH_CATEGORY="+category, "CPRA_WORKER_RESULT_CRASH_DIRECTORY="+cfg.Storage.Directory,
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
					scan := bufio.NewScanner(out)
					for scan.Scan() {
						if scan.Text() == "WORKER_RESULT_BOUNDARY" {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case ok := <-ready:
					if !ok {
						t.Fatal("child exited before result boundary")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("child did not reach result boundary")
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
				store, err := Open(t.Context(), cfg)
				if err != nil {
					t.Fatal("restart", err)
				}
				defer store.Close()
				retained, ok, err := store.WorkerExecution(t.Context(), saved.Input.ExecutionID)
				if err != nil || !ok || !reflect.DeepEqual(saved.Record, retained) {
					t.Fatal("crash changed retained execution", err)
				}
				store.fsm.mu.RLock()
				sameSLO := reflect.DeepEqual(saved.SLO, store.fsm.image.WorkerSLO)
				store.fsm.mu.RUnlock()
				if !sameSLO {
					t.Fatal("crash changed retained SLO aggregate")
				}
				if strings.Contains(phase, "finalize") {
					if _, err := store.FinalizeWorkerExecutions(t.Context(), time.Now().UTC(), 1); err != nil {
						t.Fatal("recover unfinished finalization", err)
					}
				}
				if phase == "heartbeat" {
					h, err := store.CommitWorkerHeartbeat(t.Context(), saved.Authority, heartbeatFromResult(saved.Start, saved.Input))
					if err != nil || h.Accepted {
						t.Fatal("restarted owner continued original grant", err)
					}
				}
				first, err := store.CommitWorkerResult(t.Context(), saved.Authority, saved.Input)
				if err != nil {
					t.Fatal("original delivery after crash", err)
				}
				want := "accepted"
				if phase == "before-result" || phase == "heartbeat" || strings.Contains(phase, "evidence") || strings.Contains(phase, "finalize") {
					want = "unknown"
					if category == "check" {
						want = "finalized"
					}
				}
				if saved.Record.Result != nil && first != saved.Record.Result.Receipt {
					t.Fatal("crash lost original committed receipt")
				}
				if saved.Record.Finalization != nil && first != saved.Record.Finalization.Receipt {
					t.Fatal("crash lost original finalization receipt")
				}
				if first.Disposition != want {
					t.Fatal("crash disposition", first.Disposition, want)
				}
				again, err := store.CommitWorkerResult(t.Context(), saved.Authority, saved.Input)
				if err != nil || again != first {
					t.Fatal("crash replay changed receipt", err)
				}
				if strings.Contains(phase, "evidence") {
					evidenceReceipt, err := store.CommitWorkerEvidence(t.Context(), saved.Authority, saved.Evidence)
					if err != nil {
						t.Fatal("evidence retry after crash", err)
					}
					if len(saved.Record.Evidence) != 0 && saved.Record.Evidence[0].Receipt != evidenceReceipt {
						t.Fatal("crash replaced committed evidence receipt")
					}
					repeat, err := store.CommitWorkerEvidence(t.Context(), saved.Authority, saved.Evidence)
					if err != nil || repeat != evidenceReceipt {
						t.Fatal("evidence duplicated after restart", err)
					}
					r, _, _ := store.WorkerExecution(t.Context(), saved.Input.ExecutionID)
					if len(r.Evidence) != 1 || r.Result.Receipt != first {
						t.Fatal("evidence changed original outcome")
					}
					m, _ := monitorWithActionHistory(t, store, saved.Record.Intent.MonitorID)
					if category != "check" && !reflect.DeepEqual(m.Actions[saved.Record.Intent.ActionID].Review, saved.Monitor.Actions[saved.Record.Intent.ActionID].Review) {
						t.Fatal("evidence/restart changed operator review")
					}
				}
				if category == "check" {
					report := workerSLOReport(t, store, time.Now())
					wantSamples := uint64(0)
					if saved.Record.Observation != nil {
						wantSamples = 1
					}
					if report.Samples != wantSamples || report.Expected != 1 || report.Missed != 1-wantSamples {
						t.Fatal("restart invented timing or lost an obligation", report)
					}
				}
				monitor, _ := monitorWithActionHistory(t, store, saved.Record.Intent.MonitorID)
				requireWorkerProjectionView(t, store, monitor.ID)
				if monitor.TotalChecks != saved.Monitor.TotalChecks || monitor.LastOutcome != saved.Monitor.LastOutcome {
					t.Fatal("restart or repeated outcome invented an observation")
				}
				if category != "check" && phase == "before-result" && monitor.Actions[saved.Record.Intent.ActionID].State != Unknown {
					t.Fatal("uncertain remote action was not held")
				}
				for _, mode := range []string{"begin", "reconcile"} {
					saved.Start.Mode = mode
					result, err := store.CommitWorkerStart(t.Context(), saved.Authority, saved.Start)
					if err != nil || result.Disposition == "granted" || result.ReceiptID != first.ReceiptID {
						t.Fatal("crash made an original grant executable again", err)
					}
				}
				if err := store.Snapshot(); err != nil {
					t.Fatal("recovered state cannot snapshot", err)
				}
			})
		}
	}
}
