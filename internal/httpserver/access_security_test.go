package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestAnonymousHostAndOriginAreLoopbackBound(t *testing.T) {
	for _, test := range []struct {
		host, origin string
		allowed      bool
	}{
		{"localhost:8060", "", true}, {"127.0.0.1:8060", "http://127.0.0.1:8060", true}, {"[::1]:8060", "http://[::1]:8060", true},
		{"rebind.example:8060", "", false}, {"localhost.attacker.example", "", false}, {"127.0.0.1:8060", "http://attacker.example", false},
		{"127.0.0.1:8060", "http://127.0.0.1:9000", false}, {"localhost:8060", "null", false}, {"localhost:8060", "http://user@localhost:8060", false},
		{"localhost:bad", "", false}, {"localhost:8060", "http://localhost:8060/?secret=ignored", false},
	} {
		t.Run(test.host+"/"+test.origin, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			r.Host, r.RemoteAddr = test.host, "127.0.0.1:4321"
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			called := false
			h := LoopbackHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) }))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called != test.allowed || !test.allowed && w.Code != 403 {
				t.Fatal("local diagnostics accepted hostile origin", w.Code, called)
			}
		})
	}
}

func TestShortPresentedTokenCannotUseCommittedVerifier(t *testing.T) {
	const token = "short-token"
	digest := sha256.Sum256([]byte(token))
	verifier := hex.EncodeToString(digest[:])
	auth, err := httpauth.New(httpauth.Config{Principals: []httpauth.Principal{{ID: "operator", Role: httpauth.Operator, TokenSHA256: verifier}}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://localhost/api/v2/self", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, err := auth.Authorize(r, "GetAccess"); err == nil {
		t.Fatal("short named token accepted")
	}
	s := newTestServer(t)
	s.cfg.LegacyTokenSHA256 = verifier
	w := httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	r.SetBasicAuth("cpra", token)
	s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("short observation credential admitted") })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("short observation credential did not fail authentication", w.Code)
	}
}

func TestFailedAuthThrottlingIsBoundedAndExpires(t *testing.T) {
	var throttle authThrottle
	at := time.Now()
	for n := 0; n < maxAuthSources+100; n++ {
		for range maxAuthFailures {
			throttle.failed(fmt.Sprint(n), at)
		}
	}
	if len(throttle.sources) != maxAuthSources || !throttle.blocked("0", at) || !throttle.blocked("additional", at) {
		t.Fatal("failed authentication state exceeded its bound")
	}
	if throttle.blocked("additional", at.Add(authFailureWindow+time.Second)) {
		t.Fatal("expired authentication failures did not release capacity")
	}
	called := 0
	h := failedAuthenticationLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(401) }))
	for n := 0; n <= maxAuthFailures; n++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://localhost/api/v2/self", nil))
		if n == maxAuthFailures && (w.Code != 401 || w.Header().Get("Retry-After") != "10") {
			t.Fatal("throttle hold unavailable")
		}
	}
	if called != maxAuthFailures {
		t.Fatal("blocked authentication reached the expensive handler", called)
	}
}

func TestSecurityHeadersPermitParserWithoutScriptEvaluation(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	for _, clause := range []string{"frame-ancestors 'none'", "object-src 'none'", "worker-src 'self' blob:", "'wasm-unsafe-eval'"} {
		if !strings.Contains(csp, clause) {
			t.Fatal("required browser boundary absent", clause)
		}
	}
	if strings.Contains(csp, "'unsafe-eval'") || w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("dashboard security headers incorrect")
	}
}

func TestAccessDiscoveryAndCanonicalResourceExplanations(t *testing.T) {
	f := newManagementFixture(t, true)
	mode, err := f.sdk.DiscoverAccess(t.Context())
	if err != nil || mode.Data.Mode != api.AccessDiscoveryModeManagement || !mode.Data.AuthenticationRequired {
		t.Fatal("management mode unavailable", err)
	}
	v, err := f.sdk.Version(t.Context())
	if err != nil || v.Data.Version == "" || v.Data.StorageFormat == 0 || v.Data.GoVersion == "" {
		t.Fatal("version identity unavailable", err)
	}
	for _, kind := range []string{"Monitor", "NotificationEndpoint", "NotificationGroup", "Credential", "Recipient", "Incident", "Action", "Operation"} {
		explanation, err := f.sdk.Explain(t.Context(), kind)
		if err != nil || explanation.Data.Resource != kind || !strings.Contains(string(explanation.Data.Schema), "#/$defs/"+kind) || strings.Contains(string(explanation.Data.Schema), "#/components/schemas/") {
			t.Fatal("canonical schema unavailable", kind, err)
		}
	}
	s := newTestServer(t)
	s.cfg.AllowAnonymousLoopback = true
	r := httptest.NewRequest(http.MethodGet, "/api/v2/access", nil)
	r.Host, r.RemoteAddr = "localhost:8060", "127.0.0.1:4321"
	w := httptest.NewRecorder()
	s.authMiddleware(apiMux(s)).ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"observation"`) || !strings.Contains(w.Body.String(), `"authenticationRequired":false`) {
		t.Fatal("observation discovery unavailable", w.Code, w.Body.String())
	}
}
