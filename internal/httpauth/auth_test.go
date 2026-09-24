package httpauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// These deterministic values are test fixtures, not generated production tokens.
const readerToken = "reader-fixture-012345678901234567890123456789"
const operatorToken = "operator-fixture-012345678901234567890123456"
const legacyToken = "legacy-fixture-012345678901234567890123456789"
const rotatedToken = "rotated-fixture-01234567890123456789012345678"

func verifier(t *testing.T, token string) string {
	t.Helper()
	digest, err := HashToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
func configFixture(t *testing.T) Config {
	t.Helper()
	return Config{Principals: []Principal{{ID: "reader-one", Role: Reader, TokenSHA256: verifier(t, readerToken)}, {ID: "operator-one", Role: Operator, TokenSHA256: verifier(t, operatorToken)}}, LegacyTokenSHA256: verifier(t, legacyToken)}
}
func newFixture(t *testing.T, config Config) *Authorizer {
	t.Helper()
	a, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func requestFixture(method, path, token string) *http.Request {
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, "https://cpra.example"+path, nil)
	} else {
		r = httptest.NewRequest(method, "https://cpra.example"+path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestNamedPrincipalsAndExactPermissions(t *testing.T) {
	a := newFixture(t, configFixture(t))
	for _, tc := range []struct {
		name, method, path, op, token, id string
		want                              error
	}{
		{"reader get", http.MethodGet, "/api/v2/monitors", "ListMonitors", readerToken, "reader-one", nil},
		{"operator create", http.MethodPost, "/api/v2/monitors", "CreateMonitor", operatorToken, "operator-one", nil},
		{"reader write", http.MethodPost, "/api/v2/monitors", "CreateMonitor", readerToken, "", ErrForbidden},
		{"legacy read", http.MethodGet, "/api/v2/self", "GetAccess", legacyToken, LegacyPrincipalID, nil},
		{"legacy write", http.MethodPost, "/api/v2/monitors", "CreateMonitor", legacyToken, "", ErrForbidden},
		{"unknown", http.MethodGet, "/api/v2/self", "GetAccess", rotatedToken, "", ErrUnauthorized},
		{"missing", http.MethodGet, "/api/v2/self", "GetAccess", "", "", ErrUnauthorized},
		{"case sensitive", http.MethodGet, "/api/v2/self", "getAccess", operatorToken, "", ErrForbidden},
		{"wrong method", http.MethodPost, "/api/v2/self", "GetAccess", operatorToken, "", ErrForbidden},
		{"wrong route", http.MethodGet, "/api/v2/monitors", "GetAccess", operatorToken, "", ErrForbidden},
		{"path escape", http.MethodGet, "/api/v2/monitors/a/b", "GetMonitor", operatorToken, "", ErrForbidden},
		{"worker credentials separate", http.MethodPost, "/api/v2/external-workers/start", "WorkerStart", operatorToken, "", ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, err := a.Authorize(requestFixture(tc.method, tc.path, tc.token), tc.op)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if err != nil {
				var typed *Error
				if !errors.As(err, &typed) || typed.StatusCode() < 400 {
					t.Fatal("missing typed auth error")
				}
				if access.PrincipalID != "" {
					t.Fatal("denial returned identity")
				}
				return
			}
			if access.PrincipalID != tc.id || !slices.Contains(access.Permissions, tc.op) {
				t.Fatalf("invalid access observation %+v", access)
			}
			if access.Role == Reader && slices.Contains(access.Permissions, "CreateMonitor") {
				t.Fatal("reader acquired write permission")
			}
		})
	}
}

func TestBasicNeverUsesManagementPrivileges(t *testing.T) {
	a := newFixture(t, configFixture(t))
	for _, token := range []string{readerToken, operatorToken, legacyToken} {
		read := requestFixture(http.MethodGet, "/api/v2/self", "")
		read.SetBasicAuth("cpra", token)
		_, err := a.Authorize(read, "GetAccess")
		if token == legacyToken && err != nil {
			t.Fatal(err)
		}
		if token != legacyToken && !errors.Is(err, ErrUnauthorized) {
			t.Fatal("management token accepted as Basic")
		}
		write := requestFixture(http.MethodPost, "/api/v2/monitors", "")
		write.SetBasicAuth("cpra", token)
		if _, err := a.Authorize(write, "CreateMonitor"); err == nil {
			t.Fatal("Basic write accepted")
		}
	}
}

func TestTransportOriginAndContentType(t *testing.T) {
	config := configFixture(t)
	config.TrustedProxy = &ProxyConfig{PublicOrigin: "https://cpra.example"}
	a := newFixture(t, config)
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		valid  bool
	}{
		{"direct TLS", func(*http.Request) {}, true},
		{"same origin", func(r *http.Request) { r.Header.Set("Origin", "https://CPRA.example:443") }, true},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, false},
		{"opaque origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, false},
		{"multiple origins", func(r *http.Request) {
			r.Header.Add("Origin", "https://cpra.example")
			r.Header.Add("Origin", "https://evil.example")
		}, false},
		{"clear HTTP read", func(r *http.Request) { r.TLS = nil }, false},
		{"spoofed remote forwarding", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "192.0.2.2:123"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, false},
		{"trusted loopback proxy", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "127.0.0.1:123"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", "https://cpra.example")
		}, true},
		{"trusted IPv6", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "[::1]:123"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, true},
		{"chained forwarding", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "127.0.0.1:123"
			r.Header.Set("X-Forwarded-Proto", "https, http")
		}, false},
		{"duplicate forwarding", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "127.0.0.1:123"
			r.Header.Add("X-Forwarded-Proto", "https")
			r.Header.Add("X-Forwarded-Proto", "https")
		}, false},
		{"forwarded host cannot set origin", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "127.0.0.1:123"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("Origin", "https://evil.example")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFixture(http.MethodGet, "/api/v2/self", operatorToken)
			tc.change(r)
			_, err := a.Authorize(r, "GetAccess")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	noProxy := newFixture(t, configFixture(t))
	r := requestFixture(http.MethodGet, "/api/v2/self", operatorToken)
	r.TLS = nil
	r.RemoteAddr = "127.0.0.1:123"
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := noProxy.Authorize(r, "GetAccess"); !errors.Is(err, ErrForbidden) {
		t.Fatal("unconfigured proxy trusted")
	}
	for _, media := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/merge-patch+json"} {
		r := requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken)
		r.Header.Set("Content-Type", media)
		if _, err := a.Authorize(r, "CreateMonitor"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("accepted create content type %q", media)
		}
	}
	patch := requestFixture(http.MethodPatch, "/api/v2/monitors/m", operatorToken)
	patch.Header.Set("Content-Type", "application/merge-patch+json; charset=utf-8")
	if _, err := a.Authorize(patch, "PatchMonitor"); err != nil {
		t.Fatal(err)
	}
}

