package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func waitCatalog(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("catalog projection did not reach the expected state")
}

func managedResource(kind, id string, spec any) api.Resource {
	raw, _ := json.Marshal(spec)
	return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id, Name: &id}, Spec: raw}
}

func managedMonitor(id, target string, enabled bool) api.Resource {
	raw, _ := json.Marshal(map[string]string{"url": target})
	return managedResource("Monitor", id, api.MonitorSpec{Enabled: api.Pointer(enabled), Check: api.CheckSpec{Interval: "50ms", Timeout: "2s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: raw}}})
}

func commitManaged(t *testing.T, c *management.Catalog, r api.Resource, expected string) management.MutationResult {
	t.Helper()
	p, err := c.Prepare(context.Background(), r, expected, expected == "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.CommitAs(context.Background(), p, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func managedStore(t *testing.T, cfg runtimeconfig.Config) (*management.Catalog, *persistence.Store) {
	t.Helper()
	s, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	w, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(w)
	if err != nil {
		t.Fatal(err)
	}
	c, err := management.NewCatalog(s, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, s
}

func managedController(t *testing.T, settings runtimeconfig.Config, catalog *management.Catalog, store *persistence.Store) *Controller {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Store, cfg.Catalog, cfg.Runtime = store, catalog, settings
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
	cfg.WorkerConfig.ResultBatchTimeout = time.Millisecond
	cfg.WorkerConfig.DrainTimeout = 3 * time.Second
	c := NewController(cfg)
	if err := c.LoadCatalog(context.Background()); err != nil {
		c.Stop()
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		c.Stop()
		t.Fatal(err)
	}
	return c
}

func TestCatalogControllerCreateEditDisableDeleteAndRestart(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var oldCalls, newCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			oldCalls.Add(1)
		} else {
			newCalls.Add(1)
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	c := managedController(t, settings, catalog, store)
	defer func() {
		if c != nil {
			c.Stop()
		}
		if store != nil {
			_ = store.Close()
		}
	}()
	created := commitManaged(t, catalog, managedMonitor("editable", target.URL+"/old", true), "")
	waitCatalog(t, func() bool { m, ok := store.Get("editable"); return ok && m.TotalChecks >= 2 && oldCalls.Load() >= 2 })
	uid := created.Resource.Metadata.UID
	updated := created.Resource
	updated.Spec = managedMonitor("editable", target.URL+"/new", true).Spec
	changed := commitManaged(t, catalog, updated, updated.Metadata.ResourceVersion)
	waitCatalog(t, func() bool { return newCalls.Load() >= 2 })
	waitCatalog(t, func() bool {
		r, e := catalog.Operation(context.Background(), changed.Operation.ID)
		return e == nil && (r.Applied != nil && *r.Applied == 1)
	})
	oldAfter := oldCalls.Load()
	time.Sleep(150 * time.Millisecond)
	if oldCalls.Load() != oldAfter {
		t.Fatal("superseded check target kept executing")
	}
	updated = changed.Resource
	updated.Spec = managedMonitor("editable", target.URL+"/new", false).Spec
	disabled := commitManaged(t, catalog, updated, updated.Metadata.ResourceVersion)
	waitCatalog(t, func() bool { m, ok := store.Get("editable"); return ok && !m.Policy.Enabled })
	waitCatalog(t, func() bool {
		r, e := catalog.Operation(context.Background(), disabled.Operation.ID)
		return e == nil && (r.Applied != nil && *r.Applied == 1)
	})
	stoppedAt := newCalls.Load()
	time.Sleep(150 * time.Millisecond)
	if newCalls.Load() != stoppedAt {
		t.Fatal("disabled monitor still dispatched")
	}
	c.Stop()
	c = nil
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	catalog, store = managedStore(t, settings)
	c = managedController(t, settings, catalog, store)
	m, _ := store.Get("editable")
	if m.CatalogUID != uid || m.Policy.Enabled {
		t.Fatal("restart lost managed incarnation or disablement")
	}
	resource, err := catalog.Get(context.Background(), "Monitor", "editable")
	if err != nil {
		t.Fatal(err)
	}
	p, err := catalog.PrepareDelete(context.Background(), "Monitor", "editable", resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := catalog.CommitAs(context.Background(), p, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	waitCatalog(t, func() bool { m, ok := store.Get("editable"); return ok && m.Removed })
	waitCatalog(t, func() bool {
		r, e := catalog.Operation(context.Background(), deleted.Operation.ID)
		return e == nil && (r.Applied != nil && *r.Applied == 1)
	})
	if c.SnapshotHolder().Index().Overview().Total != 0 {
		t.Fatal("deleted monitor remained in the dashboard projection")
	}
	recreated := commitManaged(t, catalog, managedMonitor("editable", target.URL+"/new", true), "")
	if recreated.Resource.Metadata.UID == uid {
		t.Fatal("recreated resource reused incarnation")
	}
	waitCatalog(t, func() bool {
		m, ok := store.Get("editable")
		return ok && m.CatalogUID == recreated.Resource.Metadata.UID && m.Generation > 0
	})
}

func TestCatalogControllerQueuedChecksCannotUseSupersededTarget(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var forbidden, replacement atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	var once, saw sync.Once
	defer once.Do(func() { close(release) })
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/block":
			saw.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/forbidden":
			forbidden.Add(1)
		case "/replacement":
			replacement.Add(1)
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	block := managedMonitor("blocking", target.URL+"/block", true)
	var spec api.MonitorSpec
	_ = json.Unmarshal(block.Spec, &spec)
	spec.Check.Timeout = "10s"
	block.Spec, _ = json.Marshal(spec)
	commitManaged(t, catalog, block, "")
	c := managedController(t, settings, catalog, store)
	defer c.Stop()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker did not start")
	}
	old := commitManaged(t, catalog, managedMonitor("waiting", target.URL+"/forbidden", true), "")
	// The dispatcher can already have removed a job from the queue while it
	// waits for the single execution slot. Submitted tasks include that wait.
	waitCatalog(t, func() bool { return c.PulsePool().Stats().TasksSubmitted >= 2 })
	if forbidden.Load() != 0 {
		t.Fatal("fixture did not hold the old target before replacement")
	}
	changed := old.Resource
	changed.Spec = managedMonitor("waiting", target.URL+"/replacement", true).Spec
	commitManaged(t, catalog, changed, old.Resource.Metadata.ResourceVersion)
	once.Do(func() { close(release) })
	waitCatalog(t, func() bool { return replacement.Load() > 0 })
	if forbidden.Load() != 0 {
		t.Fatal("worker executed a target after its committed configuration was replaced")
	}
	if !store.Status().Ready {
		t.Fatal(fmt.Sprintf("normal catalog conflict poisoned storage: %s", store.Status().Error))
	}
}

func TestCatalogStartupUsesBoundedConfigureBatchesWithoutProviderCalls(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	settings.Storage.BatchSize = 3
	catalog, store := managedStore(t, settings)
	defer store.Close()
	for n := range 8 {
		commitManaged(t, catalog, managedMonitor(fmt.Sprintf("batch-%02d", n), target.URL, true), "")
	}
	before, err := store.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Store, cfg.Catalog, cfg.Runtime = store, catalog, settings
	c := NewController(cfg)
	defer c.Stop()
	if err := c.LoadCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := store.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Index-before.Index != 3 {
		t.Fatalf("eight monitors should configure in three batches; got %d commits", after.Index-before.Index)
	}
	if calls.Load() != 0 {
		t.Fatal("startup preparation executed provider before controller start")
	}
}

func TestCatalogCredentialRotationRetainsLateActionEvidenceWithoutRedelivery(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	started, release := make(chan struct{}), make(chan struct{})
	var saw, unblock sync.Once
	var oldActions, newActions atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old-action":
			oldActions.Add(1)
			saw.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(204)
		case "/new-action":
			newActions.Add(1)
			w.WriteHeader(204)
		default:
			w.WriteHeader(503)
		}
	}))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	credential := commitManaged(t, catalog, managedResource("Credential", "recovery-destination", api.CredentialSpec{Value: api.Pointer(target.URL + "/old-action")}), "")
	monitor := managedMonitor("recovering", target.URL+"/health", true)
	var spec api.MonitorSpec
	_ = json.Unmarshal(monitor.Spec, &spec)
	spec.Recovery = &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"30s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "recovery-destination"})}}
	monitor.Spec, _ = json.Marshal(spec)
	commitManaged(t, catalog, monitor, "")
	c := managedController(t, settings, catalog, store)
	defer func() { unblock.Do(func() { close(release) }); c.Stop() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery action did not start")
	}
	changed := credential.Resource
	changed.Spec, _ = json.Marshal(api.CredentialSpec{Value: api.Pointer(target.URL + "/new-action")})
	commitManaged(t, catalog, changed, changed.Metadata.ResourceVersion)
	waitCatalog(t, func() bool {
		m, _ := store.Get("recovering")
		for _, a := range m.Actions {
			if a.State == persistence.Unknown {
				return true
			}
		}
		return false
	})
	unblock.Do(func() { close(release) })
	waitCatalog(t, func() bool {
		page, err := store.History().Page("recovering", "", 100)
		if err != nil {
			return false
		}
		for _, event := range page.Events {
			if event.Type == "action_late_evidence" && event.Outcome == "accepted" {
				return true
			}
		}
		return false
	})
	m, _ := store.Get("recovering")
	unknown := false
	for _, a := range m.Actions {
		if a.State == persistence.Unknown {
			unknown = true
		}
	}
	if !unknown || m.VerifyRemaining != 0 || oldActions.Load() != 1 || newActions.Load() != 0 {
		t.Fatal("late evidence changed the hold, redirected recovery or repeated the action")
	}
}
