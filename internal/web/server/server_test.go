package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpra/internal/web/snapshot"
)

func mkHolder() *snapshot.Holder {
	h := snapshot.NewHolder()
	now := time.Now()
	h.Set(&snapshot.StatsSnapshot{
		Generated:   now,
		Total:       3,
		Disabled:    1,
		ByStatus:    map[string]int{"up": 1, "down": 1, "incident": 1, "disabled": 1},
		ByPulseType: map[string]int{"http": 2, "tcp": 1},
		ByCode:      map[string]int{"red": 1},
		Monitors: []snapshot.MonitorSummary{
			{ID: 1, Name: "alpha", PulseType: "http", Status: "up"},
			{ID: 2, Name: "beta", PulseType: "tcp", Status: "down", PendingCode: "red"},
			{ID: 3, Name: "gamma", PulseType: "http", Status: "incident", Incident: true},
		},
		ByID: map[uint32]int{1: 0, 2: 1, 3: 2},
	})
	return h
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(ServerConfig{Addr: "localhost:0"}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{QueueCapacity: 65536})
}

// apiMux builds a ServeMux with only the API routes registered (avoids needing
// the embedded assets FS for the SPA route).
func apiMux(s *Server) *http.ServeMux {
	m := http.NewServeMux()
	s.registerAPI(m)
	return m
}

func TestOverview(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/v1/overview", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var o overviewResp
	if err := json.NewDecoder(w.Body).Decode(&o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if o.Total != 3 || o.Disabled != 1 || o.ByStatus["down"] != 1 {
		t.Fatalf("overview = %+v", o)
	}
	if o.UpPercent <= 0 {
		t.Fatalf("up_percent = %v", o.UpPercent)
	}
}

func TestMonitorsListFiltering(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/v1/monitors?status=down&size=10", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var m monitorsResp
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Total != 1 || len(m.Monitors) != 1 || m.Monitors[0].Name != "beta" {
		t.Fatalf("monitors = %+v", m)
	}
}

func TestMonitorDetailNotFound(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/v1/monitors/9999", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestMonitorDetailFound(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/v1/monitors/2", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var m snapshot.MonitorSummary
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.ID != 2 || m.Name != "beta" {
		t.Fatalf("detail = %+v", m)
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/v1/healthz", nil)
	w := httptest.NewRecorder()
	apiMux(s).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("body = %s", w.Body.String())
	}
}