func TestRevocationAndConfigurationOwnership(t *testing.T) {
	config := configFixture(t)
	a := newFixture(t, config)
	config.Principals[1].Role = Reader // New has copied every principal.
	req := requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken)
	if _, err := a.Authorize(req, "CreateMonitor"); err != nil {
		t.Fatal("caller mutated active policy")
	}
	before := a.Generation()
	config = configFixture(t)
	config.Principals[1].TokenSHA256 = verifier(t, rotatedToken)
	if err := a.Replace(config); err != nil {
		t.Fatal(err)
	}
	if a.Generation() <= before {
		t.Fatal("policy generation not advanced")
	}
	if _, err := a.Authorize(req, "CreateMonitor"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("replaced token remains authorized")
	}
	config.Principals[1].Revoked = true
	if err := a.Replace(config); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(requestFixture(http.MethodPost, "/api/v2/monitors", rotatedToken), "CreateMonitor"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked token remains authorized")
	}
	config.Principals = nil
	config.LegacyTokenSHA256 = ""
	if a.Replace(config) == nil {
		t.Fatal("invalid replacement accepted")
	}
	if _, err := a.Authorize(requestFixture(http.MethodGet, "/api/v2/self", readerToken), "GetAccess"); err != nil {
		t.Fatal("invalid replacement damaged old policy")
	}
}

