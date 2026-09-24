package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/slo"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func pauseReport(store *persistence.Store, driver string) slo.Report {
	for _, r := range store.SLO().View(time.Now(), 5*time.Minute, 1).Reports {
		if r.Driver == driver {
			return r
		}
	}
	return slo.Report{}
}

func TestCatalogPauseExposureTracksOverlapDriverChangeRemovalAndRestart(t *testing.T) {
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
	created := commitManaged(t, catalog, managedMonitor("paused", "http://127.0.0.1:1", false), "")
	owner = managedController(t, settings, catalog, store)
	waitMonitorObserved(t, catalog, "paused", created.Resource.Metadata.Generation)
	waitCatalog(t, func() bool { return pauseReport(store, "http").PausedMonitors == 1 })
	m, _ := store.Get("paused")
	paused := commitControl(t, catalog, "snooze", m.ID, m.ControlRevision, "1h")
	waitCatalog(t, func() bool {
		op, err := catalog.Operation(context.Background(), paused.Operation.ID)
		return err == nil && (op.Applied != nil && *op.Applied == 1)
	})
	if r := pauseReport(store, "http"); r.PausedMonitors != 1 || r.Samples != 0 || r.Expected != 0 || r.PausedMonitorSeconds <= 0 {
		t.Fatalf("overlap duplicated pause or fabricated check: %+v", r)
	}
	// Enabling does not end the independently active snooze.
	current, err := catalog.Get(context.Background(), "Monitor", "paused")
	if err != nil {
		t.Fatal(err)
	}
	var spec api.MonitorSpec
	if err := json.Unmarshal(current.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Enabled = api.Pointer(true)
	current.Spec, _ = json.Marshal(spec)
	enabled := commitManaged(t, catalog, current, current.Metadata.ResourceVersion)
	waitMonitorObserved(t, catalog, "paused", enabled.Resource.Metadata.Generation)
	if pauseReport(store, "http").PausedMonitors != 1 {
		t.Fatal("enable cleared snooze exposure")
	}
	// A paused driver edit transfers one membership without leaving a stale
	// count in the old driver's histogram or dispatching against either target.
	current = enabled.Resource
	spec.Check.Driver = api.DriverConfig{Type: "tcp", Config: json.RawMessage(`{"host":"127.0.0.1","port":1}`)}
	current.Spec, _ = json.Marshal(spec)
	edited := commitManaged(t, catalog, current, current.Metadata.ResourceVersion)
	waitMonitorObserved(t, catalog, "paused", edited.Resource.Metadata.Generation)
	if pauseReport(store, "http").PausedMonitors != 0 || pauseReport(store, "tcp").PausedMonitors != 1 {
		t.Fatal("driver edit duplicated or lost membership")
	}
	deletion, err := catalog.PrepareDelete(context.Background(), "Monitor", "paused", edited.Resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.CommitAs(context.Background(), deletion, "test-operator"); err != nil {
		t.Fatal(err)
	}
	waitCatalog(t, func() bool { return pauseReport(store, "tcp").PausedMonitors == 0 })
	recreated := managedMonitor("paused", "http://127.0.0.1:1", false)
	commitManaged(t, catalog, recreated, "")
	waitCatalog(t, func() bool { return pauseReport(store, "http").PausedMonitors == 1 })
	owner.Stop()
	owner = nil
	before := pauseReport(store, "http")
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	catalog, store = managedStore(t, settings)
	r := pauseReport(store, "http")
	if r.PausedMonitors != 0 || r.PausedMonitorSeconds <= 0 || r.PausedMonitorSeconds > before.PausedMonitorSeconds {
		t.Fatal("restore invented membership or lost retained exposure", r, before)
	}
	owner = managedController(t, settings, catalog, store)
	waitCatalog(t, func() bool { return pauseReport(store, "http").PausedMonitors == 1 })
	m, _ = store.Get("paused")
	if m.TotalChecks != 0 || pauseReport(store, "http").Samples != 0 || pauseReport(store, "tcp").Samples != 0 {
		t.Fatal("intentional pauses fabricated healthy samples")
	}
}
