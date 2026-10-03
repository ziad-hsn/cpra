//go:build externaljobs

package systems

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func stopWorkerCatalogUpdates(t *testing.T, s *DurableSystem) {
	t.Helper()
	// Drive the normal preparation/owner handoff deterministically below while
	// the real encrypted catalog, Raft store and worker dispatcher stay active.
	s.catalogRuntime.cancel()
	select {
	case <-s.catalogRuntime.done:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog update loop did not stop")
	}
}

func renameWorkerMonitor(t *testing.T, s *DurableSystem, catalog *management.Catalog, id, name string) {
	t.Helper()
	resource, err := catalog.Get(t.Context(), "Monitor", id)
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := s.store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: id})
	if err != nil || !exists {
		t.Fatal("missing original monitor", err)
	}
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{91}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	resource.Metadata.Name = api.Pointer(name)
	commitCatalogExternalFixture(t, s.store, sealer, resource, record.References, record.JobTypeReferences)
	projection := prepareRecoveryProjection(t, s, id)
	s.startCatalogProjection(projection)
	assertProjectionAck(t, projection, nil)
}

func TestWorkerReconfigurationKeepsOneObservationOfExistingCheck(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	stopWorkerCatalogUpdates(t, s)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	original, exists, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	if err != nil || !exists {
		t.Fatal("missing original execution", err)
	}
	for _, name := range []string{"first", "second", "third"} {
		renameWorkerMonitor(t, s, catalog, m.ID, name)
		if s.states.Get(s.entities[m.ID]).Revision != m.Revision {
			t.Fatal("metadata-only edit changed execution content")
		}
		scheduleWorkerCheck(t, s, time.Now())
		awaitWorkerPreparation(t, s)
		r := s.workerRuntime
		if len(r.order) != 1 || len(r.pending) != 1 || r.preparing != 0 {
			t.Fatalf("reconfiguration multiplied one execution's tracking: observations=%d pending=%d preparing=%d", len(r.order), len(r.pending), r.preparing)
		}
		p := r.pending[s.pendingCheckID[m.ID]]
		if p == nil || p.executionID != original.ID {
			t.Fatal("current monitor lost its original committed check")
		}
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Lifecycle != nil {
		t.Fatal("metadata edits admitted or invoked another check", err)
	}
	view := s.store.SLOView(time.Now())
	if len(view.Reports) != 1 || view.Reports[0].Missed != 3 || view.Reports[0].Pending != 1 {
		t.Fatal("reconciliation lost or duplicated original schedule obligations", view)
	}
}

type delayedWorkerLookup struct {
	*persistence.Store
	delayed atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *delayedWorkerLookup) FindWorkerExecution(ctx context.Context, monitorID, monitorUID, actionID string) (persistence.WorkerDispatchState, bool, error) {
	state, exists, err := s.Store.FindWorkerExecution(ctx, monitorID, monitorUID, actionID)
	if err == nil && exists && s.delayed.CompareAndSwap(false, true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return persistence.WorkerDispatchState{}, false, ctx.Err()
		}
	}
	return state, exists, err
}

func TestWorkerReconfigurationLatePreparationPreservesCurrentToken(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	stopWorkerCatalogUpdates(t, s)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	original, _, _ := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	s.finishWorkerDispatch()
	delayed := &delayedWorkerLookup{Store: s.store, entered: make(chan struct{}), release: make(chan struct{})}
	s.workerRuntime.dispatcher = newWorkerDispatcher(delayed, catalog)
	s.startWorkerDispatch()
	defer close(delayed.release)
	renameWorkerMonitor(t, s, catalog, m.ID, "older")
	scheduleWorkerCheck(t, s, time.Now())
	older := s.pendingCheckID[m.ID]
	select {
	case <-delayed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("earlier lookup did not reach the delayed reply")
	}
	renameWorkerMonitor(t, s, catalog, m.ID, "current")
	scheduleWorkerCheck(t, s, time.Now())
	current := s.pendingCheckID[m.ID]
	awaitWorkerPreparation(t, s)
	delayed.release <- struct{}{}
	awaitWorkerPreparation(t, s)
	r := s.workerRuntime
	if s.pendingCheckID[m.ID] != current || !s.pending[m.ID] || r.pending[current] == nil || r.pending[older] != nil ||
		len(r.order) != 1 || len(r.pending) != 1 || r.preparing != 0 {
		t.Fatal("late preparation replaced the current token or retained a duplicate observation")
	}
	record, _, _ := s.store.WorkerExecution(t.Context(), original.ID)
	if count, err := s.store.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline, 1); err != nil || count != 1 {
		t.Fatal("finalize original check", count, err)
	}
	s.receiveWorkerDispatch()
	if s.lastError != nil || s.pending[m.ID] || len(r.pending) != 0 || s.states.Get(s.entities[m.ID]).PulseGeneration != 1 {
		t.Fatal("original completion failed to release the current monitor", s.lastError)
	}
}

