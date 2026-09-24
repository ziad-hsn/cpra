package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/ziad-hsn/cpra/internal/controller"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCommittedLegacyAuthorityNeverFallsBackToPlaintext(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = "old-file-token"
	s.cfg.AuthenticationRequired = true
	digest := sha256.Sum256([]byte("committed-token"))
	s.cfg.LegacyTokenSHA256 = hex.EncodeToString(digest[:])
	for _, empty := range []bool{false, true} {
		if empty {
			s.cfg.LegacyTokenSHA256 = ""
		}
		handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
		for _, token := range []string{"committed-token", "old-file-token", ""} {
			for _, basic := range []bool{false, true} {
				r := httptest.NewRequest("GET", "/api/v1/state", nil)
				if basic {
					r.SetBasicAuth("cpra", token)
				} else {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := 401
				if token == "committed-token" && !empty {
					want = 200
				}
				if w.Code != want {
					t.Fatalf("empty=%v basic=%v got %d want %d", empty, basic, w.Code, want)
				}
			}
		}
	}
}

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

func TestServerStartFailsClosedWithoutCredentials(t *testing.T) {
	previous := controller.SystemLogger
	controller.SystemLogger = controller.NewLogger("auth-test", false)
	t.Cleanup(func() { controller.SystemLogger = previous })
	s := newTestServer(t)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/api/v1/overview", "/api/v1/history?monitor_id=one", "/metrics", "/api/v2/self"} {
		response, err := client.Get("http://" + s.srv.Addr + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("unconfigured authentication opened endpoint", path, response.StatusCode)
		}
	}
}
func TestExplicitAnonymousAccessRequiresLoopbackAndNoAuthority(t *testing.T) {
	previous := controller.SystemLogger
	controller.SystemLogger = controller.NewLogger("auth-test", false)
	t.Cleanup(func() { controller.SystemLogger = previous })
	s := newTestServer(t)
	s.cfg.AllowAnonymousLoopback = true
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + s.srv.Addr + "/api/v1/overview")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("explicit loopback compatibility unavailable", response.StatusCode)
	}
	for _, remote := range []string{"192.0.2.1:4321", "invalid", ""} {
		req := httptest.NewRequest("GET", "/api/v1/overview", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.authMiddleware(apiMux(s)).ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal("non-loopback peer bypassed authentication", remote, rec.Code)
		}
	}
	for _, cfg := range []ServerConfig{
		{Addr: ":0", AllowAnonymousLoopback: true}, {Addr: "0.0.0.0:0", AllowAnonymousLoopback: true}, {Addr: "[::]:0", AllowAnonymousLoopback: true},
		{Addr: "example.invalid:0", AllowAnonymousLoopback: true}, {Addr: "127.0.0.1:0", AllowAnonymousLoopback: true, AuthenticationRequired: true},
		{Addr: "127.0.0.1:0", AllowAnonymousLoopback: true, AuthToken: "configured-token"},
	} {
		invalid := New(cfg, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
		if err := invalid.Start(); err == nil {
			invalid.Stop()
			t.Fatal("unsafe anonymous configuration accepted", cfg.Addr)
		}
	}
}
