package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newTestCommand builds a root command pointed at the given server, capturing
// stdout into a buffer so output can be asserted.
func newTestCommand(t *testing.T, srvURL string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	root := NewRootCommand()
	root.SetOut(buf)
	root.SetErr(buf)
	args := append([]string{"--server", srvURL}, extraArgs...)
	root.SetArgs(args)
	return root, buf
}

// apiStub returns an httptest server serving canned API payloads.
func apiStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/overview", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"generated":  "2024-01-01T00:00:00Z",
			"total":      3,
			"disabled":   1,
			"by_status":  map[string]int{"up": 2, "down": 1},
			"up_percent": 66.67,
		})
	})
	mux.HandleFunc("/api/v1/monitors", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"generated": "2024-01-01T00:00:00Z",
			"page":      1, "size": 50, "total": 2,
			"monitors": []map[string]interface{}{
				{"id": 1, "name": "alpha", "pulse_type": "http", "status": "up"},
				{"id": 2, "name": "beta", "pulse_type": "tcp", "status": "down", "pending_code": "red", "consecutive_failures": 3},
			},
		})
	})
	mux.HandleFunc("/api/v1/monitors/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": 2, "name": "beta", "pulse_type": "tcp", "status": "down", "pending_code": "red",
		})
	})
	mux.HandleFunc("/api/v1/queues", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"pulse": map[string]interface{}{"name": "pulse", "queue_depth": 5, "capacity": 131072, "enqueue_rate": 1.5, "dequeue_rate": 1.4},
		})
	})
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetMonitorsTable(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "get", "monitors")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ID", "NAME", "STATUS", "alpha", "beta", "red"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestGetMonitorsJSON(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "get", "monitors", "-o", "json")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\"monitors\"") || !strings.Contains(out, "\"name\": \"alpha\"") {
		t.Errorf("json output missing monitors:\n%s", out)
	}
}

func TestGetMonitorDetail(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "get", "monitor", "2")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "beta") || !strings.Contains(out, "Pending code") {
		t.Errorf("detail output:\n%s", out)
	}
}

func TestGetMonitorInvalidID(t *testing.T) {
	srv := apiStub(t)
	root, _ := newTestCommand(t, srv.URL, "get", "monitor", "notanumber")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid monitor ID") {
		t.Fatalf("expected invalid ID error, got %v", err)
	}
}

func TestGetQueuesTable(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "get", "queues")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "pulse") || !strings.Contains(out, "DEPTH") || !strings.Contains(out, "1.5/s") {
		t.Errorf("queues table output:\n%s", out)
	}
}

func TestGetOverview(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "get", "overview")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Total monitors") || !strings.Contains(out, "up") {
		t.Errorf("overview output:\n%s", out)
	}
}

func TestHealth(t *testing.T) {
	srv := apiStub(t)
	root, buf := newTestCommand(t, srv.URL, "health")
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Errorf("health output: %q", buf.String())
	}
}

func TestHealthUnhealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	root, _ := newTestCommand(t, srv.URL, "health")
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("expected unhealthy error, got %v", err)
	}
}
