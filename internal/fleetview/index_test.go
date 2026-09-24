package fleetview

import (
	"fmt"
	"sync"
	"testing"
)

func TestIndexDynamicMembershipRenameAndIsolation(t *testing.T) {
	input := []MonitorSummary{{ID: 2, Name: "zulu", Status: "down", PulseType: "http", ActiveCodes: []string{"red"}}, {ID: 1, Name: "alpha", Status: "up", PulseType: "tcp"}}
	i := NewIndex(input)
	if input[0].ID != 2 {
		t.Fatal("constructor reordered caller data")
	}
	input[0].ActiveCodes[0] = "mutated"
	got, _ := i.Get(2)
	if got.ActiveCodes[0] != "red" {
		t.Fatal("caller retained mutable row data")
	}
	got.ActiveCodes[0] = "changed"
	got, _ = i.Get(2)
	if got.ActiveCodes[0] != "red" {
		t.Fatal("Get leaked row ownership")
	}
	i.Put(MonitorSummary{ID: 3, Name: "middle", Status: "disabled", PulseType: "http"})
	i.Put(MonitorSummary{ID: 2, Name: "aardvark", Status: "up", PulseType: "http"})
	page, total := i.Page(0, 100, nil)
	if total != 3 || len(page) != 3 || page[0].ID != 2 || page[1].ID != 1 || page[2].ID != 3 {
		t.Fatalf("wrong membership/order: %v %d", page, total)
	}
	overview := i.Overview()
	if overview.Total != 3 || overview.Disabled != 1 || overview.ByStatus["up"] != 2 || overview.ByStatus["down"] != 0 {
		t.Fatalf("wrong aggregate: %+v", overview)
	}
	if !i.Remove(3) || i.Remove(3) {
		t.Fatal("delete is not idempotent")
	}
	if _, ok := i.Get(3); ok {
		t.Fatal("deleted member remained readable")
	}
	if i.Overview().Total != 2 || i.Overview().Disabled != 0 {
		t.Fatal("delete aggregate wrong")
	}
	i.Put(MonitorSummary{ID: 3, Name: "new incarnation", Status: "down"})
	if i.Overview().Total != 3 {
		t.Fatal("reused numeric route not inserted")
	}
}

func TestIndexFilteringDoesNotBlockEdits(t *testing.T) {
	i := NewIndex([]MonitorSummary{{ID: 1, Name: "one"}, {ID: 2, Name: "two"}})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		i.Page(0, 100, func(m MonitorSummary) bool {
			if m.ID == 1 {
				close(entered)
				<-release
			}
			return true
		})
	}()
	<-entered
	// This must complete while the filter is suspended. Locking while scanning
	// would block the controller here and deadlock the test.
	i.Put(MonitorSummary{ID: 3, Name: "three"})
	i.Remove(2)
	close(release)
	<-done
}

func TestIndexConcurrentReadsAndMembership(t *testing.T) {
	i := NewIndex(nil)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 300; n++ {
				id := uint32(worker*1000 + n)
				i.Put(MonitorSummary{ID: id, Name: fmt.Sprintf("monitor-%d", id), Status: "up"})
				i.Page(n%3, 5, nil)
				i.Get(id)
				i.Overview()
				i.Put(MonitorSummary{ID: id, Name: fmt.Sprintf("renamed-%d", id), Status: "down"})
				i.Remove(id)
			}
		}(worker)
	}
	wg.Wait()
	if got := i.Overview(); got.Total != 0 {
		t.Fatalf("rows lost or counted twice: %+v", got)
	}
}
