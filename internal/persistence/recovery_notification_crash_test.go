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

func TestRecoveryNotificationProcessHelper(t *testing.T) {
	stage := os.Getenv("CPRA_RECOVERY_NOTIFICATION_CRASH")
	if stage == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = os.Getenv("CPRA_RECOVERY_NOTIFICATION_DIRECTORY")
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	m := testMonitor()
	m.Policy.Intervention, m.Policy.RecoveryDriver = true, "docker"
	m.Policy.NotificationTriggers = map[string]string{"red": NotifyRecoveryExhausted}
	m.Policy.Endpoints = map[string]int{"red": 2}
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})
	m, _ = s.Get(m.ID)
	a := interventionAction(t, m)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at})
	if stage != "before_result" {
		submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at, Outcome: "failure"})
		m, _ = s.Get(m.ID)
		rows := recoveryNotificationActions(m)
		if len(rows) != 2 {
			t.Fatal("committed rejection did not create endpoint intents")
		}
		if stage == "snapshot" {
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "started" || stage == "accepted" || stage == "snapshot" {
			row := rows[0]
			submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: row.ID, At: at})
			if stage == "accepted" {
				submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: row.ID, At: at, Outcome: "success"})
			}
		}
	}
	raw, _ := json.Marshal(struct{ Monitor, Recovery string }{m.ID, a.ID})
	fmt.Println("RECOVERY_NOTIFICATION_READY " + string(raw))
	select {}
}

func TestRecoveryNotificationSurvivesProcessKill(t *testing.T) {
	for _, stage := range []string{"before_result", "queued", "started", "accepted", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			c := testConfig(t)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryNotificationProcessHelper$")
			child.Env = append(os.Environ(), "CPRA_RECOVERY_NOTIFICATION_CRASH="+stage, "CPRA_RECOVERY_NOTIFICATION_DIRECTORY="+c.Storage.Directory)
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
					if line := scan.Text(); strings.HasPrefix(line, "RECOVERY_NOTIFICATION_READY ") {
						ready <- strings.TrimPrefix(line, "RECOVERY_NOTIFICATION_READY ")
						return
					}
				}
				ready <- ""
			}()
			var ids struct{ Monitor, Recovery string }
			select {
			case raw := <-ready:
				if err := json.Unmarshal([]byte(raw), &ids); err != nil {
					t.Fatal("child did not reach the requested commit boundary", err)
				}
			case <-ctx.Done():
				t.Fatal("child did not reach the requested commit boundary", ctx.Err())
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
			m, found := monitorWithActionHistory(t, s, ids.Monitor)
			if !found {
				t.Fatal("original monitor lost")
			}
			rows := recoveryNotificationActions(m)
			wantCount := 2
			if stage == "before_result" {
				wantCount = 0
				if m.Actions[ids.Recovery].State != Unknown {
					t.Fatal("restart invented a confirmed recovery rejection")
				}
			}
			if len(rows) != wantCount {
				t.Fatal("restart lost or invented notification intents", len(rows), wantCount)
			}
			counts := map[ActionState]int{}
			for _, a := range rows {
				counts[a.State]++
				if a.RecoveryFailure == nil || a.RecoveryFailure.ActionID != ids.Recovery || a.RecoveryFailure.Driver != "docker" ||
					a.RecoveryFailure.Trigger != NotifyRecoveryExhausted || a.IncidentID != m.Actions[ids.Recovery].IncidentID {
					t.Fatal("restart lost the original confirmed-failure context")
				}
				if a.State != Queued {
					r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC()})[0]
					if r.Allowed {
						t.Fatal("restart repeated accepted or uncertain delivery")
					}
				}
			}
			switch stage {
			case "queued":
				if counts[Queued] != 2 {
					t.Fatal("safe queued work did not survive")
				}
			case "started", "snapshot":
				if counts[Queued] != 1 || counts[Unknown] != 1 {
					t.Fatal("interrupted endpoint was not held independently", counts)
				}
			case "accepted":
				if counts[Queued] != 1 || counts[Succeeded] != 1 {
					t.Fatal("confirmed endpoint success was lost", counts)
				}
			}
			page, err := s.History().Page(m.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			seen, intents := map[string]bool{}, 0
			for _, event := range page.Events {
				if seen[event.ID] {
					t.Fatal("history replay duplicated an event")
				}
				seen[event.ID] = true
				if event.Kind == "code" && event.Type == "action_queued" {
					intents++
					if event.RecoveryFailure == nil || event.RecoveryFailure.ActionID != ids.Recovery {
						t.Fatal("history omitted original escalation policy and cause")
					}
				}
			}
			if intents != wantCount {
				t.Fatal("history lost or duplicated notification intents")
			}
		})
	}
}
