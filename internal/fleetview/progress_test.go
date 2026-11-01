package fleetview

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestControllerProgressDoesNotRewriteObservations(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	row := MonitorSummary{ID: 1, MonitorID: "m", Enabled: true, Health: "healthy", Status: "up", LastObservedHealth: "healthy", LastObservedAt: at, LastCheck: at, Incident: true, IncidentID: "prior", Availability: MonitorAvailability{State: "available", ObservedAt: at}}
	h := NewHolder()
	h.SetIndex(NewIndex([]MonitorSummary{row}))
	var progressing atomic.Bool
	progressing.Store(true)
	h.SetProgress(progressing.Load)
	if h.Get().Unavailable != 0 || h.Observation(row).Health != "healthy" {
		t.Fatal("healthy controller masked")
	}
	progressing.Store(false)
	assessment := h.ControllerAvailability()
	actual := WithControllerAvailability(row, assessment)
	if actual.Health != "unknown" || actual.Status != "unknown" || actual.LastObservedHealth != "healthy" || !actual.LastObservedAt.Equal(at) || !actual.LastCheck.Equal(at) || !actual.Incident || actual.IncidentID != "prior" || actual.CheckAdmission != "controller_unavailable" {
		t.Fatal(actual)
	}
	if h.Get().Unavailable != 1 || h.Get().ByStatus["up"] != 0 || h.Get().ByStatus["unknown"] != 1 {
		t.Fatal(h.Get())
	}
	progressing.Store(true)
	if h.Observation(row).Health != "healthy" || !reflect.DeepEqual(WithControllerAvailability(row, assessment), actual) {
		t.Fatal("live status changed retained assessment")
	}
	if saved, ok := h.Index().Get(1); !ok || saved.Health != "healthy" {
		t.Fatal("projection mutated")
	}
}

func TestControllerUnavailablePreservesPauseCounts(t *testing.T) {
	rows := []MonitorSummary{
		{ID: 1, Enabled: false, Health: "healthy", Status: "disabled", Availability: MonitorAvailability{State: "paused", Reason: "disabled"}},
		{ID: 2, Enabled: true, Health: "healthy", Status: "up", Availability: MonitorAvailability{State: "paused", Reason: "snoozed"}, SnoozedUntil: time.Now().Add(time.Hour)},
		{ID: 3, Enabled: true, Health: "healthy", Status: "up", Availability: MonitorAvailability{State: "available"}},
	}
	h := NewHolder()
	h.SetIndex(NewIndex(rows))
	h.SetProgress(func() bool { return false })
	counts := map[string]int{}
	for _, row := range rows {
		observed := h.Observation(row)
		counts[observed.Availability.State]++
		if observed.Health != "unknown" {
			t.Fatal("old observation presented as current")
		}
	}
	snap := h.Get()
	if snap.Paused != counts["paused"] || snap.Unavailable != counts["unavailable"] || snap.Paused+snap.Unavailable != snap.Total {
		t.Fatal("row/count mismatch", snap, counts)
	}
}
