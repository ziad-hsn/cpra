package fleetview

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestObservationMembershipRetainsIncarnation(t *testing.T) {
	first := MonitorSummary{ID: 1, MonitorID: "a", IncarnationUID: "original", Name: "First", ActiveCodes: []string{"red"}}
	second := MonitorSummary{ID: 2, MonitorID: "b", IncarnationUID: "second", Name: "Second"}
	index := NewIndex([]MonitorSummary{first, second})
	view := index.Snapshot()
	first.Health = "healthy"
	index.Put(first)
	// Ordinary observations advance in retained membership.
	rows, next, err := view.Page(t.Context(), "", "", 1)
	if err != nil || len(rows) != 1 || rows[0].Health != "healthy" || next != "a" {
		t.Fatal(rows, next, err)
	}
	rows[0].ActiveCodes[0] = "green"
	got, _ := index.GetStable("a")
	if got.ActiveCodes[0] != "red" {
		t.Fatal("reader mutated published row")
	}
	index.Remove(2)
	second.IncarnationUID, second.Health = "replacement", "unhealthy"
	index.Put(second)
	first.IncarnationUID = "replacement-without-remove"
	index.Put(first)
	rows, next, err = view.Page(t.Context(), "", "a", 1)
	if err != nil || len(rows) != 1 || rows[0].IncarnationUID != "second" || next != "" {
		t.Fatal(rows, next, err)
	}
	rows, _, _ = view.Page(t.Context(), "a", "", 1)
	if len(rows) != 1 || rows[0].IncarnationUID != "original" {
		t.Fatal("captured membership acquired replacement", rows)
	}
	got, _ = index.GetStable("b")
	if got.IncarnationUID != "replacement" {
		t.Fatal("new reader did not see replacement")
	}
}

func TestObservationPagesSeekAndBoundMemory(t *testing.T) {
	index := NewIndex(nil)
	for n := range 10000 {
		index.Put(MonitorSummary{ID: uint32(n), MonitorID: fmt.Sprintf("m-%05d", n), IncarnationUID: "initial"})
	}
	view := index.Snapshot()
	rows, next, err := view.Page(t.Context(), "", "m-09898", 100)
	if err != nil || len(rows) != 100 || rows[0].MonitorID != "m-09899" || next != "m-09998" {
		t.Fatal(len(rows), next, err)
	}
	rows, next, err = view.Page(t.Context(), "", next, 100)
	if err != nil || len(rows) != 1 || rows[0].MonitorID != "m-09999" || next != "" {
		t.Fatal(rows, next, err)
	}
	if allocs := testing.AllocsPerRun(10, func() { _, _, _ = view.Page(context.Background(), "", "m-09000", 10) }); allocs > 20 {
		t.Fatalf("bounded page allocated %.0f objects", allocs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := view.Page(ctx, "", "", 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
