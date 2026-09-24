package cpra_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const consumerHandle = "op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000001"
const allocationProblem = `{"type":"about:blank","title":"Operation allocation unavailable","status":503,"code":"operationAllocationUnconfirmed"}`

func consumerClient(t *testing.T, handler http.HandlerFunc) *cpra.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: "local-consumer-token", HTTPClient: server.Client(), ReadAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func consumerMonitor() api.Monitor {
	return api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "m"}, Spec: api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}}}}
}

func TestConsumerOperationHandlesRemainOpaqueAndExact(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "lost-reply"}[interrupted], func(t *testing.T) {
			var writes, reads atomic.Int32
			client := consumerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Operation-ID", consumerHandle)
				if r.Method == "GET" {
					reads.Add(1)
					if r.URL.EscapedPath() != "/api/v2/operations/"+consumerHandle || r.URL.RawQuery != "" {
						t.Error("opaque handle changed in transport")
					}
					_ = json.NewEncoder(w).Encode(api.Operation{ID: consumerHandle, ContentDigest: strings.Repeat("a", 64), State: "committed", Committed: api.Pointer(int64(1))})
					return
				}
				writes.Add(1)
				if interrupted {
					w.Header().Set("Content-Length", "10000")
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, "{")
					return
				}
				_ = json.NewEncoder(w).Encode(consumerMonitor())
			})
			response, err := client.Monitors.Create(context.Background(), consumerMonitor())
			if response == nil || response.OperationID != consumerHandle || errors.Is(err, cpra.ErrAmbiguous) != interrupted {
				t.Fatal("mutation receipt lost exact opaque handle", err)
			}
			if !interrupted && err != nil {
				t.Fatal(err)
			}
			if interrupted {
				var uncertain *cpra.AmbiguousError
				if !errors.As(err, &uncertain) || uncertain.OperationID != consumerHandle {
					t.Fatal("lost reply omitted original handle", err)
				}
			}
			operation, err := client.Operations.Get(context.Background(), consumerHandle)
			if err != nil || operation.Data.ID != consumerHandle || operation.OperationID != consumerHandle || writes.Load() != 1 || reads.Load() != 1 {
				t.Fatal("operation follow-up changed identity or repeated work", err)
			}
		})
	}
}

func TestConsumerNotAdmittedRequiresCompleteExactContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		change    func(http.Header)
		status    int
		truncated bool
		want      bool
	}{
		{name: "exact", body: allocationProblem, want: true},
		{name: "valid-media-parameter", body: allocationProblem, change: func(h http.Header) { h.Set("Content-Type", "application/problem+json; charset=utf-8") }, want: true},
		{name: "missing-code", body: strings.ReplaceAll(allocationProblem, `,"code":"operationAllocationUnconfirmed"`, "")},
		{name: "different-code", body: strings.ReplaceAll(allocationProblem, "operationAllocationUnconfirmed", "commitUnconfirmed")},
		{name: "missing-title", body: strings.ReplaceAll(allocationProblem, `"title":"Operation allocation unavailable",`, "")},
		{name: "wrong-type", body: strings.ReplaceAll(allocationProblem, "about:blank", "https://untrusted.example/problem")},
		{name: "status-mismatch", body: strings.ReplaceAll(allocationProblem, `"status":503`, `"status":500`)},
		{name: "http-status-mismatch", body: allocationProblem, status: 500},
		{name: "missing-marker", body: allocationProblem, change: func(h http.Header) { h.Del("X-CPRa-Admission") }},
		{name: "committed-marker", body: allocationProblem, change: func(h http.Header) { h.Set("X-CPRa-Admission", "committed") }},
		{name: "duplicate-marker", body: allocationProblem, change: func(h http.Header) { h.Add("X-CPRa-Admission", "not-submitted") }},
		{name: "wrong-media", body: allocationProblem, change: func(h http.Header) { h.Set("Content-Type", "application/json") }},
		{name: "header-handle", body: allocationProblem, change: func(h http.Header) { h.Set("X-Operation-ID", consumerHandle) }},
		{name: "empty-header-handle", body: allocationProblem, change: func(h http.Header) { h.Set("X-Operation-ID", "") }},
		{name: "body-handle", body: strings.TrimSuffix(allocationProblem, "}") + `,"operationID":"` + consumerHandle + `"}`},
		{name: "null-body-handle", body: strings.TrimSuffix(allocationProblem, "}") + `,"operationID":null}`},
		{name: "duplicate-code", body: strings.TrimSuffix(allocationProblem, "}") + `,"code":"operationAllocationUnconfirmed"}`},
		{name: "case-alias", body: strings.ReplaceAll(allocationProblem, `"status"`, `"Status"`)},
		{name: "trailing-value", body: allocationProblem + `{}`},
		{name: "truncated-json", body: strings.TrimSuffix(allocationProblem, "}")},
		{name: "truncated-transfer", body: allocationProblem, truncated: true},
		{name: "oversized", body: strings.TrimSuffix(allocationProblem, "}") + `,"detail":"` + strings.Repeat("x", 64<<10) + `"}`},
		{name: "invalid-utf8", body: strings.TrimSuffix(allocationProblem, "}") + ",\"detail\":\"\xff\"}"},
		{name: "null-optional-string", body: strings.TrimSuffix(allocationProblem, "}") + `,"detail":null}`},
		{name: "null-errors", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":null}`},
		{name: "null-field-error", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":[null]}`},
		{name: "missing-field-error-member", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":[{"field":"x"}]}`},
		{name: "null-field-error-member", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":[{"field":null,"message":"x"}]}`},
		{name: "field-error-case-alias", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":[{"Field":"x","message":"x"}]}`},
		{name: "valid-field-error", body: strings.TrimSuffix(allocationProblem, "}") + `,"errors":[{"field":"x","message":"x","reason":"busy"}]}`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := consumerClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("X-CPRa-Admission", "not-submitted")
				if test.change != nil {
					test.change(w.Header())
				}
				if test.truncated {
					w.Header().Set("Content-Length", "10000")
				}
				status := test.status
				if status == 0 {
					status = 503
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, test.body)
			})
			_, err := client.Monitors.Create(context.Background(), consumerMonitor())
			var problem *cpra.Error
			if err == nil || !errors.As(err, &problem) || errors.Is(err, cpra.ErrNotAdmitted) != test.want || errors.Is(err, cpra.ErrAmbiguous) == test.want || calls.Load() != 1 {
				t.Fatal("invalid non-admission classification or automatic retry", err)
			}
			if (test.status == 0 || test.status == 503) && !errors.Is(err, cpra.ErrUnavailable) {
				t.Fatal("503 unavailable classification was lost")
			}
		})
	}
}
