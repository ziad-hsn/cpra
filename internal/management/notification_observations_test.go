package management

import (
	"encoding/json"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorConditionDetailDoesNotEnrichCollectionPages(t *testing.T) {
	c, store := testCatalog(t)
	input := createResource(t, c, controlResource("condition-coverage"))
	current := configureFacadeControl(t, c, store, input)
	detail, err := c.Get(t.Context(), "Monitor", current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(detail.Status, &status); err != nil || status.NotificationConditionsObservedAt == nil || status.NotificationConditionsSequence == "" {
		t.Fatal("detail did not sample retained conditions", status, err)
	}
	view, err := c.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := view.Page(t.Context(), "Monitor", "", 100)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	status = api.MonitorStatus{}
	if err := json.Unmarshal(items[0].Status, &status); err != nil || status.NotificationConditionsObservedAt != nil || status.NotificationConditionsSequence != "" {
		t.Fatal("collection read performed a detail sample", status, err)
	}
}

func TestMonitorConditionStatusFence(t *testing.T) {
	status := persistence.MonitorStatus{CatalogUID: "owner", Revision: "execution", CatalogRevision: "catalog", ControlRevision: "control", Generation: 17, IncidentID: "incident", IncidentRevision: "incident-revision"}
	view := persistence.NotificationConditionView{IncarnationID: status.CatalogUID, Revision: status.Revision, CatalogRevision: status.CatalogRevision, ControlRevision: status.ControlRevision, Generation: status.Generation, IncidentID: status.IncidentID, IncidentRevision: status.IncidentRevision}
	if !notificationConditionsMatchStatus(view, status) {
		t.Fatal("matching identity rejected")
	}
	for _, change := range []func(*persistence.MonitorStatus){
		func(s *persistence.MonitorStatus) { s.CatalogUID += "x" },
		func(s *persistence.MonitorStatus) { s.Revision += "x" },
		func(s *persistence.MonitorStatus) { s.CatalogRevision += "x" },
		func(s *persistence.MonitorStatus) { s.ControlRevision += "x" },
		func(s *persistence.MonitorStatus) { s.Generation++ },
		func(s *persistence.MonitorStatus) { s.IncidentID += "x" },
		func(s *persistence.MonitorStatus) { s.IncidentRevision += "x" },
	} {
		changed := status
		change(&changed)
		if notificationConditionsMatchStatus(view, changed) {
			t.Fatal("different owner or state matched", changed)
		}
	}
}
