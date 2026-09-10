package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"cpra/internal/durable"
	"cpra/internal/runtimeconfig"
	"cpra/internal/web/snapshot"
)

func TestDurableAPIsAuthenticateAndRemainReadOnly(t *testing.T) {
	c := runtimeconfig.Default()
	c.Storage.Mode = "memory"
	store, err := durable.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := newTestServer(t)
	s.cfg.Store = store
	s.cfg.AuthToken = "secret-for-test"
	h := s.authMiddleware(apiMux(s))
	for _, path := range []string{"/api/v1/state", "/api/v1/slo", "/api/v1/history?monitor_id=m"} {
		for _, auth := range []bool{false, true} {
			r := httptest.NewRequest("GET", path, nil)
			if auth {
				r.Header.Set("Authorization", "Bearer secret-for-test")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			expected := 401
			if auth {
				expected = 200
			}
			if w.Code != expected {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer secret-for-test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatal("new API accepted POST", path, w.Code)
		}
	}
}

func TestIncrementalIndexPreservesNumericRoutesAndBoundsPages(t *testing.T) {
	s := newTestServer(t)
	rows := make([]snapshot.MonitorSummary, 2000)
	for n := range rows {
		rows[n] = snapshot.MonitorSummary{ID: uint32(n + 1), MonitorID: "stable", Name: "monitor", Status: "up", LastCheck: time.Now()}
	}
	s.holder.SetIndex(snapshot.NewIndex(rows))
	for _, path := range []string{"/api/v1/monitors?size=99999", "/api/v1/monitors/1", "/api/v1/overview"} {
		w := httptest.NewRecorder()
		apiMux(s).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if path == "/api/v1/monitors?size=99999" {
			var v monitorsResp
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if v.Total != 2000 || len(v.Monitors) != 500 {
				t.Fatal(v.Total, len(v.Monitors))
			}
		}
	}
}
