package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	executionCrashDir   = "CPRA_EXECUTION_CRASH_DIRECTORY"
	executionCrashStage = "CPRA_EXECUTION_CRASH_STAGE"
	executionCrashReady = "CPRA_EXECUTION_COMMITTED "
)

type executionCrashReport struct {
	OperationID string                      `json:"operation_id"`
	PreparedID  string                      `json:"prepared_id"`
	ChildID     string                      `json:"child_id,omitempty"`
	Progress    CollectionExecutionProgress `json:"progress"`
}

// This process runs the actual single-node Raft Store and registered commands.
// Ciphertext candidates are synthetic driver-free catalog fixtures: no provider
// execution or management plaintext-to-candidate proof is claimed here.
func TestCollectionExecutionProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(executionCrashDir)
	if directory == "" {
		return
	}
	stage := os.Getenv(executionCrashStage)
	if stage != "format8-snapshot-execution-log" && stage != "prepared-snapshot" && stage != "accepted-snapshot" && stage != "accepted-snapshot-terminal-log" {
		t.Fatal("unknown execution crash stage")
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	admin, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	policy := authenticationBootstrap()
	policy.Principals[0].ExpiresAt = policy.At.Add(90 * 24 * time.Hour)
	if _, err := admin.CommitAuthentication(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no Close: the parent kills this owner at the IPC boundary.
	head, admission := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	if stage == "format8-snapshot-execution-log" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	begin := CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: *admission.ActivationAuthority, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
	at := head.Activation.At.Add(time.Second)
	if r := executeStoreCommand(t, s, begin, at); r.Err != nil {
		t.Fatal(r.Err)
	}
	candidate := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at.Add(time.Second))
	r := executeStoreCommand(t, s, candidate, candidate.Prepared.At)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	report := executionCrashReport{OperationID: head.ID, PreparedID: candidate.Prepared.ID, Progress: r.Collection.Execution.Clone()}
	if stage != "prepared-snapshot" {
		r = executeStoreCommand(t, s, executeDecision(candidate), at.Add(2*time.Second))
		if r.Err != nil || r.Operation == nil {
			t.Fatal("child acceptance", r.Err)
		}
		report.ChildID, report.Progress = r.Operation.ID, r.Collection.Execution.Clone()
	}
	if stage != "format8-snapshot-execution-log" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("execution snapshot", err)
		}
	}
	if stage == "accepted-snapshot-terminal-log" {
		u := OperationUpdate{ID: r.Operation.ID, Key: r.Operation.Key, UID: r.Operation.UID, Revision: r.Operation.NewVersion, Applied: true}
		results, err := s.Submit(context.Background(), []Command{{Kind: "operation", Operation: &u, At: at.Add(3 * time.Second)}})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("terminal log", err)
		}
		head, ok, err := s.CollectionGet(head.ID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		report.Progress = head.Execution.Clone()
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 16<<10 || bytes.Contains(raw, []byte(authenticationToken)) || bytes.Contains(raw, []byte("ciphertext")) {
		t.Fatal("unsafe execution crash IPC", err)
	}
	fmt.Println(executionCrashReady + string(raw))
	select {}
}

func executionCrashChild(t *testing.T, directory, stage string) executionCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionExecutionProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), executionCrashDir+"="+directory, executionCrashStage+"="+stage)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics planCrashDiagnostics
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 32<<10)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), executionCrashReady) {
				ready <- strings.TrimPrefix(scanner.Text(), executionCrashReady)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("execution child did not reach durable boundary")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("child exited before execution boundary: %s", diagnostics.String())
	}
	var report executionCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.OperationID == "" || report.PreparedID == "" || report.Progress.validate() != nil {
		t.Fatal("invalid execution crash IPC", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("owner was not forcibly terminated")
	}
	return report
}

func TestCollectionExecutionCommittedProgressSurvivesProcessKill(t *testing.T) {
	for _, stage := range []string{"format8-snapshot-execution-log", "prepared-snapshot", "accepted-snapshot", "accepted-snapshot-terminal-log"} {
		t.Run(stage, func(t *testing.T) {
			config := testConfig(t)
			report := executionCrashChild(t, config.Storage.Directory, stage)
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("offline snapshot/log inventory verification", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal("restart", err)
			}
			defer s.Close()
			head, ok, err := s.CollectionGet(report.OperationID)
			if err != nil || !ok || head.Execution == nil || !reflect.DeepEqual(*head.Execution, report.Progress) || s.fsm.image.Version != CollectionExecutionFormatVersion {
				t.Fatal("restart changed committed execution evidence", err)
			}
			at := head.Execution.LastAt.Add(time.Second)
			authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			decision := CollectionExecuteCommand{Action: "decide", Binding: head.Execution.Binding, Authority: authority,
				CapabilitiesDigest: head.Activation.CapabilitiesDigest, Ordinal: 1, PreparedID: report.PreparedID}
			high := s.fsm.image.OperationHighWater
			if stage == "prepared-snapshot" {
				if len(s.fsm.image.Catalog) != 0 || len(s.fsm.collectionChildren) != 0 || head.Execution.Prepared == nil || head.Execution.Prepared.ID != report.PreparedID {
					t.Fatal("restart executed a merely prepared candidate")
				}
				r := executeStoreCommand(t, s, decision, at)
				if r.Err != nil || r.Operation == nil || s.fsm.image.OperationHighWater != high+1 {
					t.Fatal("safe prepared continuation failed", r.Err)
				}
				head, high = *r.Collection, s.fsm.image.OperationHighWater
			}
			r := executeStoreCommand(t, s, decision, at.Add(time.Second))
			if r.Err != nil || len(r.Events) != 0 || !reflect.DeepEqual(r.Collection.Execution, head.Execution) || s.fsm.image.OperationHighWater != high {
				t.Fatal("lost acceptance response replayed catalog operation", r.Err)
			}
			if stage == "accepted-snapshot-terminal-log" && (head.Execution.ChildApplied != 1 || len(s.fsm.collectionChildren) != 0) {
				t.Fatal("terminal log replay lost completed-child evidence")
			}
		})
	}
}
