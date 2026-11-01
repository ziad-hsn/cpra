package persistence

import (
	"fmt"
	"testing"
	"time"
)

func TestIncidentTriageSnapshotTracksAttentionWithoutReorderingRetainedPage(t *testing.T) {
	s := openCatalogMemory(t)
	a, g := managedControlMonitor(t, s, "old-open")
	a = controlPulse(t, s, a, g, policyTime().Add(time.Second), "failure")
	b, bg := managedControlMonitor(t, s, "new-open")
	b = controlPulse(t, s, b, bg, policyTime().Add(2*time.Second), "failure")
	v, err := s.IncidentSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, next, err := v.FilteredPage(t.Context(), "", 1, IncidentFilter{Order: "triage", State: "open"})
	if err != nil || len(rows) != 1 || rows[0].MonitorID != a.ID || next == "" {
		t.Fatal("oldest unattended outage not first", rows, err)
	}
	applyControlRequest(t, s, controlRequest(a, "acknowledge", "triage-ack", policyTime().Add(3*time.Second)))
	fresh, err := s.IncidentSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := fresh.FilteredPage(t.Context(), "", 100, IncidentFilter{Order: "triage"})
	if err != nil || len(current) != 2 || current[0].MonitorID != b.ID {
		t.Fatal("attention did not update incremental triage index", current, err)
	}
	for range 2 {
		rows, more, err := v.FilteredPage(t.Context(), next, 1, IncidentFilter{Order: "triage", State: "open"})
		if err != nil || more != "" || len(rows) != 1 || rows[0].MonitorID != b.ID {
			t.Fatal("frozen continuation moved", rows, more, err)
		}
	}
	unacked, _, err := fresh.FilteredPage(t.Context(), "", 100, IncidentFilter{Acknowledged: "false", Dismissed: "false"})
	if err != nil || len(unacked) != 1 || unacked[0].MonitorID != b.ID {
		t.Fatal("attention filter changed meaning", err)
	}
}

func TestIncidentSparseFilterHasBoundedAdvancingPages(t *testing.T) {
	f := &machine{}
	f.emptyControlIndexes()
	for n := 0; n < 5000; n++ {
		r := IncidentRecord{ID: fmt.Sprintf("incident-%05d", n), MonitorID: fmt.Sprint(n), Active: true, FirstFailureAt: policyTime(), Dismissed: n == 4999}
		f.incidents.ReplaceOrInsert(r)
		f.incidentsByTriage.ReplaceOrInsert(r)
	}
	v := IncidentView{tree: f.incidents.Clone(), triage: f.incidentsByTriage.Clone()}
	for _, order := range []string{"id", "triage"} {
		filter := IncidentFilter{Dismissed: "true", Order: order}
		rows, next, err := v.FilteredPage(t.Context(), "", 100, filter)
		if err != nil || len(rows) != 0 || next != "incident-04095" {
			t.Fatal("sparse page scanned past bound or lost continuation", len(rows), next, err)
		}
		rows, next, err = v.FilteredPage(t.Context(), next, 100, filter)
		if err != nil || len(rows) != 1 || rows[0].ID != "incident-04999" || next != "" {
			t.Fatal("sparse continuation lost matching result", rows, next, err)
		}
	}
}
