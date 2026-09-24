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
	planCrashDirectoryEnv = "CPRA_PLAN_STAGING_CRASH_DIRECTORY"
	planCrashScenarioEnv  = "CPRA_PLAN_STAGING_CRASH_SCENARIO"
	planCrashReadyPrefix  = "CPRA_PLAN_STAGING_COMMITTED "
)

// Only safe plan metadata crosses the process pipe. In particular, this does
// not serialize the protected CollectionState inventory envelope. The parent
// retains the original artifact parts; it never regenerates a new plan from a
// catalog observed after the crash.
type planCrashReport struct {
	OperationID string                         `json:"operation_id"`
	UploadID    string                         `json:"upload_id"`
	Phase       string                         `json:"phase"`
	ActivityAt  time.Time                      `json:"activity_at"`
	ExpiresAt   time.Time                      `json:"expires_at"`
	Plan        CollectionPlanState            `json:"plan"`
	Parts       []CollectionPlanLedgerFragment `json:"parts"`
}

func TestCollectionPlanProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(planCrashDirectoryEnv)
	if directory == "" {
		return
	}
	scenario := os.Getenv(planCrashScenarioEnv)
	if scenario != "v3-partial" && scenario != "v5-partial" && scenario != "v5-finalized" {
		t.Fatal("unknown process fixture scenario")
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally no graceful Close: the parent forcibly terminates this
	// process after the durable command acknowledgement below.
	head := collectionFill(t, s, 4)
	if scenario == "v3-partial" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("input-only snapshot", err)
		}
		s.fsm.mu.RLock()
		version := s.fsm.image.Version
		s.fsm.mu.RUnlock()
		if version != CollectionFormatVersion {
			t.Fatal("fixture did not establish a format-3 snapshot")
		}
	}
	begin, parts := planApplyArtifact(t, s, head)
	head = planApplyBegin(t, s, head, begin)
	head = planApplyAll(t, s, head, parts[:2])
	if scenario != "v3-partial" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("partial plan snapshot", err)
		}
	}
	// Leave a valid row open at the committed prefix boundary. This checks
	// reconstruction of codec state, beyond merely restoring complete rows.
	head = planApplyAll(t, s, head, parts[2:4])
	if scenario == "v5-finalized" {
		head = planApplyAll(t, s, head, parts[4:])
		proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
		if err != nil {
			t.Fatal("verify before crash", err)
		}
		result := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID,
			UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Second))
		if result.Err != nil || result.Collection == nil || result.Collection.Phase != "validated" {
			t.Fatal("finalize before crash", result.Err)
		}
		head = result.Collection.Clone()
	}
	report := planCrashReport{OperationID: head.ID, UploadID: head.UploadID, Phase: head.Phase,
		ActivityAt: head.ActivityAt, ExpiresAt: head.ExpiresAt, Plan: *head.Plan, Parts: parts}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 1<<20 {
		t.Fatal("invalid bounded crash fixture report", err)
	}
	fmt.Println(planCrashReadyPrefix + string(raw))
	select {}
}

// Collect bounded child diagnostics without blocking the process on a full
// pipe. The buffer is read only after Wait has joined os/exec's copy goroutine.
type planCrashDiagnostics struct{ bytes.Buffer }

func (b *planCrashDiagnostics) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (32 << 10) - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(data[:min(remaining, n)])
	}
	return n, nil
}

func planCrashChild(t *testing.T, directory, scenario string) planCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionPlanProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), planCrashDirectoryEnv+"="+directory, planCrashScenarioEnv+"="+scenario)
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
		scanner.Buffer(make([]byte, 4096), (1<<20)+len(planCrashReadyPrefix)+1)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), planCrashReadyPrefix) {
				ready <- strings.TrimPrefix(scanner.Text(), planCrashReadyPrefix)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("child did not commit before startup deadline")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("child exited before committed boundary: %s", diagnostics.String())
	}
	var report planCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.OperationID == "" || len(report.Parts) == 0 {
		t.Fatal("malformed child commitment report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal("force termination", err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("child was not forcibly terminated")
	}
	return report
}

func TestCollectionPlanCommittedStateSurvivesProcessKill(t *testing.T) {
	for _, scenario := range []string{"v3-partial", "v5-partial", "v5-finalized"} {
		t.Run(scenario, func(t *testing.T) {
			config := testConfig(t)
			report := planCrashChild(t, config.Storage.Directory, scenario)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal("open after forced termination", err)
			}
			defer s.Close()
			head, exists, err := s.CollectionGet(report.OperationID)
			if err != nil || !exists || head.Plan == nil || head.UploadID != report.UploadID || head.Phase != report.Phase ||
				!reflect.DeepEqual(*head.Plan, report.Plan) || !head.ActivityAt.Equal(report.ActivityAt) || !head.ExpiresAt.Equal(report.ExpiresAt) {
				t.Fatal("restart changed original committed plan", err)
			}
			prefix, err := s.fsm.collections.PlanPage(head.ID, 0, 256)
			if err != nil || !reflect.DeepEqual(prefix, report.Parts[:head.Plan.UploadedFragments]) {
				t.Fatal("restart regenerated or changed committed plan fragments", err)
			}
			if scenario != "v5-finalized" {
				if head.Plan.UploadedFragments != 4 {
					t.Fatal("fixture did not stop at its declared partial boundary")
				}
				if proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); err == nil || proof != (CollectionPlanVerification{}) {
					t.Fatal("partial artifact became a complete verification result")
				}
				// Simulate retrying the last acknowledged part before continuing.
				// No committed part or artifact identity may be duplicated.
				retry := planApplyAppend(t, s, head, report.Parts[head.Plan.UploadedFragments-1])
				if retry.Err != nil || retry.Collection == nil || *retry.Collection.Plan != *head.Plan {
					t.Fatal("original-part retry changed plan identity", retry.Err)
				}
				head = retry.Collection.Clone()
				head = planApplyAll(t, s, head, report.Parts[head.Plan.UploadedFragments:])
			}
			proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
			if err != nil || proof.Descriptor != report.Plan.Descriptor {
				t.Fatal("original complete artifact failed after restart", err)
			}
			before := head.Clone()
			result := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID,
				UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Second))
			if result.Err != nil || result.Collection == nil || result.Collection.Phase != "validated" ||
				result.Collection.Plan.Descriptor != report.Plan.Descriptor {
				t.Fatal("same artifact could not finalize", result.Err)
			}
			if scenario == "v5-finalized" && !reflect.DeepEqual(*result.Collection, before) {
				t.Fatal("lost finalization response caused a replacement verdict or renewed deadline")
			}
			catalog, err := s.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.RLock()
			defer s.fsm.mu.RUnlock()
			if catalog.Len() != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 ||
				s.fsm.image.CatalogMutationSequence != 0 || s.fsm.image.Version != CollectionPlanFormatVersion {
				t.Fatal("crash replay or finalization activated catalog resources")
			}
		})
	}
}
