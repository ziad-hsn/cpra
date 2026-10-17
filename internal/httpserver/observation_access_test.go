package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestObservationFileActionsRetainOriginalIncarnations(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Now().UTC()
	config := persistence.Monitor{ID: "file-service", Name: "File service", Revision: "file-revision", Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Unhealthy: 1, Healthy: 1, Endpoints: map[string]int{"red": 1}}}
	apply := func(command persistence.Command) persistence.Monitor {
		at = at.Add(time.Millisecond)
		command.At = at
		command.MonitorID = config.ID
		command.Revision = config.Revision
		results, err := store.Submit(t.Context(), []persistence.Command{command})
		if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
			t.Fatalf("%s failed: %+v %v", command.Kind, results, err)
		}
		return *results[0].Monitor
	}
	m := apply(persistence.Command{Kind: "configure", Config: &config})
	m = apply(persistence.Command{Kind: "pulse", Generation: m.Generation + 1, CheckControlRevision: m.ControlRevision, Outcome: "failure"})
	var original persistence.Action
	for _, a := range m.Actions {
		if a.Kind == "code" {
			original = a
			break
		}
	}
	if original.ID == "" {
		t.Fatal("missing original intent")
	}
	apply(persistence.Command{Kind: "start", ActionID: original.ID})
	apply(persistence.Command{Kind: "remove"})
	m = apply(persistence.Command{Kind: "configure", Config: &config})
	m = apply(persistence.Command{Kind: "pulse", Generation: m.Generation + 1, CheckControlRevision: m.ControlRevision, Outcome: "failure"})
	if !m.Actions[original.ID].Held() || m.FileIncarnation == "" {
		t.Fatal("fixture lost original hold or new incarnation")
	}
	s := newTestServer(t)
	s.cfg.Store = store
	s.cfg.AuthToken = managementReaderToken
	server := httptest.NewServer(s.authMiddleware(apiMux(s)))
	t.Cleanup(server.Close)
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: managementReaderToken, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	page, err := client.Actions.List(t.Context(), cpra.ListOptions{MonitorID: config.ID})
	if err != nil || len(page.Data.Items) != 2 {
		t.Fatal(page, err)
	}
	incarnations := map[string]bool{}
	for _, item := range page.Data.Items {
		got, err := client.Actions.Get(t.Context(), item.ID)
		if err != nil || got.Data.IncarnationUID == "" || got.Data.IncarnationUID != item.IncarnationUID {
			t.Fatal("action identity absent or inconsistent", got, err)
		}
		incarnations[item.IncarnationUID] = true
		if item.ID == original.ID && !item.Held {
			t.Fatal("observation changed original hold")
		}
	}
	if len(incarnations) != 2 {
		t.Fatal("remove/re-add actions share incarnation", incarnations)
	}
}

func TestObservationModeV2ReadsWithoutCatalog(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared-token", true: "anonymous-loopback"}[anonymous], func(t *testing.T) {
			cfg := runtimeconfig.Default()
			cfg.Storage.Mode = "memory"
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			s := newTestServer(t)
			s.cfg.Store, s.cfg.AllowAnonymousLoopback = store, anonymous
			s.cfg.Ready = func(context.Context) bool { return true }
			token := "observation-fixture-0123456789012345678901"
			if anonymous {
				token = ""
			}
			s.cfg.AuthToken = token
			s.pulseQueue = &observationQueue{value: queue.Stats{Capacity: 10, QueueDepth: 5}}
			h := s.corsMiddleware(s.authMiddleware(apiMux(s)))
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: token, AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			access, err := client.DiscoverAccess(t.Context())
			if err != nil || access.Data.Mode != api.AccessDiscoveryModeObservation || access.Data.AuthenticationRequired == anonymous {
				t.Fatal(access, err)
			}
			for _, call := range []func() error{
				func() error { _, e := client.State(t.Context()); return e },
				func() error { _, e := client.Ready(t.Context()); return e },
				func() error { _, e := client.Live(t.Context()); return e },
				func() error { _, e := client.SLO.Get(t.Context()); return e },
				func() error { _, e := client.Metrics(t.Context()); return e },
				func() error { _, e := client.Version(t.Context()); return e },
				func() error { _, e := client.Incidents.List(t.Context(), cpra.ListOptions{}); return e },
				func() error { _, e := client.Actions.List(t.Context(), cpra.ListOptions{}); return e },
				func() error { _, e := client.History(t.Context(), cpra.ListOptions{MonitorID: "service"}); return e },
			} {
				if err := call(); err != nil {
					t.Fatal(err)
				}
			}
			config, err := client.RuntimeConfig(t.Context())
			if err != nil || !config.Data.ReadOnly {
				t.Fatal(config, err)
			}
			first, err := client.Queues.List(t.Context(), cpra.ListOptions{Limit: 1})
			if err != nil || first.Data.NextCursor == "" {
				t.Fatal(first, err)
			}
			second, err := client.Queues.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
			if err != nil || first.Data.Snapshot != second.Data.Snapshot || len(second.Data.Items) != 1 || first.Data.Items[0].Name == second.Data.Items[0].Name {
				t.Fatal(second, err)
			}
			capabilities, err := client.Capabilities(t.Context())
			if err != nil || slices.Contains(capabilities.Data.Resources, "Credential") || slices.Contains(capabilities.Data.ResourceOperations["Action"], "ReviewAction") {
				t.Fatal(capabilities, err)
			}
			for _, kind := range capabilities.Data.Resources {
				if _, err := client.Explain(t.Context(), kind); err != nil {
					t.Fatal("advertised schema unavailable", kind, err)
				}
			}
			if s.cfg.Management != nil || s.managementHTTP.catalog != nil {
				t.Fatal("read path created a writable catalog")
			}
			before := store.Status().CommittedIndex
			for _, path := range []string{"/api/v2/monitors", "/api/v2/operations", "/api/v2/actions/one/review", "/api/v2/monitors/one/recover", "/api/v2/monitors/one/ignore"} {
				r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, nil)
				r.RemoteAddr = "127.0.0.1:1234"
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 404 && w.Code != 405 {
					t.Fatal("write route available", path, w.Code)
				}
			}
			if store.Status().CommittedIndex != before {
				t.Fatal("read or denied write changed durable state")
			}
			// Cursor possession and loopback bind configuration do not authenticate
			// a request from an unapproved Host, Origin or peer.
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.Host = "attacker.invalid" },
				func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") },
				func(r *http.Request) { r.RemoteAddr = "192.0.2.10:1234" },
			} {
				if !anonymous {
					break
				}
				r := httptest.NewRequest("GET", "http://localhost/api/v2/state", nil)
				r.RemoteAddr = "127.0.0.1:1234"
				mutate(r)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 401 && w.Code != 403 {
					t.Fatal("host/origin/peer bypass", w.Code)
				}
			}
			if !anonymous {
				r := httptest.NewRequest("GET", "/api/v2/queues?cursor="+first.Data.NextCursor, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 401 {
					t.Fatal("cursor authenticated request", w.Code)
				}
			}
			// A direct route call without the middleware's identity also fails.
			w := httptest.NewRecorder()
			apiMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/v2/state", nil))
			if w.Code != 401 {
				t.Fatal("missing middleware identity admitted", w.Code)
			}
		})
	}
}

func TestObservationModeMissingStorageIsUnavailable(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AllowAnonymousLoopback = true
	h := s.authMiddleware(apiMux(s))
	r := httptest.NewRequest("GET", "http://localhost/api/v2/actions", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var problem api.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || w.Code != 503 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
}
