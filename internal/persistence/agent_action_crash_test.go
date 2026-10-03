package persistence

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestAgentReceiptProcessHelper(t *testing.T) {
	stage := os.Getenv("CPRA_AGENT_RECEIPT_CRASH")
	if stage == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = os.Getenv("CPRA_AGENT_RECEIPT_DIRECTORY")
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	m, a, h := agentActionFixture(t, s, "intervention")
	if stage != "started" {
		if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 1, "TASK_STATE_SUBMITTED")); err != nil {
			t.Fatal(err)
		}
	}
	switch stage {
	case "snapshot":
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
		if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 2, "TASK_STATE_WORKING")); err != nil {
			t.Fatal(err)
		}
	case "outcome":
		if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 2, "TASK_STATE_COMPLETED")); err != nil {
			t.Fatal(err)
		}
		submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "success", At: time.Now().UTC()})
	case "finished":
		if err := h.Finish(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(struct{ Monitor, Action string }{m.ID, a.ID})
	fmt.Println("AGENT_RECEIPT_READY " + string(raw))
	select {}
}

func TestAgentReceiptSurvivesProcessKill(t *testing.T) {
	for _, stage := range []string{"started", "receipt", "snapshot", "outcome", "finished"} {
		t.Run(stage, func(t *testing.T) {
			c := testConfig(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentReceiptProcessHelper$")
			child.Env = append(os.Environ(), "CPRA_AGENT_RECEIPT_CRASH="+stage, "CPRA_AGENT_RECEIPT_DIRECTORY="+c.Storage.Directory)
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
					if line := scan.Text(); strings.HasPrefix(line, "AGENT_RECEIPT_READY ") {
						ready <- strings.TrimPrefix(line, "AGENT_RECEIPT_READY ")
						return
					}
				}
				ready <- ""
			}()
			var identity struct{ Monitor, Action string }
			select {
			case raw := <-ready:
				if err := json.Unmarshal([]byte(raw), &identity); err != nil {
					t.Fatal("child did not reach requested durable boundary", err)
				}
			case <-ctx.Done():
				t.Fatal("child did not reach requested durable boundary", ctx.Err())
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			s, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a, ok, err := s.Action(identity.Action)
			if err != nil || !ok || a.ExecutorKind != "agent" {
				t.Fatal("lost original agent action", err)
			}
			want := Unknown
			if stage == "outcome" {
				want = Succeeded
			}
			if a.State != want || a.ExecutorFenced != (stage == "outcome") || a.Held != (stage != "outcome") {
				t.Fatal("restart repeated or incorrectly fenced agent action", a)
			}
			if stage == "started" {
				if a.AgentReceipt != nil {
					t.Fatal("restart invented receipt for lost response")
				}
			} else if a.AgentReceipt == nil || a.AgentReceipt.TaskID != "task-123" || a.AgentReceipt.ContextID != "context-456" {
				t.Fatal("restart lost task correlation")
			}
			if stage == "snapshot" && (a.AgentReceipt.Sequence != 2 || a.AgentReceipt.State != "TASK_STATE_WORKING") {
				t.Fatal("snapshot lost later committed observation")
			}
			claim, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: identity.Monitor, Revision: a.Revision, ActionID: a.ID, Driver: "a2a", At: time.Now().UTC()})
			if err == nil {
				t.Fatal("restart granted the original action again")
			}
			if claim != nil {
				if err := claim.Finish(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			page, err := s.History().Page(identity.Monitor, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			observations := 0
			seen := map[string]bool{}
			for _, event := range page.Events {
				if seen[event.ID] {
					t.Fatal("replay duplicated event identity")
				}
				seen[event.ID] = true
				if event.AgentReceipt != nil {
					observations++
				}
			}
			wantObservations := 1
			if stage == "started" {
				wantObservations = 0
			} else if stage == "snapshot" || stage == "outcome" {
				wantObservations = 2
			}
			if observations != wantObservations {
				t.Fatal("history lost or duplicated observations", observations, wantObservations)
			}
		})
	}
}
