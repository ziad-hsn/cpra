package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestAgentObservationProcessHelper(t *testing.T) {
	stage := os.Getenv("CPRA_AGENT_OBSERVATION_CRASH")
	if stage == "" {
		return
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = os.Getenv("CPRA_AGENT_OBSERVATION_DIRECTORY")
	store, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	h, action := agentObservationFixture(t, store, "intervention")
	if stage == "snapshot" || stage == "observed" {
		if err := store.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "observed" {
		if err := h.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
		action, _, _ = store.Action(action.ID)
		if err := store.RecordAgentObservation(t.Context(), action, nextAgentObservation(action, "TASK_STATE_COMPLETED")); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(struct{ Monitor, Action string }{action.MonitorID, action.ID})
	fmt.Println("AGENT_OBSERVATION_READY " + string(raw))
	select {}
}

func TestAgentObservationSurvivesProcessKill(t *testing.T) {
	for _, stage := range []string{"working", "snapshot", "observed"} {
		t.Run(stage, func(t *testing.T) {
			config := testConfig(t)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentObservationProcessHelper$")
			child.Env = append(os.Environ(), "CPRA_AGENT_OBSERVATION_CRASH="+stage, "CPRA_AGENT_OBSERVATION_DIRECTORY="+config.Storage.Directory)
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			ready := make(chan string, 1)
			go func() {
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if line := scan.Text(); strings.HasPrefix(line, "AGENT_OBSERVATION_READY ") {
						ready <- strings.TrimPrefix(line, "AGENT_OBSERVATION_READY ")
						return
					}
				}
				ready <- ""
			}()
			var ids struct{ Monitor, Action string }
			select {
			case raw := <-ready:
				if err := json.Unmarshal([]byte(raw), &ids); err != nil {
					t.Fatal("child did not commit requested boundary", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			store, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			action, found, err := store.Action(ids.Action)
			if err != nil || !found || action.AgentConnection == nil || action.State != Unknown || !action.Held || action.LocalExecutorActive {
				t.Fatal("restart lost original connection or held action", err)
			}
			plain, err := agentObservationSealer(t).Open(t.Context(), AgentConnectionBinding(store.Status().NodeID, ids.Monitor, action.Action), action.AgentConnection.Payload)
			defer clear(plain)
			if err != nil || string(plain) != agentObservationSecret {
				t.Fatal("restart lost authenticated original connection", err)
			}
			if stage == "observed" {
				if !action.ExecutorFenced || action.AgentReceipt.Sequence != 2 || action.AgentReceipt.State != "TASK_STATE_COMPLETED" {
					t.Fatal("snapshot replay lost the later terminal observation")
				}
			} else {
				if action.ExecutorFenced || action.AgentReceipt.Sequence != 1 {
					t.Fatal("restart invented remote completion")
				}
				observed := nextAgentObservation(action, "TASK_STATE_COMPLETED")
				stale := Command{Kind: "agent_observation", MonitorID: ids.Monitor, Revision: action.Revision, ActionID: ids.Action,
					ExecutorSession: action.ExecutorSession, At: observed.ObservedAt,
					AgentObservation: &AgentObservationCommand{ExpectedRevision: action.ReviewRevision, ExpectedSequence: action.AgentReceipt.Sequence, Receipt: observed}}
				if result := submit(t, store, stale)[0]; !errors.Is(result.Err, ErrAgentReceiptConflict) {
					t.Fatal("old process retained observation authority", result.Err)
				}
				receipt := agentReceiptCommand(action.Action, 2, "TASK_STATE_COMPLETED")
				old := Command{Kind: "agent_receipt", MonitorID: ids.Monitor, Revision: action.Revision, ActionID: ids.Action,
					ExecutorSession: action.ExecutorSession, At: receipt.Receipt.ObservedAt, AgentReceipt: &receipt}
				if result := submit(t, store, old)[0]; !errors.Is(result.Err, ErrAgentReceiptConflict) {
					t.Fatal("old invocation retained post-restart authority", result.Err)
				}
				if err := store.RecordAgentObservation(t.Context(), action, nextAgentObservation(action, "TASK_STATE_COMPLETED")); err != nil {
					t.Fatal("new read authority rejected the original task", err)
				}
			}
			retained, _, _ := store.Action(ids.Action)
			if retained.State != Unknown || !retained.Held || retained.Review != nil {
				t.Fatal("replay or observation resolved the action automatically")
			}
			page, err := store.History().Page(ids.Monitor, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			observations := 0
			for _, event := range page.Events {
				if event.AgentReceipt != nil {
					observations++
				}
			}
			if observations != 2 {
				t.Fatal("log replay duplicated or lost observations", observations)
			}
			if bytes.Contains(captureSnapshotBytes(t, store.fsm), []byte("original-agent-private-token")) {
				t.Fatal("snapshot leaked original credentials")
			}
		})
	}
}
