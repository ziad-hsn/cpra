package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Both providers are isolated HTTP fixtures. Normal startup owns TLS management,
// encrypted Raft, configuration loading, scheduling and production job dispatch.
func TestMainA2ARecoveryEscalation(t *testing.T) {
	for _, tc := range []struct {
		source, name, trigger string
		dockerStatus          int
		maxAttempts           int
		agentLost, heal       bool
	}{
		{"api", "exhausted", "recovery_exhausted", 404, 1, false, false},
		{"manifest", "exhausted", "recovery_exhausted", 404, 1, false, false},
		{"api", "permanent-rejection", "recovery_exhausted", 404, 2, false, false},
		{"manifest", "permanent-rejection", "recovery_exhausted", 404, 2, false, false},
		{"api", "first-attempt", "recovery_failed", 404, 2, false, true},
		{"api", "docker-server-error", "recovery_exhausted", 500, 2, false, false},
		{"api", "docker-lost-reply", "recovery_exhausted", 0, 2, false, false},
		{"api", "agent-lost-reply", "recovery_exhausted", 404, 1, true, false},
	} {
		t.Run(tc.source+"/"+tc.name, func(t *testing.T) {
			fixture := newMainManagementFixture(t)
			ctx := context.Background()
			// A permanent Docker rejection cannot consume unused retry capacity.
			// Exhaustion requires reaching the configured attempt limit; the
			// recovery_failed trigger can notify on the first definite failure.
			expectNotification := tc.dockerStatus == 404 && (tc.maxAttempts == 1 || tc.trigger == "recovery_failed")
			var restarts, messages, checks atomic.Int32
			var healthy atomic.Bool
			investigationStarted := make(chan struct{})
			received := make(chan map[string]any, 8)
			drop := func(w http.ResponseWriter) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}
			docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/containers/designated-test/restart") {
					t.Error("unexpected Docker operation")
					w.WriteHeader(400)
					return
				}
				restarts.Add(1)
				if tc.dockerStatus == 0 {
					drop(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.dockerStatus)
				_, _ = w.Write([]byte(`{"message":"token=provider-secret"}`))
			}))
			defer docker.Close()
			t.Setenv("DOCKER_HOST", docker.URL)
			t.Setenv("DOCKER_API_VERSION", "1.52")
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				messages.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/message:send" || r.Header.Get("Authorization") != "Bearer isolated-agent-token" {
					t.Error("incorrect agent route or identity")
					w.WriteHeader(400)
					return
				}
				var body struct {
					Message struct {
						MessageID string `json:"messageId"`
						Parts     []struct {
							Data map[string]any `json:"data"`
						} `json:"parts"`
					} `json:"message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Message.Parts) != 2 {
					t.Error("missing agent incident context")
					w.WriteHeader(400)
					return
				}
				action := body.Message.Parts[1].Data
				if action["actionId"] != body.Message.MessageID || action["role"] != "notification" {
					t.Error("notification identity or role changed")
				}
				select {
				case received <- action:
				default:
					t.Error("unbounded repeated notification")
				}
				if tc.agentLost {
					drop(w)
					return
				}
				if tc.heal && healthy.CompareAndSwap(false, true) {
					close(investigationStarted)
				}
				w.Header().Set("Content-Type", "application/a2a+json")
				_, _ = w.Write([]byte(`{"task":{"id":"investigation","contextId":"incident-context","status":{"state":"TASK_STATE_SUBMITTED"}}}`))
			}))
			defer agent.Close()
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if checks.Add(1) > 1 && tc.heal {
					// Exercise escalation after one failed attempt, before a
					// second failed observation can start another recovery.
					select {
					case <-investigationStarted:
					case <-r.Context().Done():
						return
					}
				}
				if !healthy.Load() {
					w.WriteHeader(503)
				}
			}))
			defer target.Close()
			if tc.source == "manifest" {
				body := map[string]any{"monitors": []any{map[string]any{"id": "escalation", "name": "Escalation", "enabled": false, "pulse_check": map[string]any{"type": "http", "interval": "250ms", "timeout": "1s", "unhealthy_threshold": 1, "healthy_threshold": 1, "config": map[string]any{"url": target.URL}}, "intervention": map[string]any{"action": "docker", "max_attempts": tc.maxAttempts, "target": map[string]any{"container": "designated-test", "timeout": "1s"}}, "codes": map[string]any{"red": map[string]any{"trigger": tc.trigger, "notify": "a2a", "config": map[string]any{"url": agent.URL, "token": "isolated-agent-token", "allow_insecure_http": true, "timeout": "2s"}}}}}}
				raw, _ := json.Marshal(body)
				if err := os.WriteFile(fixture.options.manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			client, stop := startMainManagement(t, fixture)
			if tc.source == "api" {
				for id, value := range map[string]string{"agent-url": agent.URL, "agent-token": "isolated-agent-token"} {
					r, err := client.Credentials.Create(ctx, api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: id}, Spec: api.CredentialSpec{Value: &value}})
					if err != nil {
						t.Fatal(err)
					}
					waitMainOperation(t, client, r.OperationID)
				}
				endpoint, err := client.NotificationEndpoints.Create(ctx, api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "investigator"}, Spec: api.DriverConfig{Type: "a2a", Config: json.RawMessage(`{"allowInsecureHttp":true,"timeout":"2s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "agent-url", "token": "agent-token"})}})
				if err != nil {
					t.Fatal(err)
				}
				waitMainOperation(t, client, endpoint.OperationID)
				check, _ := json.Marshal(api.PulseHTTPConfig{URL: api.Pointer(target.URL)})
				spec := api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: check}}, Recovery: &api.RecoverySpec{MaxAttempts: api.Pointer(int64(tc.maxAttempts)), Driver: api.DriverConfig{Type: "docker", Config: json.RawMessage(`{"container":"designated-test","timeout":"1s"}`)}}, Notifications: api.Pointer(map[string]api.AlertRule{"red": {Trigger: api.Pointer(api.AlertRuleTrigger(tc.trigger)), EndpointRefs: api.Pointer([]string{"investigator"})}})}
				if tc.heal {
					spec.Check.Timeout = "10s"
				}
				admissionStarted := time.Now()
				created, err := client.Monitors.Create(ctx, api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "escalation"}, Spec: spec})
				if err != nil {
					var problem *cpra.Error
					status := 0
					if errors.As(err, &problem) {
						status = problem.StatusCode
					}
					var uncertain *cpra.AmbiguousError
					operationPresent := created != nil && created.OperationID != ""
					if errors.As(err, &uncertain) {
						operationPresent = operationPresent || uncertain.OperationID != ""
					}
					t.Fatalf("monitor admission failed: %v (status=%d deadline=%t canceled=%t operation_present=%t elapsed=%s checks=%d recoveries=%d notifications=%d)",
						err, status, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), operationPresent,
						time.Since(admissionStarted), checks.Load(), restarts.Load(), messages.Load())
				}
				t.Logf("disabled monitor admitted in %s; operation_present=%t", time.Since(admissionStarted), created.OperationID != "")
				waitMainOperation(t, client, created.OperationID)
			}
			monitor, err := client.Monitors.Get(ctx, "escalation")
			if err != nil {
				t.Fatal(err)
			}
			if restarts.Load() != 0 || messages.Load() != 0 || checks.Load() != 0 {
				t.Fatal("configuration performed provider operations")
			}
			if monitor.Data.Spec.Notifications == nil || (*monitor.Data.Spec.Notifications)["red"].Trigger == nil || string(*(*monitor.Data.Spec.Notifications)["red"].Trigger) != tc.trigger {
				t.Fatal("saved monitor lost trigger")
			}
			operation, err := client.Monitors.Enable(ctx, "escalation", monitor.ResourceVersion)
			if err != nil {
				t.Fatal(err)
			}
			waitMainOperation(t, client, operation.OperationID)
			var terminal api.Event
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				history, err := client.History(ctx, cpra.ListOptions{MonitorID: "escalation"})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range history.Data.Items {
					if tc.dockerStatus != 404 && event.ActionKind == "intervention" && event.Kind == "action_unknown" {
						terminal = event
					}
					if expectNotification && event.ActionKind == "code" && ((!tc.agentLost && event.Kind == "action_succeeded") || (tc.agentLost && event.Kind == "action_unknown")) {
						terminal = event
					}
					if tc.dockerStatus == 404 && !expectNotification && event.ActionKind == "intervention" && event.Kind == "action_failed" {
						terminal = event
					}
				}
				if terminal.ActionID != "" {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if terminal.ActionID == "" {
				t.Fatalf("no expected committed outcome: docker=%d agent=%d", restarts.Load(), messages.Load())
			}
			action, err := client.Actions.Get(ctx, terminal.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			if expectNotification {
				var sent map[string]any
				select {
				case sent = <-received:
				default:
					t.Fatal("no independently received notification")
				}
				failure, ok := sent["recoveryFailure"].(map[string]any)
				wantAttempt := tc.maxAttempts
				if tc.heal {
					wantAttempt = 1
					if restarts.Load() != 1 {
						t.Fatal("early escalation waited for or repeated a second recovery")
					}
				}
				if !ok || len(failure) != 7 || failure["driver"] != "docker" || failure["outcome"] != "failure" || failure["trigger"] != tc.trigger || failure["attempt"] != float64(wantAttempt) || failure["maxAttempts"] != float64(tc.maxAttempts) || failure["actionId"] == "" {
					t.Fatal("incorrect or unsafe agent failure context", failure)
				}
				if terminal.RecoveryFailure == nil || action.Data.RecoveryFailure == nil || terminal.RecoveryFailure.ActionID != failure["actionId"] || action.Data.RecoveryFailure.ActionID != failure["actionId"] || terminal.IncidentID != sent["incidentId"] || messages.Load() != 1 {
					t.Fatal("API, history and agent disagree about the original failure")
				}
				if tc.agentLost && (!action.Data.Held || action.Data.State != "unknown") {
					t.Fatal("lost notification reply was not held")
				}
			} else if tc.dockerStatus == 404 {
				if messages.Load() != 0 || restarts.Load() != 1 || action.Data.Held || action.Data.State != "failed" || action.Data.RecoveryFailure != nil {
					t.Fatal("permanent rejection retried, escalated as exhausted, or became unknown")
				}
			} else if messages.Load() != 0 || restarts.Load() != 1 || !action.Data.Held || action.Data.RecoveryFailure != nil {
				t.Fatal("unknown recovery triggered or repeated an external action")
			}
			stop()
			beforeRestarts, beforeMessages := restarts.Load(), messages.Load()
			if err := os.Remove(fixture.options.manifest); err != nil {
				t.Fatal(err)
			}
			fixture.options.allowEmpty = false
			restarted, stopRestarted := startMainManagement(t, fixture)
			beforeChecks := checks.Load()
			deadline = time.Now().Add(5 * time.Second)
			for checks.Load() < beforeChecks+3 && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			recovered, err := restarted.Actions.Get(ctx, terminal.ActionID)
			if err != nil || checks.Load() < beforeChecks+3 || restarts.Load() != beforeRestarts || messages.Load() != beforeMessages || recovered.Data.Held != action.Data.Held || recovered.Data.State != action.Data.State {
				t.Fatal("restart repeated completed/unknown work or lost its hold", err)
			}
			stopRestarted()
			wantMessages := int32(0)
			if expectNotification {
				wantMessages = 1
			}
			if restarts.Load() != 1 || messages.Load() != wantMessages {
				t.Fatal("shutdown or restart repeated an external operation", restarts.Load(), messages.Load())
			}
			if tc.dockerStatus == 404 && !expectNotification && (recovered.Data.State != "failed" || recovered.Data.Held) {
				t.Fatal("restart changed the permanent rejection outcome")
			}
		})
	}
}
