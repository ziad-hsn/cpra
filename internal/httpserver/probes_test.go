package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/cpractl/cli"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestTokenOnlyV2ProbesSDKAndCLI(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "token", true: "committed-verifier"}[committed], func(t *testing.T) {
			t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
			t.Setenv("CPRA_CA_FILE", "")
			const token = "probe-fixture-012345678901234567890123456"
			t.Setenv("CPRA_AUTH_TOKEN", token)
			config := runtimeconfig.Default()
			config.Storage.Mode = "memory"
			store, err := persistence.Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			var ready atomic.Bool
			ready.Store(true)
			cfg := ServerConfig{Store: store, AuthToken: token, Ready: ready.Load}
			if committed {
				digest := sha256.Sum256([]byte(token))
				cfg.AuthToken = "must-not-authorize"
				cfg.LegacyTokenSHA256 = hex.EncodeToString(digest[:])
				cfg.AuthenticationRequired = true
			}
			server := New(cfg, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
			// No controller, provider, executable job or target is constructed. Real HTTP
			// passes through the same authentication middleware as Server.Start.
			transport := httptest.NewServer(server.authMiddleware(apiMux(server)))
			t.Cleanup(transport.Close)
			sdk, err := cpra.New(cpra.Config{BaseURL: transport.URL, AuthToken: token, AllowInsecureHTTP: true, ReadAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sdk.CloseIdleConnections)
			run := func(name string) error {
				command := cli.NewRootCommand()
				command.SetOut(io.Discard)
				command.SetErr(io.Discard)
				command.SetArgs([]string{"--server", transport.URL, "--allow-insecure-http", name})
				return command.ExecuteContext(t.Context())
			}
			before := store.Status().CommittedIndex
			if response, err := sdk.Live(t.Context()); err != nil || !response.Data.Available || response.Data.GeneratedAt.IsZero() {
				t.Fatalf("SDK liveness: %v", err)
			}
			if response, err := sdk.Ready(t.Context()); err != nil || !response.Data.Available {
				t.Fatalf("SDK readiness: %v", err)
			}
			if err := run("health"); err != nil {
				t.Fatal(err)
			}
			if err := run("ready"); err != nil {
				t.Fatal(err)
			}
			ready.Store(false)
			if _, err := sdk.Ready(t.Context()); !errors.Is(err, cpra.ErrUnavailable) {
				t.Fatalf("SDK accepted unavailable readiness: %v", err)
			}
			if err := run("ready"); err == nil {
				t.Fatal("CLI accepted unavailable readiness")
			}
			if response, err := sdk.Live(t.Context()); err != nil || !response.Data.Available {
				t.Fatalf("readiness changed liveness: %v", err)
			}
			if err := run("health"); err != nil {
				t.Fatal(err)
			}
			if store.Status().CommittedIndex != before {
				t.Fatal("probes mutated persistence")
			}
			ready.Store(true)
			store.MarkUnavailable(errors.New("private-storage-error"))
			if _, err := sdk.Ready(t.Context()); !errors.Is(err, cpra.ErrUnavailable) || strings.Contains(err.Error(), "private-storage-error") {
				t.Fatalf("storage readiness not gated/sanitized: %v", err)
			}
			if response, err := sdk.Live(t.Context()); err != nil || !response.Data.Available {
				t.Fatalf("storage error changed liveness: %v", err)
			}
		})
	}
}

func TestTokenOnlyV2ProbesRequireExistingAuthentication(t *testing.T) {
	for _, cfg := range []ServerConfig{{AuthToken: "probe-token"}, {}, {Addr: "127.0.0.1:0", AllowAnonymousLoopback: true}} {
		server := New(cfg, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
		handler := server.authMiddleware(apiMux(server))
		for _, path := range []string{"/api/v2/healthz", "/api/v2/readyz"} {
			for _, credential := range []string{"", "wrong-token"} {
				request := httptest.NewRequest("GET", path, nil)
				request.RemoteAddr = "127.0.0.1:1000"
				if credential != "" {
					request.Header.Set("Authorization", "Bearer "+credential)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("probe bypassed authentication: %s %d", path, response.Code)
				}
			}
		}
	}
}

func TestTokenOnlyV2ProbesAreNarrowReadRoutes(t *testing.T) {
	server := New(ServerConfig{AuthToken: "probe-token", Ready: func() bool { return true }}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	handler := server.authMiddleware(apiMux(server))
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v2/healthz?extra=true", 400}, {"GET", "/api/v2/readyz?extra=true", 400}, {"POST", "/api/v2/healthz", 405}, {"POST", "/api/v2/readyz", 405}, {"GET", "/api/v2/state", 404}, {"GET", "/api/v2/monitors", 404}} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("Authorization", "Bearer probe-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("%s %s returned%d", tc.method, tc.path, response.Code)
		}
	}
}

func TestV2ProbesRetainManagementAuthorization(t *testing.T) {
	fixture := newManagementFixture(t, true)
	for _, path := range []string{"/api/v2/healthz", "/api/v2/readyz"} {
		for _, token := range []string{"", managementReaderToken} {
			response, _ := fixture.request(t, "GET", path, token, nil, nil)
			want := http.StatusOK
			if token == "" {
				want = http.StatusUnauthorized
			}
			if response.StatusCode != want {
				t.Fatalf("management probe authorization changed: %s %d", path, response.StatusCode)
			}
		}
		// Even the configured compatibility token cannot bypass the management
		// transport policy on these routes.
		request := httptest.NewRequest("GET", "http://cpra.example"+path, bytes.NewReader(nil))
		request.Header.Set("Authorization", "Bearer "+managementLegacyToken)
		response := httptest.NewRecorder()
		fixture.server.authMiddleware(apiMux(fixture.server)).ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("management transport policy bypassed: %d", response.Code)
		}
	}
}
