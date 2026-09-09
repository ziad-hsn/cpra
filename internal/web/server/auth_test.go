package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticatedBrowserAndBearerAPI(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = "local-fixture-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	handler := s.corsMiddleware(s.authMiddleware(next))
	for _, tc := range []struct {
		path, kind string
		code       int
	}{{"/", "", 401}, {"/", "basic", 200}, {"/api/v1/overview", "basic", 200}, {"/api/v1/overview", "bearer", 200}, {"/api/v1/overview", "wrong", 401}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		switch tc.kind {
		case "basic":
			req.SetBasicAuth("cpra", s.cfg.AuthToken)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+s.cfg.AuthToken)
		case "wrong":
			req.SetBasicAuth("cpra", "wrong")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("%s %s status %d", tc.path, tc.kind, rec.Code)
		}
		if tc.path == "/" && tc.kind == "" && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
			t.Fatal("no browser sign-in challenge")
		}
	}
	req := httptest.NewRequest("OPTIONS", "/api/v1/overview", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatal("auth blocked CORS preflight")
	}
}
func TestSamplerStopsAfterAConcurrentSample(t *testing.T) {
	s := newTestServer(t)
	s.startHistorySampler()
	s.stopHistorySampler()
	s.stopHistorySampler()
	s.wg.Wait()
}
