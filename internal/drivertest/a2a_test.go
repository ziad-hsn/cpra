package drivertest

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type agentObservation struct {
	Count    int    `json:"count"`
	RunID    string `json:"run_id"`
	Monitor  string `json:"monitor_id"`
	Action   string `json:"action_id"`
	Observed bool   `json:"observed"`
}

func agentVerificationConfig(t *testing.T, endpoint, kind string) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Monitor{ID: "designated-monitor", Name: "verification", Intervention: manifest.Intervention{
		Action: "a2a", Target: &manifest.InterventionTargetA2A{URL: endpoint, Token: "private-agent-token", AllowInsecureHTTP: true, TaskTimeout: 2 * time.Second, PollInterval: time.Second}}}
	m.Codes = manifest.Codes{"red": {Notify: "a2a", Config: &manifest.CodeNotificationA2A{
		URL: endpoint, Token: "private-agent-token", AllowInsecureHTTP: true, Timeout: 2 * time.Second}}}
	return Config{StateDirectory: filepath.Join(t.TempDir(), "campaign"), Manifest: manifest.Manifest{Monitors: []manifest.Monitor{m}},
		Cases: []Case{{Kind: kind, Driver: "a2a", Configured: true, MonitorID: m.ID, Color: "red", Timeout: 15 * time.Second,
			EvidenceType: EvidenceMockContract, Observer: []string{executable, "-test.run=^TestA2AVerificationObserver$", "--", endpoint + "/observations"}}}}
}

func TestA2AVerificationObserver(t *testing.T) {
	if os.Getenv("CPRA_VERIFY_ACTION_ID") == "" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(1)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(args[1])
	if err != nil {
		os.Exit(1)
	}
	var current, before agentObservation
	err = json.NewDecoder(response.Body).Decode(&current)
	response.Body.Close()
	if err != nil {
		os.Exit(1)
	}
	if os.Getenv("CPRA_VERIFY_PHASE") == "after" {
		if json.NewDecoder(os.Stdin).Decode(&before) != nil {
			os.Exit(1)
		}
		current.Observed = len(args) == 2 && current.Count == before.Count+1 && current.RunID == os.Getenv("CPRA_VERIFY_RUN_ID") &&
			current.Monitor == os.Getenv("CPRA_VERIFY_MONITOR_ID") && current.Action == os.Getenv("CPRA_VERIFY_ACTION_ID")
	}
	_ = json.NewEncoder(os.Stdout).Encode(current)
	os.Exit(0)
}

