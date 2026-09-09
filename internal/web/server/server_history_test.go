package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"cpra/internal/queue"
)

func TestQueuesHistory(t *testing.T) {
	s := newTestServer(t)
	s.queueHistory["pulse"].Record(queue.Stats{QueueDepth: 5, Capacity: 100})

	r := httptest.NewRequest("GET", "/api/v1/queues/history", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string][]queue.Sample[queue.Stats]
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp["pulse"]) != 1 {
		t.Fatalf("expected 1 pulse sample, got %d", len(resp["pulse"]))
	}
	if resp["pulse"][0].Value.QueueDepth != 5 {
		t.Fatalf("expected depth 5, got %d", resp["pulse"][0].Value.QueueDepth)
	}
}

func TestPoolsHistory(t *testing.T) {
	s := newTestServer(t)
	s.poolHistory["code"].Record(queue.WorkerPoolStats{RunningWorkers: 7})

	r := httptest.NewRequest("GET", "/api/v1/pools/history", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string][]queue.Sample[queue.WorkerPoolStats]
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp["code"]) != 1 {
		t.Fatalf("expected 1 code sample, got %d", len(resp["code"]))
	}
	if resp["code"][0].Value.RunningWorkers != 7 {
		t.Fatalf("expected 7 running workers, got %d", resp["code"][0].Value.RunningWorkers)
	}
}