func TestAdmissionAndReplacementSerialize(t *testing.T) {
	a := newFixture(t, configFixture(t))
	entered := make(chan struct{})
	release := make(chan struct{})
	admitted := make(chan error, 1)
	go func() {
		admitted <- a.WithAdmission(requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken), "CreateMonitor", func(api.AccessInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	config := configFixture(t)
	config.Principals[1].Revoked = true
	replaced := make(chan error, 1)
	go func() { replaced <- a.Replace(config) }()
	select {
	case <-replaced:
		t.Fatal("revocation passed an active synchronous admission")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	called := false
	err := a.WithAdmission(requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken), "CreateMonitor", func(api.AccessInfo) error { called = true; return nil })
	if !errors.Is(err, ErrUnauthorized) || called {
		t.Fatal("revoked admission reached callback")
	}
}

func TestBoundsValidationAndRedaction(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Principals[0].TokenSHA256 = "sensitive-bad-verifier" },
		func(c *Config) { c.Principals[1].TokenSHA256 = c.Principals[0].TokenSHA256 },
		func(c *Config) { c.Principals[1].ID = c.Principals[0].ID },
		func(c *Config) { c.Principals[0].Role = "worker" },
		func(c *Config) { c.Principals[0].ID = "invalid\nidentity" },
		func(c *Config) { c.LegacyTokenSHA256 = c.Principals[1].TokenSHA256 },
		func(c *Config) { c.TrustedProxy = &ProxyConfig{PublicOrigin: "http://cpra.example"} },
		func(c *Config) { c.Principals = make([]Principal, MaxPrincipals+1) },
	} {
		config := configFixture(t)
		change(&config)
		err := ValidateConfig(config)
		if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "sensitive-bad-verifier") {
			t.Fatalf("invalid or unredacted config error %v", err)
		}
	}
	config := configFixture(t)
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), config.Principals[0].TokenSHA256) {
		t.Fatal("verifier serialized into JSON diagnostics")
	}
	a := newFixture(t, config)
	for _, header := range []string{"Bearer " + strings.Repeat("sensitive", MaxAuthorizationBytes), "Bearer bad token", "Bearer\t" + operatorToken, "unknown " + operatorToken} {
		r := requestFixture(http.MethodGet, "/api/v2/self", "")
		r.Header.Set("Authorization", header)
		_, err := a.Authorize(r, "GetAccess")
		if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), operatorToken) || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("invalid credentials accepted or echoed")
		}
	}
	r := requestFixture(http.MethodGet, "/api/v2/self", operatorToken)
	r.Header.Add("Authorization", "Bearer "+readerToken)
	if _, err := a.Authorize(r, "GetAccess"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("multiple credentials accepted")
	}
	access, err := a.Authorize(requestFixture(http.MethodGet, "/api/v2/self", readerToken), "GetAccess")
	if err != nil {
		t.Fatal(err)
	}
	access.Permissions[0] = "CreateMonitor"
	next, err := a.Authorize(requestFixture(http.MethodGet, "/api/v2/self", readerToken), "GetAccess")
	if err != nil || slices.Contains(next.Permissions, "CreateMonitor") {
		t.Fatal("access observation aliases policy")
	}
}

func TestConcurrentPolicyReplacement(t *testing.T) {
	config := configFixture(t)
	a := newFixture(t, config)
	r := requestFixture(http.MethodGet, "/api/v2/self", readerToken)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 32 {
				if _, err := a.Authorize(r, "GetAccess"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for range 4 {
		if err := a.Replace(config); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
