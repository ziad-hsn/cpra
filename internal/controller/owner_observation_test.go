package controller

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func monitorObservation(t *testing.T, catalog *management.Catalog, id string, want int64) api.Resource {
	t.Helper()
	resource, err := catalog.Get(context.Background(), "Monitor", id)
	if err != nil {
		t.Fatal(err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(resource.Status, &status); err != nil {
		t.Fatal(err)
	}
	if status.ObservedGeneration != want {
		t.Fatalf("owner observation = %d, want %d (desired %d)", status.ObservedGeneration, want, resource.Metadata.Generation)
	}
	return resource
}

func TestCatalogOwnerInitializationFailureStillJoinsReconciler(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	defer store.Close()
	commitManaged(t, catalog, managedMonitor("initialization-failure", "http://127.0.0.1:1", false), "")
	cfg := DefaultConfig()
	cfg.Store, cfg.Catalog, cfg.Runtime = store, catalog, settings
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
	owner := NewController(cfg)
	if err := owner.LoadCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Loading prepared the catalog channel, but initialization has not launched
	// its reconciler. A failure here must still allow Finalize to join safely.
	store.MarkUnavailable(errors.New("injected storage failure before initialization"))
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := owner.WaitReady(ctx); err == nil || owner.Ready() {
		t.Fatal("failed initialization reported ready")
	}
	if err := owner.StopContext(ctx); err != nil {
		t.Fatalf("failed initialization stranded the owner at shutdown: %v", err)
	}
}

func waitMonitorObserved(t *testing.T, catalog *management.Catalog, id string, want int64) {
	t.Helper()
	waitCatalog(t, func() bool {
		resource, err := catalog.Get(context.Background(), "Monitor", id)
		var status api.MonitorStatus
		return err == nil && json.Unmarshal(resource.Status, &status) == nil && status.ObservedGeneration == want
	})
}

func TestCatalogOwnerObservationRequiresInstallAfterRestartAndDependencyChange(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	var owner *Controller
	defer func() {
		if owner != nil {
			owner.Stop()
		}
		if store != nil {
			_ = store.Close()
		}
	}()
	credential := commitManaged(t, catalog, managedResource("Credential", "check-url", api.CredentialSpec{Value: api.Pointer("http://127.0.0.1:1/first")}), "")
	monitor := managedMonitor("owner-proof", "", false)
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Check.Driver.Config = json.RawMessage(`{}`)
	spec.Check.Driver.CredentialRefs = api.Pointer(map[string]string{"url": "check-url"})
	monitor.Spec, _ = json.Marshal(spec)
	created := commitManaged(t, catalog, monitor, "")
	monitorObservation(t, catalog, "owner-proof", 0)
	cfg := DefaultConfig()
	cfg.Store, cfg.Catalog, cfg.Runtime = store, catalog, settings
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
	owner = NewController(cfg)
	if err := owner.LoadCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Jobs have been prepared, but the startup owner has not yet built its
	// operational index and schedules. No application claim is justified yet.
	monitorObservation(t, catalog, "owner-proof", 0)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitMonitorObserved(t, catalog, "owner-proof", created.Resource.Metadata.Generation)
	if owner.SnapshotHolder().Index().Overview().Total != 1 {
		t.Fatal("observation published before dashboard projection was installed")
	}
	owner.Stop()
	owner = nil
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	catalog, store = managedStore(t, settings)
	monitorObservation(t, catalog, "owner-proof", 0)
	owner = managedController(t, settings, catalog, store)
	waitMonitorObserved(t, catalog, "owner-proof", created.Resource.Metadata.Generation)
	owner.Stop()
	owner = nil

	// A shared secret changes the compiled target without changing this monitor's
	// desired generation. The previous owner acknowledgement must be invalidated.
	rotated := credential.Resource
	rotated.Spec, _ = json.Marshal(api.CredentialSpec{Value: api.Pointer("http://127.0.0.1:1/second")})
	commitManaged(t, catalog, rotated, rotated.Metadata.ResourceVersion)
	current := monitorObservation(t, catalog, "owner-proof", 0)
	if current.Metadata.Generation != created.Resource.Metadata.Generation {
		t.Fatal("fixture changed monitor generation rather than shared dependency")
	}
	owner = managedController(t, settings, catalog, store)
	waitMonitorObserved(t, catalog, "owner-proof", current.Metadata.Generation)
	owner.Stop()
	owner = nil

	// Metadata changes still need the exact resource version installed, even
	// when the spec generation and resolved check target have not changed.
	current.Metadata.Name = api.Pointer("Renamed monitor")
	edited := commitManaged(t, catalog, current, current.Metadata.ResourceVersion)
	monitorObservation(t, catalog, "owner-proof", 0)
	owner = managedController(t, settings, catalog, store)
	waitMonitorObserved(t, catalog, "owner-proof", edited.Resource.Metadata.Generation)
	owner.Stop()
	owner = nil
	deletion, err := catalog.PrepareDelete(context.Background(), "Monitor", "owner-proof", edited.Resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CommitAs(context.Background(), deletion, "test-operator"); err != nil {
		t.Fatal(err)
	}
	recreated := commitManaged(t, catalog, monitor, "")
	if recreated.Resource.Metadata.UID == created.Resource.Metadata.UID {
		t.Fatal("recreation reused the original incarnation")
	}
	monitorObservation(t, catalog, "owner-proof", 0)
	owner = managedController(t, settings, catalog, store)
	waitMonitorObserved(t, catalog, "owner-proof", recreated.Resource.Metadata.Generation)
	state, _ := store.Get("owner-proof")
	if state.TotalChecks != 0 {
		t.Fatal("disabled observation fixture executed a health check")
	}
}
