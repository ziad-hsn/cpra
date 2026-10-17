package management

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func configureFacadeControl(t *testing.T, c *Catalog, store *persistence.Store, input api.Resource) persistence.Monitor {
	t.Helper()
	guard := &persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: input.Metadata.ID}, UID: input.Metadata.UID, Revision: input.Metadata.ResourceVersion}}}
	m := persistence.Monitor{ID: input.Metadata.ID, CatalogUID: input.Metadata.UID, Revision: "execution", Policy: persistence.Policy{Interval: time.Minute, Enabled: true, Unhealthy: 1, Healthy: 1}}
	result, err := store.Submit(context.Background(), []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), Guard: guard, Config: &m}})
	if err != nil || len(result) != 1 || result[0].Err != nil || result[0].Monitor == nil {
		t.Fatalf("configure: %+v %v", result, err)
	}
	return *result[0].Monitor
}

func controlResource(id string) api.Resource {
	return resource("Monitor", id, api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Interval: "60s", Timeout: "5s",
		Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://fixture-not-executed.invalid"}`)}}})
}

func TestPreparedControlCannotCrossMonitorIncarnation(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	original := createResource(t, c, controlResource("service"))
	m := configureFacadeControl(t, c, store, original)
	prepared, err := c.PrepareControl(ctx, "snooze", m.ID, api.ControlRequest{Revision: m.ControlRevision, Duration: "1m", Reason: "Maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := c.PrepareDelete(ctx, "Monitor", m.ID, original.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitAs(ctx, deletion, "operator"); err != nil {
		t.Fatal(err)
	}
	replacement := createResource(t, c, controlResource(m.ID))
	current := configureFacadeControl(t, c, store, replacement)
	if _, err := c.CommitControlAs(ctx, prepared, "operator"); !errors.Is(err, persistence.ErrControlConflict) {
		t.Fatalf("old incarnation accepted: %v", err)
	}
	after, _ := store.Get(m.ID)
	if after.CatalogUID != replacement.Metadata.UID || after.ControlRevision != current.ControlRevision || !after.SnoozedUntil.IsZero() {
		t.Fatal("replacement changed by old control")
	}
}

func TestPreparedControlHasSingleAdmissionAndAuthenticatedAudit(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	m := configureFacadeControl(t, c, store, createResource(t, c, controlResource("service")))
	p, err := c.PrepareControl(ctx, "snooze", m.ID, api.ControlRequest{Revision: m.ControlRevision, Duration: "1m", Reason: "Planned work"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.CommitControlAs(ctx, p, "oncall")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitControlAs(ctx, p, "another"); !errors.Is(err, persistence.ErrControlInvalid) {
		t.Fatal("prepared control reused", err)
	}
	receipt, err := store.Operation(result.Operation.ID)
	if err != nil || receipt.Actor != "oncall" || receipt.Subject != "control" || receipt.OldVersion != m.ControlRevision {
		t.Fatalf("audit lost: %+v %v", receipt, err)
	}
	if err := c.CompleteOperation(ctx, receipt.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteOperation(ctx, receipt.ID, true); err != nil {
		t.Fatal("identical completion not idempotent", err)
	}
}

func TestMonitorObservationNeverAttachesControlsToStaleDesiredPage(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	original := createResource(t, c, controlResource("service"))
	configureFacadeControl(t, c, store, original)
	view, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.PreparePatch(ctx, "Monitor", "service", original.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"Updated"}}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := c.CommitAs(ctx, p, "operator")
	if err != nil {
		t.Fatal(err)
	}
	configureFacadeControl(t, c, store, updated.Resource)
	items, _, err := view.Page(ctx, "Monitor", "", 100)
	if err != nil || len(items) != 1 {
		t.Fatal("page failed", err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(items[0].Status, &status); err != nil {
		t.Fatal(err)
	}
	if status.ControlRevision != "" || status.ObservedGeneration != 0 || status.LastCheckLatencyMS.Available {
		t.Fatalf("stale page acquired current controls: %+v", status)
	}
	current, err := c.Get(ctx, "Monitor", "service")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(current.Status, &status); err != nil || status.ControlRevision == "" {
		t.Fatal("current controls unavailable", err)
	}
}

func TestMonitorObservationRequiresExplicitOwnerAcknowledgement(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	resource := createResource(t, c, controlResource("observed"))
	m := configureFacadeControl(t, c, store, resource)
	before, err := c.Get(ctx, "Monitor", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var old api.MonitorStatus
	if err := json.Unmarshal(before.Status, &old); err != nil {
		t.Fatal(err)
	}
	if old.ObservedGeneration != 0 {
		t.Fatal("configure claimed owner installation")
	}
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: m.ID}, UID: resource.Metadata.UID, Revision: resource.Metadata.ResourceVersion}}}
	if err := store.MarkMonitorObserved(m.ID, guard, uint64(resource.Metadata.Generation)); err != nil {
		t.Fatal(err)
	}
	after, err := c.Get(ctx, "Monitor", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var observed api.MonitorStatus
	if err := json.Unmarshal(after.Status, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.ObservedGeneration != resource.Metadata.Generation || observed.StatusRevision == old.StatusRevision || observed.LastCheckLatencyMS.Available {
		t.Fatal("owner marker missing or fabricated a check", observed)
	}
}

func TestMonitorActionCountsPreserveAvailability(t *testing.T) {
	c, store := testCatalog(t)
	ctx := t.Context()
	r := createResource(t, c, controlResource("action-counts"))
	assertCounts := func(input api.Resource, want string) {
		t.Helper()
		observed, err := c.observedResource(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(observed.Status, &fields); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"unknownActions", "actionsNeedingReview"} {
			if string(fields[name]) != want {
				t.Fatalf("%s = %s, want %q; status: %s", name, fields[name], want, observed.Status)
			}
		}
	}
	assertCounts(r, "") // The controller has not installed this configuration.
	commit := func(command persistence.Command) persistence.Result {
		t.Helper()
		results, err := store.Submit(ctx, []persistence.Command{command})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("fixture commit", err, results)
		}
		return results[0]
	}
	guard := &persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: r.Metadata.ID}, UID: r.Metadata.UID, Revision: r.Metadata.ResourceVersion}}}
	m := persistence.Monitor{ID: r.Metadata.ID, CatalogUID: r.Metadata.UID, Revision: "execution",
		Policy: persistence.Policy{Interval: time.Minute, Enabled: true, Intervention: true, Unhealthy: 1, Healthy: 1}}
	m = *commit(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), Guard: guard, Config: &m}).Monitor
	assertCounts(r, "0")
	for _, field := range []string{"uid", "revision"} {
		stale := r
		if field == "uid" {
			stale.Metadata.UID = "other-incarnation"
		} else {
			stale.Metadata.ResourceVersion = "other-revision"
		}
		assertCounts(stale, "")
	}

	m = *commit(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: guard, Generation: 1, Outcome: "failure", At: time.Now().UTC()}).Monitor
	if len(m.Actions) != 1 {
		t.Fatal("fixture did not admit one action")
	}
	for id := range m.Actions {
		handle, err := store.BeginLocalAction(ctx, persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, Guard: guard, ActionID: id, At: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Finish(ctx); err != nil {
			t.Fatal(err)
		}
		commit(persistence.Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, Outcome: "failure", Ambiguous: true, At: time.Now().UTC()})
	}
	assertCounts(r, "1")
	deletion, err := c.PrepareDelete(ctx, "Monitor", m.ID, r.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitAs(ctx, deletion, "operator"); err != nil {
		t.Fatal(err)
	}
	removed := deletion.mutation.Record
	removal := &persistence.CatalogGuard{Removed: true, Conditions: []persistence.CatalogCondition{{Key: removed.Key, UID: removed.UID, Revision: removed.Revision}}}
	commit(persistence.Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, Guard: removal, At: time.Now().UTC()})
	assertCounts(r, "")
}
