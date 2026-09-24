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
