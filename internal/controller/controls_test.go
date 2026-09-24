package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func commitControl(t *testing.T, catalog *management.Catalog, action, id, revision, duration string) management.ControlResult {
	t.Helper()
	p, err := catalog.PrepareControl(context.Background(), action, id, api.ControlRequest{Revision: revision, Duration: duration, Reason: "planned maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := catalog.CommitControlAs(context.Background(), p, "oncall-alice")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCatalogControlsFenceQueuedCheckAndRecordStartedObservation(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	started, release := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	var waitingCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocking" {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
		} else {
			waitingCalls.Add(1)
		}
		w.WriteHeader(503)
	}))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	commitManaged(t, catalog, managedMonitor("a-blocking", target.URL+"/blocking", true), "")
	c := managedController(t, settings, catalog, store)
	defer func() { unblock.Do(func() { close(release) }); c.Stop() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking check did not start")
	}
	commitManaged(t, catalog, managedMonitor("b-waiting", target.URL+"/waiting", true), "")
	waitCatalog(t, func() bool { return c.PulsePool().Stats().TasksSubmitted >= 2 })
	if waitingCalls.Load() != 0 {
		t.Fatal("queued check reached provider before test pause")
	}
	for _, id := range []string{"a-blocking", "b-waiting"} {
		m, _ := store.Get(id)
		commitControl(t, catalog, "snooze", id, m.ControlRevision, "1h")
	}
	unblock.Do(func() { close(release) })
	waitCatalog(t, func() bool { m, _ := store.Get("a-blocking"); return m.TotalChecks == 1 })
	time.Sleep(150 * time.Millisecond)
	if waitingCalls.Load() != 0 {
		t.Fatal("pre-snooze queued check reached provider")
	}
	m, _ := store.Get("a-blocking")
	if m.Incident || len(m.Actions) != 0 {
		t.Fatal("started observation opened incident or action while snoozed")
	}
	waiting, _ := store.Get("b-waiting")
	if waiting.TotalChecks != 0 {
		t.Fatal("rejected queued work became a health observation")
	}
	commitControl(t, catalog, "unsnooze", "b-waiting", waiting.ControlRevision, "")
	waitCatalog(t, func() bool { return waitingCalls.Load() > 0 })
}

func TestCatalogSnoozeRestartExpiryPreservesDisableAndCompletesReceipt(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	commitManaged(t, catalog, managedMonitor("paused", target.URL, false), "")
	c := managedController(t, settings, catalog, store)
	defer func() {
		if c != nil {
			c.Stop()
		}
		if store != nil {
			_ = store.Close()
		}
	}()
	m, _ := store.Get("paused")
	receipt := commitControl(t, catalog, "snooze", "paused", m.ControlRevision, "1h")
	c.Stop()
	c = nil
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	catalog, store = managedStore(t, settings)
	m, _ = store.Get("paused")
	if m.SnoozedUntil.IsZero() || m.Policy.Enabled || len(receipt.Operation.Items) != 1 || m.ControlRevision != receipt.Operation.Items[0].NewVersion || m.ControlRevision == receipt.Operation.ID {
		t.Fatal("restart lost independent pause or disable state")
	}
	c = managedController(t, settings, catalog, store)
	commitControl(t, catalog, "snooze", "paused", m.ControlRevision, "500ms")
	waitCatalog(t, func() bool { m, _ := store.Get("paused"); return m.SnoozedUntil.IsZero() })
	m, _ = store.Get("paused")
	if m.Policy.Enabled || calls.Load() != 0 {
		t.Fatal("snooze expiry enabled or dispatched disabled monitor")
	}
	finalRevision := m.ControlRevision
	waitCatalog(t, func() bool {
		r, e := catalog.Operation(context.Background(), finalRevision)
		return e == nil && (r.Applied != nil && *r.Applied == 1)
	})
	page, err := store.History().Page("paused", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Events {
		if e.Type == "control_expire_snooze" && e.Actor == "system" {
			found = true
		}
	}
	if !found {
		t.Fatal("expiry audit missing")
	}
}

func TestCatalogAcknowledgementKeepsChecksRunningAndUsesOwnerReceipt(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	commitManaged(t, catalog, managedMonitor("incident", target.URL, true), "")
	c := managedController(t, settings, catalog, store)
	defer c.Stop()
	waitCatalog(t, func() bool { m, _ := store.Get("incident"); return m.Incident })
	m, _ := store.Get("incident")
	before := m.TotalChecks
	receipt := commitControl(t, catalog, "acknowledge", m.IncidentID, m.IncidentRevision, "")
	waitCatalog(t, func() bool {
		m, _ := store.Get("incident")
		return m.TotalChecks > before && m.AcknowledgedBy == "oncall-alice"
	})
	waitCatalog(t, func() bool {
		r, e := catalog.Operation(context.Background(), receipt.Operation.ID)
		return e == nil && (r.Applied != nil && *r.Applied == 1)
	})
	m, _ = store.Get("incident")
	if m.Dismissed || !m.SnoozedUntil.IsZero() {
		t.Fatal("acknowledgement changed scheduling or notification suppression")
	}
}

func TestCatalogExpiredSnoozesShareOneDurableBatch(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	settings := runtimeconfig.Default()
	settings.Storage.Mode = "memory"
	catalog, store := managedStore(t, settings)
	defer store.Close()
	for n := range 5 {
		commitManaged(t, catalog, managedMonitor(fmt.Sprintf("expiry-%d", n), "http://example.invalid/never-invoked", false), "")
	}
	cfg := DefaultConfig()
	cfg.Store, cfg.Catalog, cfg.Runtime = store, catalog, settings
	c := NewController(cfg)
	defer c.Stop()
	if err := c.LoadCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	var latestExpiry time.Time
	for n := range 5 {
		id := fmt.Sprintf("expiry-%d", n)
		m, _ := store.Get(id)
		// Allow actual durable allocation and activation to finish before the
		// requested deadline. A 1ms snooze can expire during the 5ms batch wait.
		commitControl(t, catalog, "snooze", id, m.ControlRevision, "2s")
		m, _ = store.Get(id)
		if m.SnoozedUntil.After(latestExpiry) {
			latestExpiry = m.SnoozedUntil
		}
	}
	// Start the owner only after every admitted deadline, so this test covers
	// one simultaneous expiry batch rather than relying on a fixed 2ms sleep.
	if remaining := time.Until(latestExpiry); remaining > 0 {
		time.Sleep(remaining)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitCatalog(t, func() bool {
		for n := range 5 {
			m, _ := store.Get(fmt.Sprintf("expiry-%d", n))
			if !m.SnoozedUntil.IsZero() {
				return false
			}
		}
		return true
	})
	var committedIndex uint64
	for n := range 5 {
		m, _ := store.Get(fmt.Sprintf("expiry-%d", n))
		receipt, err := store.Operation(m.ControlRevision)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			committedIndex = receipt.CommittedIndex
		}
		if receipt.CommittedIndex != committedIndex {
			t.Fatal("simultaneous expiries used separate durable submissions")
		}
	}
}
