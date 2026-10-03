package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// This uses normal startup, TLS management, encrypted Raft configuration,
// scheduler and controller dispatch. The agent is a local HTTP fixture.
func TestMainA2AConfiguredActionsSurviveRestart(t *testing.T) {
	for _, source := range []string{"api", "manifest"} {
		for _, role := range []string{"intervention", "notification"} {
			t.Run(source+"/"+role, func(t *testing.T) {
				fixture := newMainManagementFixture(t)
				ctx := context.Background()
				var requests, checks atomic.Int32
				var healthy atomic.Bool
				secret := "agent-" + role + "-credential-must-remain-private"
				agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/message:send" || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("A2A-Version") != "1.0" {
						t.Error("wrong agent method, destination or credential")
					}
					var body struct {
						Message struct {
							ID    string `json:"messageId"`
							Parts []struct {
								Data json.RawMessage `json:"data"`
							} `json:"parts"`
						} `json:"message"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message.ID == "" || len(body.Message.Parts) != 2 {
						t.Error("missing action context")
					} else {
						var action struct{ MonitorID, Role, ActionID string }
						if json.Unmarshal(body.Message.Parts[1].Data, &action) != nil || action.MonitorID != "agent-monitor" || action.Role != role || action.ActionID != body.Message.ID || bytes.Contains(body.Message.Parts[1].Data, []byte(secret)) {
							t.Error("wrong or unsafe action context")
						}
					}
					state := "TASK_STATE_SUBMITTED"
					if role == "intervention" {
						state = "TASK_STATE_COMPLETED"
						healthy.Store(true)
					}
					w.Header().Set("Content-Type", "application/a2a+json")
					_, _ = w.Write([]byte(`{"task":{"id":"agent-task","contextId":"agent-context","status":{"state":"` + state + `"}}}`))
				}))
				defer agent.Close()
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					checks.Add(1)
					if !healthy.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}))
				defer target.Close()
				if source == "manifest" {
					monitor := map[string]any{"id": "agent-monitor", "name": "Agent monitor", "enabled": false, "pulse_check": map[string]any{"type": "http", "interval": "100ms", "timeout": "1s", "unhealthy_threshold": 1, "healthy_threshold": 1, "config": map[string]any{"url": target.URL}}}
					config := map[string]any{"url": agent.URL, "token": secret, "allow_insecure_http": true, "request_timeout": "2s"}
					input := map[string]any{"monitors": []any{monitor}}
					if role == "intervention" {
						config["task_timeout"] = "3s"
						monitor["intervention"] = map[string]any{"action": "a2a", "max_failures": 1, "target": config}
					} else {
						config["timeout"] = "3s"
						input["endpoints"] = map[string]any{"agent-endpoint": map[string]any{"type": "a2a", "config": config}}
						input["notification_groups"] = map[string]any{"agents": []string{"agent-endpoint"}}
						monitor["codes"] = map[string]any{"red": map[string]any{"notify_group": "agents"}}
					}
					raw, _ := json.Marshal(input)
					if err := os.WriteFile(fixture.options.manifest, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				client, stop := startMainManagement(t, fixture)
				if source == "api" {
					for id, value := range map[string]string{"agent-url": agent.URL, "agent-token": secret} {
						created, err := client.Credentials.Create(ctx, api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: id}, Spec: api.CredentialSpec{Value: &value}})
						if err != nil {
							t.Fatal(err)
						}
						waitMainOperation(t, client, created.OperationID)
					}
					driver := api.DriverConfig{Type: "a2a", Config: json.RawMessage(`{"requestTimeout":"2s","allowInsecureHttp":true}`), CredentialRefs: api.Pointer(map[string]string{"url": "agent-url", "token": "agent-token"})}
					checkConfig, _ := json.Marshal(api.PulseHTTPConfig{URL: api.Pointer(target.URL)})
					spec := api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "100ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: checkConfig}}}
					if role == "intervention" {
						driver.Config = json.RawMessage(`{"requestTimeout":"2s","taskTimeout":"3s","allowInsecureHttp":true}`)
						spec.Recovery = &api.RecoverySpec{Driver: driver, MaxFailures: api.Pointer(int64(1))}
					} else {
						driver.Config = json.RawMessage(`{"requestTimeout":"2s","timeout":"3s","allowInsecureHttp":true}`)
						created, err := client.NotificationEndpoints.Create(ctx, api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "agent-endpoint"}, Spec: driver})
						if err != nil {
							t.Fatal(err)
						}
						waitMainOperation(t, client, created.OperationID)
						spec.Notifications = api.Pointer(map[string]api.AlertRule{"red": {EndpointRefs: api.Pointer([]string{"agent-endpoint"})}})
					}
					created, err := client.Monitors.Create(ctx, api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "agent-monitor"}, Spec: spec})
					if err != nil {
						t.Fatal(err)
					}
					waitMainOperation(t, client, created.OperationID)
				}
				monitor, err := client.Monitors.Get(ctx, "agent-monitor")
				if err != nil {
					t.Fatal(err)
				}
				read, _ := json.Marshal(monitor.Data)
				if bytes.Contains(read, []byte(secret)) || bytes.Contains(read, []byte(agent.URL)) || requests.Load() != 0 || checks.Load() != 0 {
					t.Fatal("configuration leaked credentials or invoked a disabled provider")
				}
				enabled, err := client.Monitors.Enable(ctx, "agent-monitor", monitor.ResourceVersion)
				if err != nil {
					t.Fatal(err)
				}
				waitMainOperation(t, client, enabled.OperationID)
				deadline := time.Now().Add(12 * time.Second)
				completed := false
				var actionID string
				receiptSeen := false
				for time.Now().Before(deadline) {
					history, err := client.History(ctx, cpra.ListOptions{MonitorID: "agent-monitor"})
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range history.Data.Items {
						if r := event.AgentReceipt; r != nil {
							receiptSeen = r.Sequence == "1" && r.TaskID != nil && *r.TaskID == "agent-task" && r.ContextID != nil && *r.ContextID == "agent-context"
						}
						if event.Kind == "action_succeeded" {
							completed = true
							actionID = event.ActionID
						}
					}
					if completed {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				if !completed || !receiptSeen || requests.Load() != 1 {
					t.Fatal("configured action did not complete once", completed, requests.Load())
				}
				action, err := client.Actions.Get(ctx, actionID)
				if err != nil || action.Data.AgentReceipt == nil || action.Data.AgentReceipt.TaskID == nil || *action.Data.AgentReceipt.TaskID != "agent-task" {
					t.Fatal("action read lost committed agent correlation", err)
				}
				stop()
				if err := os.Remove(fixture.options.manifest); err != nil {
					t.Fatal(err)
				}
				fixture.options.allowEmpty = false
				restarted, stopRestarted := startMainManagement(t, fixture)
				before := checks.Load()
				deadline = time.Now().Add(5 * time.Second)
				for checks.Load() < before+2 && time.Now().Before(deadline) {
					time.Sleep(25 * time.Millisecond)
				}
				state, err := restarted.State(ctx)
				if err != nil || !state.Data.Ready || !state.Data.Storage.Available || checks.Load() < before+2 || requests.Load() != 1 {
					t.Fatal("restart repeated the accepted action or failed to resume checks", err, requests.Load())
				}
				stopRestarted()
				if err := filepath.WalkDir(fixture.settings.Storage.Directory, func(path string, entry os.DirEntry, walkErr error) error {
					if walkErr != nil || entry.IsDir() {
						return walkErr
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					defer clear(data)
					if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(agent.URL)) {
						t.Error("plaintext agent connection persisted in Raft or history")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
