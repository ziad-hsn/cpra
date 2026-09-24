package persistence

import (
	"bufio"
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
	sourceRetirementCrashDirectory = "CPRA_SOURCE_RETIREMENT_CRASH_DIRECTORY"
	sourceRetirementCrashOperation = "CPRA_SOURCE_RETIREMENT_CRASH_OPERATION"
	sourceRetirementCrashStage     = "CPRA_SOURCE_RETIREMENT_CRASH_STAGE"
	sourceRetirementCrashReady     = "CPRA_SOURCE_RETIREMENT_COMMITTED "
)

// Only compact non-secret progress crosses the process boundary. The original
// input inventory is encrypted and is never part of the diagnostic report.
type sourceRetirementCrashReport struct {
	Cleanup CollectionCleanup
	Sources CollectionExecutionSourceRetirementState
	Present bool
}

func TestCollectionExecutionSourceRetirementProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(sourceRetirementCrashDirectory)
	if directory == "" {
		return
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	head, found, err := s.CollectionGet(os.Getenv(sourceRetirementCrashOperation))
	if err != nil || !found || head.ExecutionRetirement == nil || !head.ExecutionRetirement.complete(head) {
		t.Fatal("missing completed execution retirement", err)
	}
	at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	if head.ExecutionRetirement.Sources != nil {
		at = head.ExecutionRetirement.Sources.UpdatedAt.Add(time.Second)
	}
	stage := os.Getenv(sourceRetirementCrashStage)
	for step := 0; step < 10; step++ {
		head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at))
		present := sourceHeaderPresent(t, s, head.ID)
		reached := stage == "validation" && head.Validation.RemovedRows > 0 && head.Validation.RemovedRows < head.Validation.Uploaded ||
			stage == "plan" && head.Plan.RemovedFragments > 0 && head.Plan.RemovedFragments < head.Plan.UploadedFragments ||
			stage == "input" && head.RemovedRows > 0 && head.RemovedRows < head.Uploaded ||
			stage == "header" && !present
		if reached {
			fence := CollectionExecutionSourceRetirementFenceFor(head)
			report := sourceRetirementCrashReport{Cleanup: fence.Cleanup, Sources: *head.ExecutionRetirement.Sources, Present: present}
			raw, err := json.Marshal(report)
			if err != nil || len(raw) > 32<<10 {
				t.Fatal("invalid source retirement crash report", err)
			}
			fmt.Println(sourceRetirementCrashReady + string(raw))
			select {}
		}
		if !present {
			t.Fatal("source retirement skipped requested crash boundary", stage)
		}
		at = at.Add(time.Second)
	}
	t.Fatal("source retirement did not reach requested crash boundary", stage)
}

func sourceRetirementCrashChild(t *testing.T, directory, operation, stage string) sourceRetirementCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionExecutionSourceRetirementProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), sourceRetirementCrashDirectory+"="+directory, sourceRetirementCrashOperation+"="+operation, sourceRetirementCrashStage+"="+stage)
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
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), sourceRetirementCrashReady) {
				ready <- strings.TrimPrefix(scanner.Text(), sourceRetirementCrashReady)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("source retirement process did not reach its committed boundary", stage)
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("source retirement process exited before commit: %s", diagnostics.String())
	}
	var report sourceRetirementCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.Sources.validate() != nil {
		t.Fatal("invalid committed source retirement report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("source retirement owner was not forcibly terminated")
	}
	if strings.Contains(diagnostics.String(), "WARNING: DATA RACE") {
		t.Fatal("source retirement child reported a data race", diagnostics.String())
	}
	return report
}

func TestCollectionExecutionSourceRetirementSurvivesProcessKill(t *testing.T) {
	s, original := sourceRetirementFixture(t, true, 257)
	config := s.config
	for _, stage := range []string{"validation", "plan", "input", "header"} {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		report := sourceRetirementCrashChild(t, config.Storage.Directory, original.ID, stage)
		lock, err := LockOffline(config.Storage.Directory)
		if err != nil {
			t.Fatal("offline verification after forced source retirement termination", stage, err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(context.Background(), config)
		if err != nil {
			t.Fatal("source retirement restart", stage, err)
		}
		reopened := s
		t.Cleanup(func() { _ = reopened.Close() })
		if present := sourceHeaderPresent(t, s, original.ID); present != report.Present {
			t.Fatal("restart changed committed source retirement presence", stage)
		}
		if report.Present {
			head, found, err := s.CollectionGet(original.ID)
			if err != nil || !found || !reflect.DeepEqual(CollectionExecutionSourceRetirementFenceFor(head).Cleanup, report.Cleanup) || !reflect.DeepEqual(*head.ExecutionRetirement.Sources, report.Sources) {
				t.Fatal("restart changed committed source retirement progress", stage, err)
			}
			if !reflect.DeepEqual(head.Execution, original.Execution) || !reflect.DeepEqual(head.ExecutionResult, original.ExecutionResult) || !reflect.DeepEqual(head.Activation, original.Activation) {
				t.Fatal("restart rewrote original result or activation evidence", stage)
			}
		} else if used, err := s.fsm.collections.Bytes(); err != nil || used != 0 {
			t.Fatal("deleted source reappeared after process kill", used, err)
		}
		if len(s.fsm.image.Operations) != 0 || s.fsm.collectionOutcomeCommitments[original.ID] != nil || s.fsm.collectionTerminalTrees[original.ID] != nil {
			t.Fatal("terminal source recovery recreated child operations or executable caches")
		}
		view, status, err := s.CollectionExecutionResultView(t.Context(), original.ID, original.Actor, report.Sources.UpdatedAt)
		if err != nil || status.State != "ready" {
			t.Fatal("restart lost original result authority", stage, status, err)
		}
		page, err := view.Page(t.Context(), 0, 100, report.Sources.UpdatedAt)
		if err != nil || len(page.Items) != 100 {
			t.Fatal("restart lost retained results", stage, err)
		}
	}
}
