package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New(Config{BaseURL: "://bad"}); err == nil {
		t.Fatal("expected error for malformed base URL")
	}
	if _, err := New(Config{BaseURL: "localhost:8060"}); err == nil {
		t.Fatal("expected error for base URL without scheme")
	}
}

func TestOverview(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/overview" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(w, map[string]interface{}{
			"generated":  "2024-01-01T00:00:00Z",
			"total":      3,
			"disabled":   1,
			"by_status":  map[string]int{"up": 2, "down": 1},
			"up_percent": 66.67,
		})
	})
	c := newTestClient(t, h)
	o, err := c.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.Total != 3 || o.Disabled != 1 || o.ByStatus["up"] != 2 {
		t.Fatalf("overview = %+v", o)
	}
}

func TestListMonitorsQueryParams(t *testing.T) {
	var gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		writeJSON(w, map[string]interface{}{
			"page":     2,
			"size":     10,
			"total":    1,
			"monitors": []interface{}{},
		})
	})
	c := newTestClient(t, h)
	_, err := c.ListMonitors(context.Background(), MonitorListOptions{Status: "down", PulseType: "http", Page: 2, Size: 10})
	if err != nil {
		t.Fatalf("ListMonitors: %v", err)
	}
	for _, want := range []string{"status=down", "type=http", "page=2", "size=10"} {
		if !containsParam(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
}

func containsParam(query, kv string) bool {
	for _, p := range splitParams(query) {
		if p == kv {
			return true
		}
	}
	return false
}

func splitParams(query string) []string {
	if query == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(query); i++ {
		if i == len(query) || query[i] == '&' {
			out = append(out, query[start:i])
			start = i + 1
		}
	}
	return out
}

func TestGetMonitorNotFound(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "monitor 9999 not found"})
	})
	c := newTestClient(t, h)
	_, err := c.GetMonitor(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestQueues(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"pulse": map[string]interface{}{
				"name":         "pulse",
				"queue_depth":  5,
				"capacity":     100,
				"enqueue_rate": 1.5,
			},
		})
	})
	c := newTestClient(t, h)
	q, err := c.Queues(context.Background())
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	p := q["pulse"]
	if p.Name != "pulse" || p.QueueDepth != 5 || p.Capacity != 100 || p.EnqueueRate != 1.5 {
		t.Fatalf("pulse = %+v", p)
	}
}

func TestHealth(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	c := newTestClient(t, h)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("cpra_monitors_total 3\n"))
	})
	c := newTestClient(t, h)
	body, err := c.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if body != "cpra_monitors_total 3\n" {
		t.Fatalf("body = %q", body)
	}
}
