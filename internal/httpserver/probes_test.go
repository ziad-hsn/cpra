package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/cpractl/cli"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestObservationRequestsReceiveBoundedContext(t *testing.T) {
	const token = "read-deadline-fixture-token-01234567890123456789012345678901"
	for _, path := range []string{"/api/v1/readyz", "/api/v2/readyz", "/api/v1/state", "/api/v2/state", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			server := New(ServerConfig{AuthToken: token, ReadTimeout: time.Second}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
			handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > time.Second {
					t.Error("observation handler did not receive the configured deadline")
				}
				w.WriteHeader(http.StatusOK)
			}))
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			handler.ServeHTTP(httptest.NewRecorder(), request)

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			want, _ := ctx.Deadline()
			handler = server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, ok := r.Context().Deadline(); !ok || !got.Equal(want) {
					t.Error("observation replaced the caller's earlier deadline")
				}
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(ctx))
		})
	}
}

func TestObservationDeadlineDoesNotShortenWorkerPolls(t *testing.T) {
	server := New(ServerConfig{AuthToken: "read-deadline-fixture-token-01234567890123456789012345678901", ReadTimeout: time.Millisecond}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); ok {
			t.Error("read deadline applied to a worker poll")
		}
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v2/external-workers/poll", nil)
	request.Header.Set("Authorization", "Bearer read-deadline-fixture-token-01234567890123456789012345678901")
	handler.ServeHTTP(httptest.NewRecorder(), request)
}

func TestReadinessUsesRequestContext(t *testing.T) {
	for _, scenario := range []struct {
		name, path string
		management bool
	}{
		{"token-v1", "/api/v1/readyz", false},
		{"token-v2", "/api/v2/readyz", false},
		{"managed-readiness", "/api/v2/readyz", true},
		{"managed-state", "/api/v2/state", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := New(ServerConfig{AuthToken: "request-context-token-01234567890123456789012345678901"}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
			token := "request-context-token-01234567890123456789012345678901"
			if scenario.management {
				fixture := newManagementFixture(t, true)
				server, token = fixture.server, managementReaderToken
			}
			type requestKey struct{}
			key := requestKey{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key, "request-context"))
			seen, done, release := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
			server.cfg.Ready = func(ctx context.Context) bool {
				seen <- ctx
				select {
				case <-ctx.Done():
				case <-release:
				}
				return false
			}
			t.Cleanup(func() {
				cancel()
				close(release)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("request did not exit after readiness was released")
				}
			})
			request := httptest.NewRequest(http.MethodGet, "https://cpra.example"+scenario.path, nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler := server.authMiddleware(apiMux(server))
			go func() {
				defer close(done)
				handler.ServeHTTP(response, request)
			}()
			var observed context.Context
			select {
			case observed = <-seen:
				if observed.Value(key) != "request-context" {
					t.Fatal("readiness lost request context values")
				}
			case <-done:
				t.Fatalf("request exited before readiness: %d %s", response.Code, response.Body.String())
			case <-time.After(time.Second):
				t.Fatal("request did not reach readiness")
			}
			cancel()
			select {
			case <-done:
				if !errors.Is(observed.Err(), context.Canceled) || response.Code != http.StatusServiceUnavailable {
					t.Fatal("request cancellation did not stop readiness", observed.Err(), response.Code)
				}
			case <-time.After(time.Second):
				t.Fatal("readiness outlived request cancellation")
			}
		})
	}
}

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
			cfg := ServerConfig{Store: store, AuthToken: token, Ready: func(context.Context) bool { return ready.Load() }}
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
	for _, cfg := range []ServerConfig{{AuthToken: "probe-token-01234567890123456789012345678901"}, {}, {Addr: "127.0.0.1:0", AllowAnonymousLoopback: true}} {
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
	server := New(ServerConfig{AuthToken: "probe-token-01234567890123456789012345678901", Ready: func(context.Context) bool { return true }}, mkHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	handler := server.authMiddleware(apiMux(server))
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v2/healthz?extra=true", 400}, {"GET", "/api/v2/readyz?extra=true", 400}, {"POST", "/api/v2/healthz", 405}, {"POST", "/api/v2/readyz", 405}, {"GET", "/api/v2/state", 200}, {"POST", "/api/v2/state", 405}, {"GET", "/api/v2/monitors", 404}, {"POST", "/api/v2/monitors", 404}} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("Authorization", "Bearer probe-token-01234567890123456789012345678901")
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
