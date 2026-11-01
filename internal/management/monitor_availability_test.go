package management

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorAvailabilityRequiresMatchingOwnerProjection(t *testing.T) {
	c, store := testCatalog(t)
	r := createResource(t, c, controlResource("owner-availability"))
	m := configureFacadeControl(t, c, store, r)
	at := time.Now().UTC()
	guard := &persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: r.Metadata.ID}, UID: r.Metadata.UID, Revision: r.Metadata.ResourceVersion}}}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Generation: m.Generation + 1, Guard: guard, CheckControlRevision: m.ControlRevision, Outcome: "success"}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatal("observation", results, err)
	}
	m = *results[0].Monitor
	read := func() api.MonitorStatus {
		t.Helper()
		out, err := c.Get(t.Context(), "Monitor", m.ID)
		if err != nil {
			t.Fatal(err)
		}
		var status api.MonitorStatus
		if err := json.Unmarshal(out.Status, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	status := read()
	if status.Health != "unknown" || status.Availability.State != "unavailable" || status.LastObservedHealth != "healthy" || status.LastObservedAt == nil || !status.LastObservedAt.Equal(at) || status.Maintenance != nil {
		t.Fatal("durable observation masqueraded as current owner availability", status)
	}
	holder := fleetview.NewHolder()
	index := fleetview.NewIndex(nil)
	holder.SetIndex(index)
	c.SetObservations(holder)
	row := fleetview.MonitorSummary{MonitorID: m.ID, IncarnationUID: m.IncarnationID(), CatalogRevision: r.Metadata.ResourceVersion, ConfigurationRevision: m.Revision, ControlRevision: m.ControlRevision, StateGeneration: m.Generation, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, Health: "healthy", Availability: fleetview.MonitorAvailability{State: "available", ObservedAt: at}, Maintenance: true, NotificationsSuppressed: true, SuppressionObservedAt: at}
	index.Put(row)
	status = read()
	if status.Health != "unknown" || status.Availability.State != "unavailable" {
		t.Fatal("unacknowledged owner projection was published", status)
	}
	if err := store.MarkMonitorObserved(m.ID, *guard, uint64(r.Metadata.Generation)); err != nil {
		t.Fatal(err)
	}
	status = read()
	if status.Health != "healthy" || status.Availability.State != "available" || status.Maintenance == nil || !*status.Maintenance || status.NotificationsSuppressed == nil || !*status.NotificationsSuppressed {
		t.Fatal("matching owner projection not published", status)
	}
	for _, field := range []string{"incarnation", "catalog", "configuration", "control", "state", "incident", "incident revision"} {
		changed := row
		switch field {
		case "incarnation":
			changed.IncarnationUID = "another-owner"
		case "catalog":
			changed.CatalogRevision = "another-catalog-revision"
		case "configuration":
			changed.ConfigurationRevision = "another-configuration"
		case "control":
			changed.ControlRevision = "another-control"
		case "state":
			changed.StateGeneration++
		case "incident":
			changed.IncidentID = "another-incident"
		case "incident revision":
			changed.IncidentRevision = "another-incident-revision"
		}
		index.Put(changed)
		status = read()
		if status.Health != "unknown" || status.Availability.State != "unavailable" || status.Maintenance != nil || status.NotificationsSuppressed != nil {
			t.Fatal("mismatched owner projection attached", field, status)
		}
	}
}

func TestMonitorAvailabilityWaitsForDismissAndReopenProjection(t *testing.T) {
	c, store := testCatalog(t)
	r := createResource(t, c, controlResource("incident-availability"))
	m := configureFacadeControl(t, c, store, r)
	at := time.Now().UTC()
	guard := &persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: r.Metadata.ID}, UID: r.Metadata.UID, Revision: r.Metadata.ResourceVersion}}}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at, Generation: m.Generation + 1, Guard: guard, CheckControlRevision: m.ControlRevision, Outcome: "failure"}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatal("observation", results, err)
	}
	m = *results[0].Monitor
	if m.IncidentID == "" {
		t.Fatal("incident was not opened")
	}
	holder := fleetview.NewHolder()
	index := fleetview.NewIndex(nil)
	holder.SetIndex(index)
	c.SetObservations(holder)
	row := fleetview.MonitorSummary{MonitorID: m.ID, IncarnationUID: m.IncarnationID(), CatalogRevision: r.Metadata.ResourceVersion, ConfigurationRevision: m.Revision, ControlRevision: m.ControlRevision, StateGeneration: m.Generation, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, Health: "unhealthy", Availability: fleetview.MonitorAvailability{State: "available", ObservedAt: at}, SuppressionObservedAt: at}
	index.Put(row)
	if err := store.MarkMonitorObserved(m.ID, *guard, uint64(r.Metadata.Generation)); err != nil {
		t.Fatal(err)
	}
	read := func() api.MonitorStatus {
		t.Helper()
		out, err := c.Get(t.Context(), "Monitor", m.ID)
		if err != nil {
			t.Fatal(err)
		}
		var status api.MonitorStatus
		if err := json.Unmarshal(out.Status, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	for _, action := range []string{"dismiss", "reopen"} {
		prepared, err := c.PrepareControl(t.Context(), action, m.IncidentID, api.ControlRequest{Revision: m.IncidentRevision, Reason: "planned operator change"})
		if err != nil {
			t.Fatal(action, err)
		}
		if _, err := c.CommitControlAs(t.Context(), prepared, "operator"); err != nil {
			t.Fatal(action, err)
		}
		current, _ := store.Get(m.ID)
		if current.Generation != row.StateGeneration || current.ControlRevision != row.ControlRevision || current.IncidentRevision == row.IncidentRevision {
			t.Fatal("fixture did not isolate the incident revision", action)
		}
		status := read()
		if status.Health != "unknown" || status.Availability.State != "unavailable" || status.NotificationsSuppressed != nil || status.LastObservedHealth != "unhealthy" {
			t.Fatal("stale incident suppression was published", action, status)
		}
		m = current
		row.IncidentRevision, row.NotificationsSuppressed = m.IncidentRevision, m.Dismissed
		index.Put(row)
		status = read()
		if status.Health != "unhealthy" || status.NotificationsSuppressed == nil || *status.NotificationsSuppressed != m.Dismissed {
			t.Fatal("matching incident projection was not published", action, status)
		}
	}
}
