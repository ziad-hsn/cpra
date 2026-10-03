package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type mainMonitorAdmissionReceipt struct {
	operationID string
	version     string
}

// The transport reaches the application's real TLS/Raft admission before
// withholding the reply. The receipt is a test oracle, never supplied to the
// caller when the selected boundary hides the response headers.
type mainMonitorAdmissionTransport struct {
	http.RoundTripper
	boundary string
	cancel   context.CancelFunc
	receipt  chan mainMonitorAdmissionReceipt
	posts    atomic.Int64
}

func (transport *mainMonitorAdmissionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	monitorCreate := request.Method == http.MethodPost && request.URL.Path == "/api/v2/monitors"
	if monitorCreate {
		transport.posts.Add(1)
	}
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil || !monitorCreate || response.StatusCode != http.StatusOK || response.Header.Get("X-CPRa-Admission") != "committed" {
		return response, err
	}
	_ = response.Body.Close()
	transport.receipt <- mainMonitorAdmissionReceipt{
		operationID: response.Header.Get("X-Operation-ID"),
		version:     response.Header.Get("X-Resource-Version"),
	}
	if transport.cancel != nil {
		transport.cancel()
	}
	if transport.boundary == "headers" {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	response.Body = &mainMonitorAdmissionBody{context: request.Context()}
	return response, nil
}

func (transport *mainMonitorAdmissionTransport) CloseIdleConnections() {
	if closer, ok := transport.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type mainMonitorAdmissionBody struct{ context context.Context }

func (body *mainMonitorAdmissionBody) Read([]byte) (int, error) {
	<-body.context.Done()
	return 0, body.context.Err()
}

func (*mainMonitorAdmissionBody) Close() error { return nil }

// A timeout while admitting configuration is distinct from an uncertain agent
// effect. Lost replies must not trigger a second create, enable the monitor, or
// dispatch providers, including after restart. Both A2A execution roles use the
// normal management client and application owner, with local provider fixtures.
func TestMainA2AMonitorAdmissionLostReply(t *testing.T) {
	for _, role := range []string{"intervention", "notification"} {
		for _, boundary := range []string{"headers", "body"} {
			for _, failure := range []string{"cancel", "deadline"} {
				t.Run(role+"/"+boundary+"/"+failure, func(t *testing.T) {
					fixture := newMainManagementFixture(t)
					transport := &mainMonitorAdmissionTransport{
						RoundTripper: fixture.client.Transport,
						boundary:     boundary,
						receipt:      make(chan mainMonitorAdmissionReceipt, 1),
					}
					fixture.client.Transport = transport
					var checks, agentCalls atomic.Int64
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						checks.Add(1)
						w.WriteHeader(http.StatusServiceUnavailable)
					}))
					defer target.Close()
					agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						agentCalls.Add(1)
						w.WriteHeader(http.StatusInternalServerError)
					}))
					defer agent.Close()
					client, stop := startMainManagement(t, fixture)
					for _, credential := range []struct{ id, value string }{
						{"agent-url", agent.URL}, {"agent-token", "admission-fixture-private-token"},
					} {
						created, err := client.Credentials.Create(t.Context(), api.Credential{
							APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: credential.id},
							Spec: api.CredentialSpec{Value: api.Pointer(credential.value)},
						})
						if err != nil {
							t.Fatal("create agent credential", err)
						}
						waitMainOperation(t, client, created.OperationID)
					}
					driver := api.DriverConfig{Type: "a2a", Config: json.RawMessage(`{"allowInsecureHttp":true,"requestTimeout":"2s"}`),
						CredentialRefs: api.Pointer(map[string]string{"url": "agent-url", "token": "agent-token"})}
					check, _ := json.Marshal(api.PulseHTTPConfig{URL: api.Pointer(target.URL)})
					monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "agent-monitor"},
						Spec: api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "100ms", Timeout: "1s",
							Driver: api.DriverConfig{Type: "http", Config: check}}}}
					if role == "intervention" {
						driver.Config = json.RawMessage(`{"allowInsecureHttp":true,"requestTimeout":"2s","taskTimeout":"3s"}`)
						monitor.Spec.Recovery = &api.RecoverySpec{Driver: driver}
					} else {
						driver.Config = json.RawMessage(`{"allowInsecureHttp":true,"requestTimeout":"2s","timeout":"3s"}`)
						endpoint, err := client.NotificationEndpoints.Create(t.Context(), api.NotificationEndpoint{
							APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "agent-endpoint"}, Spec: driver,
						})
						if err != nil {
							t.Fatal("create agent endpoint", err)
						}
						waitMainOperation(t, client, endpoint.OperationID)
						monitor.Spec.Notifications = api.Pointer(map[string]api.AlertRule{"red": {EndpointRefs: api.Pointer([]string{"agent-endpoint"})}})
					}

					requestCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					wantCause := context.DeadlineExceeded
					if failure == "cancel" {
						transport.cancel = cancel
						wantCause = context.Canceled
					}
					response, err := client.Monitors.Create(requestCtx, monitor)
					var uncertain *cpra.AmbiguousError
					if !errors.As(err, &uncertain) || !errors.Is(err, wantCause) || errors.Is(err, cpra.ErrNotAdmitted) {
						t.Fatal("lost committed reply did not preserve uncertain admission and its cause", err)
					}
					var receipt mainMonitorAdmissionReceipt
					select {
					case receipt = <-transport.receipt:
					default:
						t.Fatal("request did not reach confirmed admission before reply loss")
					}
					if receipt.operationID == "" || receipt.version == "" {
						t.Fatal("server did not return committed operation/resource identities")
					}
					if boundary == "headers" {
						if response != nil || uncertain.OperationID != "" {
							t.Fatal("SDK invented a receipt that the caller did not receive")
						}
					} else if response == nil || response.OperationID != receipt.operationID || uncertain.OperationID != receipt.operationID {
						t.Fatal("SDK lost an operation handle received before body failure")
					}
					waitMainOperation(t, client, receipt.operationID)
					assertDisabled := func(client *cpra.Client) {
						t.Helper()
						stored, err := client.Monitors.Get(t.Context(), "agent-monitor")
						if err != nil || stored.ResourceVersion != receipt.version || stored.Data.Spec.Enabled == nil || *stored.Data.Spec.Enabled {
							t.Fatal("read-only reconciliation lost the original disabled configuration", err)
						}
						actions, err := client.Actions.List(t.Context(), cpra.ListOptions{MonitorID: "agent-monitor"})
						if err != nil || len(actions.Data.Items) != 0 || checks.Load() != 0 || agentCalls.Load() != 0 || transport.posts.Load() != 1 {
							t.Fatal("uncertain configuration was retried or caused provider work", err)
						}
					}
					assertDisabled(client)
					stop()
					restarted, stopRestarted := startMainManagement(t, fixture)
					waitMainOperation(t, restarted, receipt.operationID)
					assertDisabled(restarted)
					stopRestarted()
				})
			}
		}
	}
}
