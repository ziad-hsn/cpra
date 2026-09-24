package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestDurableAPIsAuthenticateAndRemainReadOnly(t *testing.T) {
	c := runtimeconfig.Default()
	c.Storage.Mode = "memory"
	store, err := persistence.Open(context.Background(), c)
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
	rows := make([]fleetview.MonitorSummary, 2000)
	for n := range rows {
		rows[n] = fleetview.MonitorSummary{ID: uint32(n + 1), MonitorID: "stable", Name: "monitor", Status: "up", LastCheck: time.Now()}
	}
	s.holder.SetIndex(fleetview.NewIndex(rows))
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

func TestLegacyHistoryErrorsDoNotExposeStoragePaths(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	catalogPath := filepath.Join(cfg.Storage.Directory, "history", "catalog.json")
	if err := os.Remove(catalogPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(catalogPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.History().Expire(time.Now().UTC().Add(24 * time.Hour)); err == nil || !strings.Contains(err.Error(), cfg.Storage.Directory) {
		t.Fatal("fixture did not produce path-bearing filesystem error", err)
	}
	if _, err := store.History().Page("one", "", 100); !errors.Is(err, persistence.ErrHistoryUnavailable) || !strings.Contains(err.Error(), cfg.Storage.Directory) {
		t.Fatal("fixture did not preserve wrapped storage error", err)
	}
	s := newTestServer(t)
	s.cfg.Store = store
	s.cfg.AuthToken = "history-fixture"
	r := httptest.NewRequest("GET", "/api/v1/history?monitor_id=one", nil)
	r.Header.Set("Authorization", "Bearer history-fixture")
	w := httptest.NewRecorder()
	s.authMiddleware(apiMux(s)).ServeHTTP(w, r)
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"event history unavailable"}` {
		t.Fatal("public history error leaked internal cause", w.Code, w.Body.String())
	}
}
func TestLegacyHistoryRejectsInvalidRequestsWithoutEchoingInput(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := newTestServer(t)
	s.cfg.Store = store
	for _, path := range []string{"/api/v1/history", "/api/v1/history?monitor_id=one&cursor=PRIVATE-INVALID-CURSOR"} {
		w := httptest.NewRecorder()
		apiMux(s).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"invalid history request"}` {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
