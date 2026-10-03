package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func waitMainAgentAction(t *testing.T, client *cpra.Client, condition func(api.Action) bool) api.Action {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := client.Actions.List(ctx, cpra.ListOptions{MonitorID: "agent-monitor"})
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range page.Data.Items {
			if condition(action) {
				return action
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("agent action did not reach the expected committed state", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestMainA2AObservesOriginalTaskAfterRestart(t *testing.T) {
	for _, role := range []string{"intervention", "notification"} {
		for _, change := range []string{"unchanged", "reconfigured", "removed"} {
			t.Run(role+"/"+change, func(t *testing.T) {
				fixture := newMainManagementFixture(t)
				if change == "removed" {
					fixture.settings.Storage.Encryption = fixture.settings.Management.Encryption
					fixture.settings.Management.Encryption = nil
					writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
				}
				ctx := t.Context()
				var sends, reads, replacementCalls atomic.Int32
				var complete atomic.Bool
				const secret = "original-agent-observation-private-token"
				agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("A2A-Version") != "1.0" {
						t.Error("observation used a different identity")
					}
					state := "TASK_STATE_INPUT_REQUIRED"
					if complete.Load() {
						state = "TASK_STATE_COMPLETED"
					}
					task := `{"id":"original-task","contextId":"original-context","status":{"state":"` + state + `"}}`
					w.Header().Set("Content-Type", "application/a2a+json")
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/message:send":
						sends.Add(1)
						_, _ = w.Write([]byte(`{"task":` + task + `}`))
					case r.Method == http.MethodGet && r.URL.Path == "/tasks/original-task" && r.URL.Query().Get("historyLength") == "0":
						reads.Add(1)
						_, _ = w.Write([]byte(task))
					default:
						t.Error("observer used an unexpected operation or task")
						http.Error(w, "unexpected operation", http.StatusBadRequest)
					}
				}))
				defer agent.Close()
				replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					replacementCalls.Add(1)
					http.Error(w, "replacement must not receive old work", http.StatusInternalServerError)
				}))
				defer replacement.Close()
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
				defer target.Close()
				monitor := map[string]any{"id": "agent-monitor", "name": "Agent monitor", "enabled": true,
					"pulse_check": map[string]any{"type": "http", "interval": "100ms", "timeout": "1s", "unhealthy_threshold": 1, "healthy_threshold": 1, "config": map[string]any{"url": target.URL}}}
				connection := map[string]any{"url": agent.URL, "token": secret, "allow_insecure_http": true, "request_timeout": "2s"}
				input := map[string]any{"monitors": []any{monitor}}
				if role == "intervention" {
					connection["task_timeout"] = "2s"
					monitor["intervention"] = map[string]any{"action": "a2a", "max_failures": 1, "target": connection}
				} else {
					connection["timeout"] = "2s"
					input["endpoints"] = map[string]any{"agent-endpoint": map[string]any{"type": "a2a", "config": connection}}
					input["notification_groups"] = map[string]any{"agents": []string{"agent-endpoint"}}
					monitor["codes"] = map[string]any{"red": map[string]any{"notify_group": "agents"}}
				}
				raw, _ := json.Marshal(input)
				if err := os.WriteFile(fixture.options.manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
				client, stop := startMainManagement(t, fixture)
				original := waitMainAgentAction(t, client, func(a api.Action) bool {
					return a.State == "unknown" && a.AgentReceipt != nil && a.AgentReceipt.State != nil && *a.AgentReceipt.State == "TASK_STATE_INPUT_REQUIRED"
				})
				if !original.Held || original.ExecutorFenced || sends.Load() != 1 {
					t.Fatal("ambiguous task was repeated or released")
				}
				resource, err := client.Monitors.Get(ctx, "agent-monitor")
				if err != nil {
					t.Fatal(err)
				}
				// Disable prevents new checks/actions while configuration changes.
				// It does not cancel already-running agent work or conceal its state.
				disabled, err := client.Monitors.Disable(ctx, "agent-monitor", resource.ResourceVersion)
				if err != nil {
					t.Fatal(err)
				}
				waitMainOperation(t, client, disabled.OperationID)
				if change == "reconfigured" {
					var driver api.DriverConfig
					if role == "intervention" {
						driver = resource.Data.Spec.Recovery.Driver
					} else {
						endpoint, err := client.NotificationEndpoints.Get(ctx, "agent-endpoint")
						if err != nil {
							t.Fatal(err)
						}
						driver = endpoint.Data.Spec
					}
					if driver.CredentialRefs == nil {
						t.Fatal("bootstrap did not protect agent credentials")
					}
					for field, value := range map[string]string{"url": replacement.URL, "token": "new-agent-token"} {
						id := (*driver.CredentialRefs)[field]
						credential, err := client.Credentials.Get(ctx, id)
						if err != nil {
							t.Fatal(err)
						}
						request := credential.Data
						request.Spec.Value = &value
						updated, err := client.Credentials.Replace(ctx, id, credential.ResourceVersion, request)
						if err != nil {
							t.Fatal(err)
						}
						waitMainOperation(t, client, updated.OperationID)
					}
				} else if change == "removed" {
					resource, err := client.Monitors.Get(ctx, "agent-monitor")
					if err != nil {
						t.Fatal(err)
					}
					removed, err := client.Monitors.Delete(ctx, "agent-monitor", resource.ResourceVersion)
					if err != nil {
						t.Fatal(err)
					}
					waitMainOperation(t, client, removed.OperationID)
				}
				stop()
				if err := os.Remove(fixture.options.manifest); err != nil {
					t.Fatal(err)
				}
				complete.Store(true)
				readsBefore := reads.Load()
				restarted, stopRestarted := startMainManagement(t, fixture)
				observed := waitMainAgentAction(t, restarted, func(a api.Action) bool {
					return a.ID == original.ID && a.AgentReceipt != nil && a.AgentReceipt.State != nil && *a.AgentReceipt.State == "TASK_STATE_COMPLETED"
				})
				if observed.State != "unknown" || !observed.Held || !observed.ExecutorFenced || observed.Review != nil ||
					observed.ExecutionRevision != original.ExecutionRevision || observed.IncarnationUID != original.IncarnationUID ||
					sends.Load() != 1 || reads.Load() <= readsBefore || replacementCalls.Load() != 0 {
					t.Fatal("restart redirected, repeated or automatically resolved original work")
				}
				public, _ := json.Marshal(observed)
				if bytes.Contains(public, []byte(secret)) || bytes.Contains(public, []byte(agent.URL)) || bytes.Contains(public, []byte("agent_connection")) {
					t.Fatal("action response exposed its retained connection")
				}
				stopRestarted()
			})
		}
	}
}
