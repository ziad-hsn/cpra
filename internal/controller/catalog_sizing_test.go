package controller

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCatalogChangesRefreshPulseDemand(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	c := managedController(t, settings, catalog, store)
	defer c.Stop()
	demand := func(want float64) {
		t.Helper()
		waitCatalog(t, func() bool { return math.Abs(c.pulsePool.Stats().ArrivalRate-want) < 1e-9 })
	}
	demand(0)
	first := commitManaged(t, catalog, managedMonitor("first", target.URL, true), "").Resource
	demand(20)
	var spec api.MonitorSpec
	if err := json.Unmarshal(first.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Check.Interval = "100ms"
	first.Spec, _ = json.Marshal(spec)
	first = commitManaged(t, catalog, first, first.Metadata.ResourceVersion).Resource
	demand(10)
	second := commitManaged(t, catalog, managedMonitor("second", target.URL, true), "").Resource
	demand(30)
	spec.Enabled = api.Pointer(false)
	first.Spec, _ = json.Marshal(spec)
	commitManaged(t, catalog, first, first.Metadata.ResourceVersion)
	demand(20)
	m, _ := store.Get("second")
	commitControl(t, catalog, "snooze", "second", m.ControlRevision, "1h")
	demand(0)
	m, _ = store.Get("second")
	commitControl(t, catalog, "unsnooze", "second", m.ControlRevision, "")
	demand(20)
	deletion, err := catalog.PrepareDelete(context.Background(), "Monitor", "second", second.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.CommitAs(context.Background(), deletion, "test-operator"); err != nil {
		t.Fatal(err)
	}
	demand(0)
}