func TestWorkerReconfigurationAfterEarlierObservationCompletes(t *testing.T) {
	s, catalog := workerDispatchOwner(t, 2)
	stopWorkerCatalogUpdates(t, s)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	first := s.workerRuntime.pending[s.pendingCheckID["remote"]]
	firstRecord, exists, err := s.store.WorkerExecution(t.Context(), first.executionID)
	if err != nil || !exists {
		t.Fatal("missing first execution", err)
	}
	ent := s.entities["remote-01"]
	schedule := func() {
		at := time.Now()
		s.scheduler.Cancel(ent)
		s.checks[ent.ID()].next = at.UnixNano()
		s.scheduler.Schedule(ent, at)
		s.dispatchChecks(at)
		awaitWorkerPreparation(t, s)
	}
	schedule()
	original := s.workerRuntime.pending[s.pendingCheckID["remote-01"]].executionID
	if count, err := s.store.FinalizeWorkerExecutions(t.Context(), firstRecord.Intent.Deadline, 1); err != nil || count != 1 {
		t.Fatal("finalize first execution", count, err)
	}
	s.workerRuntime.cursor = 0
	s.receiveWorkerDispatch()
	if s.lastError != nil || len(s.workerRuntime.order) != 1 || s.pending["remote"] || !s.pending["remote-01"] {
		t.Fatal("completion did not preserve the remaining monitor", s.lastError)
	}
	// Removing an earlier observation moves the last one. Adopting that moved
	// execution must find its new position and retain the current monitor token.
	renameWorkerMonitor(t, s, catalog, "remote-01", "renamed remaining monitor")
	schedule()
	r := s.workerRuntime
	current := s.pendingCheckID["remote-01"]
	if len(r.order) != 1 || len(r.pending) != 1 || len(r.observations) != 1 || r.order[0] != current || r.pending[current].executionID != original {
		t.Fatal("reconfiguration duplicated or lost a moved execution observation")
	}
	secondRecord, exists, err := s.store.WorkerExecution(t.Context(), original)
	if err != nil || !exists {
		t.Fatal("missing remaining execution", err)
	}
	if count, err := s.store.FinalizeWorkerExecutions(t.Context(), secondRecord.Intent.Deadline, 1); err != nil || count != 1 {
		t.Fatal("finalize remaining execution", count, err)
	}
	s.receiveWorkerDispatch()
	if s.lastError != nil || s.pending["remote-01"] || len(r.order)+len(r.observations)+len(r.pending) != 0 {
		t.Fatal("final completion retained an observation or monitor hold", s.lastError)
	}
}

func TestWorkerReconfigurationAtFullObservationCapacity(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	stopWorkerCatalogUpdates(t, s)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	original, exists, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	if err != nil || !exists {
		t.Fatal("missing original execution", err)
	}
	r := s.workerRuntime
	const changes = 20
	for i := range changes {
		// Retired scalar observations model a full owner queue, not live worker
		// handlers. Refill between edits so every handoff starts at capacity.
		retainWorkerCheckObservations(s, persistence.MaxWorkerExecutions-len(r.order))
		renameWorkerMonitor(t, s, catalog, m.ID, fmt.Sprintf("revision-%d", i))
		scheduleWorkerCheck(t, s, time.Now())
		awaitWorkerPreparation(t, s)
		current := s.pendingCheckID[m.ID]
		if r.preparing != 0 || len(r.completed) != 0 || len(r.pending) != len(r.order) ||
			len(r.observations) != len(r.order) || len(r.order) > persistence.MaxWorkerExecutions ||
			r.pending[current] == nil || r.pending[current].executionID != original.ID {
			t.Fatal("reconfiguration exceeded tracking bounds or lost the current execution", i)
		}
		observed := 0
		for _, token := range r.order {
			if r.pending[token].executionID == original.ID {
				observed++
			}
		}
		if observed != 1 {
			t.Fatal("reconfiguration retained duplicate execution observations", i, observed)
		}
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Intent.ID != original.ID || page.Items[0].Lifecycle != nil {
		t.Fatal("renaming admitted or invoked another check", err)
	}
	view := s.store.SLOView(time.Now())
	if len(view.Reports) != 1 || view.Reports[0].Missed != changes || view.Reports[0].Pending != 1 {
		t.Fatal("capacity pressure lost or duplicated scheduling obligations", view)
	}
}
