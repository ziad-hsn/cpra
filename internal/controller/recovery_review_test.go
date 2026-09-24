package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func commitManagedAction(t *testing.T, catalog *management.Catalog, action, id string, request api.ControlRequest) management.ActionResult {
	t.Helper()
	p, err := catalog.PrepareAction(context.Background(), action, id, request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := catalog.CommitActionAs(context.Background(), p, "oncall-alice")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCatalogManualRecoveryAndHistoricalReviewUseOwnerReceipts(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var calls atomic.Int64
	// Execute the normal webhook recovery driver. A connection closed after
	// receiving its request is ambiguous, not a confirmed provider rejection.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recover" {
			calls.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(503)
	}))
	defer target.Close()
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	commitManaged(t, catalog, managedResource("Credential", "manual-destination", api.CredentialSpec{Value: api.Pointer(target.URL + "/recover")}), "")
	monitor := managedMonitor("manual-recovery", target.URL+"/health", true)
	var spec api.MonitorSpec
	_ = json.Unmarshal(monitor.Spec, &spec)
	spec.Check.UnhealthyThreshold = api.Pointer(int64(100))
	spec.Check.Interval = "100ms"
	raw, _ := json.Marshal(map[string]string{"method": "POST", "timeout": "2s"})
	spec.Recovery = &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: raw, CredentialRefs: api.Pointer(map[string]string{"url": "manual-destination"})}, MaxAttempts: api.Pointer(int64(3)), Cooldown: api.Pointer(api.Duration("100ms"))}
	monitor.Spec, _ = json.Marshal(spec)
	created := commitManaged(t, catalog, monitor, "")
	c := managedController(t, settings, catalog, store)
	defer func() {
		if c != nil {
			c.Stop()
		}
		if store != nil {
			_ = store.Close()
		}
	}()
	waitCatalog(t, func() bool { m, _ := store.Get("manual-recovery"); return m.TotalChecks > 0 })
	m, _ := store.Get("manual-recovery")
	if m.Incident || m.LastOutcome != "failure" || calls.Load() != 0 {
		t.Fatal("manual fixture did not preserve failed observation below automatic threshold")
	}
	admitted := commitManagedAction(t, catalog, "recover", m.ID, api.ControlRequest{Revision: m.ControlRevision, Reason: "Operator investigated the target"})
	waitCatalog(t, func() bool {
		receipt, err := catalog.Operation(context.Background(), admitted.Operation.ID)
		return err == nil && (receipt.Applied != nil && *receipt.Applied == 1)
	})
	var action persistence.ActionRecord
	waitCatalog(t, func() bool {
		m, _ := store.Get("manual-recovery")
		for id, a := range m.Actions {
			if a.Kind == "intervention" && a.State == persistence.Unknown {
				var err error
				var ok bool
				action, ok, err = store.Action(id)
				return err == nil && ok && action.ExecutorFenced
			}
		}
		return false
	})
	if calls.Load() != 1 || !action.Held {
		t.Fatal("unknown manual recovery repeated or lost its hold")
	}
	reviewed := commitManagedAction(t, catalog, "review", action.ID, api.ControlRequest{Revision: action.ReviewRevision, Resolution: "inconclusive", Reason: "Provider investigation pending", EvidenceRefs: []string{"ticket-41"}})
	waitCatalog(t, func() bool {
		receipt, err := catalog.Operation(context.Background(), reviewed.Operation.ID)
		return err == nil && (receipt.Applied != nil && *receipt.Applied == 1)
	})
	c.Stop()
	c = nil
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	catalog, store = managedStore(t, settings)
	action, ok, err := store.Action(action.ID)
	if err != nil || !ok || action.Review == nil || action.Review.Resolution != "inconclusive" || !action.Held || !action.ExecutorFenced {
		t.Fatal("restart lost the action, review or local executor fence")
	}
	// Replace the public ID while retaining the original action. Its review
	// must complete against that original UID without altering the new target.
	deleted, err := catalog.PrepareDelete(context.Background(), "Monitor", "manual-recovery", created.Resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CommitAs(context.Background(), deleted, "oncall-alice"); err != nil {
		t.Fatal(err)
	}
	replacement := managedMonitor("manual-recovery", target.URL+"/new-health", false)
	newRecord := commitManaged(t, catalog, replacement, "")
	if newRecord.Resource.Metadata.UID == action.CatalogUID {
		t.Fatal("replacement did not get a new incarnation")
	}
	reviewed = commitManagedAction(t, catalog, "review", action.ID, api.ControlRequest{Revision: action.ReviewRevision, Resolution: "accepted", Reason: "Operator confirmed provider acceptance", EvidenceRefs: []string{"provider-receipt-41"}})
	c = managedController(t, settings, catalog, store)
	waitCatalog(t, func() bool {
		receipt, err := catalog.Operation(context.Background(), reviewed.Operation.ID)
		return err == nil && (receipt.Applied != nil && *receipt.Applied == 1)
	})
	time.Sleep(300 * time.Millisecond) // More than the fixture's recovery cooldown.
	action, _, err = store.Action(action.ID)
	if err != nil || action.State != persistence.Unknown || action.Held || action.Review == nil || action.Review.Actor != "oncall-alice" || action.Review.Resolution != "accepted" {
		t.Fatal("historical review changed provider facts or failed to retain its assertion")
	}
	m, _ = store.Get("manual-recovery")
	if m.CatalogUID != newRecord.Resource.Metadata.UID || m.Policy.Enabled || m.TotalChecks != 0 || calls.Load() != 1 {
		t.Fatal("historical review redirected or replayed work against the replacement monitor")
	}
}