func readAgentRequest(t *testing.T, r *http.Request) agentObservation {
	t.Helper()
	var body struct {
		Message struct {
			MessageID string `json:"messageId"`
			Parts     []struct {
				Data struct {
					MonitorID, ActionID, ConfigurationRevision string
				} `json:"data"`
			} `json:"parts"`
		} `json:"message"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/message:send" || r.Header.Get("Authorization") != "Bearer private-agent-token" ||
		json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Message.Parts) != 2 || body.Message.MessageID != body.Message.Parts[1].Data.ActionID {
		t.Error("incorrect A2A operation or committed identity")
		return agentObservation{}
	}
	data := body.Message.Parts[1].Data
	return agentObservation{RunID: data.ConfigurationRevision, Monitor: data.MonitorID, Action: data.ActionID}
}

func TestA2AVerificationPersistsOutcomesAndRequiresObservedEffects(t *testing.T) {
	for _, tc := range []struct {
		kind, response string
		denyObserver   bool
	}{
		{"intervention", "completed", false}, {"code", "submitted", false},
		{"intervention", "failed", false}, {"intervention", "lost", false}, {"code", "lost", false},
		{"intervention", "completed", true}, {"code", "submitted", true},
	} {
		t.Run(fmt.Sprintf("%s/%s/deny=%t", tc.kind, tc.response, tc.denyObserver), func(t *testing.T) {
			var mu sync.Mutex
			var observed agentObservation
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodGet && r.URL.Path == "/observations" {
					_ = json.NewEncoder(w).Encode(observed)
					return
				}
				next := readAgentRequest(t, r)
				next.Count = observed.Count + 1
				observed = next
				if tc.response == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/a2a+json")
				fmt.Fprintf(w, `{"task":{"id":"task-original","contextId":"context-original","status":{"state":"TASK_STATE_%s"},"metadata":{"private":"provider-private-text"}}}`, strings.ToUpper(tc.response))
			}))
			defer server.Close()
			cfg := agentVerificationConfig(t, server.URL, tc.kind)
			if tc.denyObserver {
				cfg.Cases[0].Observer = append(cfg.Cases[0].Observer, "deny")
			}
			report, err := Run(t.Context(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			var row Record
			for _, candidate := range report.Records {
				if candidate.Name == "a2a" && candidate.Kind == tc.kind {
					row = candidate
				}
			}
			accepted := tc.response == "completed" || tc.response == "submitted"
			pass := accepted && !tc.denyObserver
			if row.Accepted != accepted || !row.Invoked || row.Observed != pass || (row.Status == "pass") != pass ||
				report.Complete || report.AllConfiguredPassed != pass || row.ActionID == "" || row.MonitorID != "designated-monitor" {
				t.Fatal("incorrect operation/effect evidence", row, report.AllConfiguredPassed)
			}
			storage := runtimeconfig.Default()
			storage.Storage.Directory = filepath.Join(cfg.StateDirectory, tc.kind)
			store, err := persistence.Open(t.Context(), storage)
			if err != nil {
				t.Fatal(err)
			}
			action, found, err := store.Action(row.ActionID)
			want := persistence.Unknown
			if accepted {
				want = persistence.Succeeded
			}
			if err != nil || !found || action.State != want || row.ActionState != string(want) || action.Held == accepted || action.ExecutorKind != "agent" {
				t.Fatal("restart changed original outcome", action, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(t.Context(), cfg, true); err == nil {
				t.Fatal("existing verification state granted another invocation")
			}
			mu.Lock()
			calls := observed.Count
			mu.Unlock()
			if calls != 1 {
				t.Fatal("verification or restart repeated a side effect", calls)
			}
			err = filepath.WalkDir(cfg.StateDirectory, func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for _, secret := range []string{server.URL, "private-agent-token", "provider-private-text"} {
					if strings.Contains(string(data), secret) {
						return fmt.Errorf("provider configuration or content entered verification storage")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestA2AVerificationRejectsUnsafeSetupBeforeOtherOperations(t *testing.T) {
	cfg, calls := localHTTPScenario(t)
	agent := agentVerificationConfig(t, "http://127.0.0.1:1", "intervention")
	cfg.Cases = append(cfg.Cases, agent.Cases...)
	cfg.Manifest.Monitors = append(cfg.Manifest.Monitors, agent.Manifest.Monitors...)
	for _, directory := range []string{"", "relative", t.TempDir()} {
		cfg.StateDirectory = directory
		if _, err := Run(t.Context(), cfg, true); err == nil {
			t.Fatal("invalid or existing storage accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("A2A setup failure allowed an earlier provider operation")
	}
}

func TestA2AVerificationExampleRequiresExplicitConfiguration(t *testing.T) {
	cfg, err := Load("../../examples/verification/a2a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Cases) != 2 || len(cfg.Manifest.Monitors) != 2 || !strings.Contains(cfg.StateDirectory, "REPLACE_WITH") {
		t.Fatal("missing configured-agent examples")
	}
	if err := manifest.Validate(&cfg.Manifest); err != nil {
		t.Fatal("example does not preserve the monitor schema", err)
	}
	report, err := Run(t.Context(), cfg, false)
	if err != nil || report.ConfiguredCases != 0 || report.ExecutedCases != 0 || report.Complete {
		t.Fatal("example invoked an operation or claimed verification", report, err)
	}
}

func TestA2AVerificationCrashHelper(t *testing.T) {
	directory := os.Getenv("CPRA_VERIFY_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	cfg := agentVerificationConfig(t, os.Getenv("CPRA_VERIFY_CRASH_AGENT"), "intervention")
	cfg.StateDirectory = directory
	_, _ = Run(context.Background(), cfg, true)
	t.Fatal("parent must interrupt the in-flight agent request")
}

func TestA2AVerificationProcessCrashPreservesUnknownAction(t *testing.T) {
	received := make(chan agentObservation, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/observations" {
			_ = json.NewEncoder(w).Encode(agentObservation{})
			return
		}
		received <- readAgentRequest(t, r)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	cfg := agentVerificationConfig(t, server.URL, "intervention")
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestA2AVerificationCrashHelper$")
	child.Env = append(os.Environ(), "CPRA_VERIFY_CRASH_DIRECTORY="+cfg.StateDirectory, "CPRA_VERIFY_CRASH_AGENT="+server.URL)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	var observation agentObservation
	select {
	case observation = <-received:
	case <-ctx.Done():
		t.Fatal("child did not reach agent request")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	config := runtimeconfig.Default()
	config.Storage.Directory = filepath.Join(cfg.StateDirectory, "intervention")
	store, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	action, found, err := store.Action(observation.Action)
	if err != nil || !found || action.State != persistence.Unknown || !action.Held || action.ExecutorFenced {
		t.Fatal("process loss cleared the original action or remote uncertainty", action, err)
	}
	if _, err := Run(ctx, cfg, true); err == nil {
		t.Fatal("interrupted campaign automatically repeated the agent action")
	}
}
