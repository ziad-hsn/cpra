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
	executionRetirementCrashDirectory = "CPRA_RETIREMENT_CRASH_DIRECTORY"
	executionRetirementCrashOperation = "CPRA_RETIREMENT_CRASH_OPERATION"
	executionRetirementCrashReady     = "CPRA_RETIREMENT_COMMITTED "
)

// The parent creates the original result through registered Raft commands, then
// gives each child exclusive ownership of the stopped store. A child commits one
// bounded deletion and waits without closing storage until the parent kills it.
func TestCollectionExecutionRetirementProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(executionRetirementCrashDirectory)
	if directory == "" {
		return
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	head, found, err := s.CollectionGet(os.Getenv(executionRetirementCrashOperation))
	if err != nil || !found || head.ExecutionResult == nil {
		t.Fatal("missing original execution result", err)
	}
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	if head.ExecutionRetirement != nil {
		at = head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, executionRetireCommand(head), at))
	raw, err := json.Marshal(head.ExecutionRetirement)
	if err != nil || len(raw) > 32<<10 {
		t.Fatal("invalid retirement crash report", err)
	}
	fmt.Println(executionRetirementCrashReady + string(raw))
	select {}
}

func executionRetirementCrashChild(t *testing.T, directory, operation string) CollectionExecutionRetirementState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionExecutionRetirementProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), executionRetirementCrashDirectory+"="+directory, executionRetirementCrashOperation+"="+operation)
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
			if strings.HasPrefix(scanner.Text(), executionRetirementCrashReady) {
				ready <- strings.TrimPrefix(scanner.Text(), executionRetirementCrashReady)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("retirement process did not reach its committed boundary")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("retirement process exited before commit: %s", diagnostics.String())
	}
	var report CollectionExecutionRetirementState
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.Checkpoint == nil {
		t.Fatal("invalid committed retirement report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("retirement owner was not forcibly terminated")
	}
	if strings.Contains(diagnostics.String(), "WARNING: DATA RACE") {
		t.Fatal("retirement child reported a data race", diagnostics.String())
	}
	return report
}

func TestCollectionExecutionRetirementSurvivesProcessKill(t *testing.T) {
	s, original := executionRetirementFixture(t, true, 129)
	config := s.config
	// First recovery starts from format11 plus the first retirement log entry.
	// Second recovery starts from a format12 partial snapshot plus completion.
	for _, expected := range []uint64{128, 129} {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		report := executionRetirementCrashChild(t, config.Storage.Directory, original.ID)
		lock, err := LockOffline(config.Storage.Directory)
		if err != nil {
			t.Fatal("offline verification after forced termination", err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(context.Background(), config)
		if err != nil {
			t.Fatal("retirement restart", err)
		}
		reopened := s
		t.Cleanup(func() { _ = reopened.Close() })
		head, found, err := s.CollectionGet(original.ID)
		if err != nil || !found || !collectionExecutionRetirementsEqual(head.ExecutionRetirement, &report) || report.Checkpoint.Progress.Processed != expected {
			t.Fatal("restart changed committed retirement progress", expected, err)
		}
		if !reflect.DeepEqual(head.Execution, original.Execution) || !reflect.DeepEqual(head.ExecutionResult, original.ExecutionResult) || len(s.fsm.image.Operations) != 0 {
			t.Fatal("restart changed original evidence or recreated child operations")
		}
		stats, err := s.fsm.collections.ExecutionStats(head.ID)
		if err != nil || stats.Outcomes != 129-expected || stats.Terminals != 129-expected {
			t.Fatal("restart restored deleted rows or lost remaining pairs", err)
		}
		if s.fsm.collectionOutcomeCommitments[head.ID] != nil || s.fsm.collectionTerminalTrees[head.ID] != nil {
			t.Fatal("terminal retirement recovery installed executable caches")
		}
		view, status, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, report.UpdatedAt)
		if err != nil || status.State != "ready" {
			t.Fatal("restart lost retained result authority", status, err)
		}
		page, err := view.Page(t.Context(), 0, 100, report.UpdatedAt)
		if err != nil || len(page.Items) != 100 {
			t.Fatal("restart lost published original results", err)
		}
	}
}
