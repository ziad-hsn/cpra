//go:build externaljobs

package systems

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestWorkerScheduleReadyPreparationAndShutdownAccounting(t *testing.T) {
	s, catalog := workerDispatchOwner(t, 20)
	s.finishWorkerDispatch()
	r := s.workerRuntime
	blocked := &blockedDispatchCatalog{Catalog: catalog, entered: make(chan struct{}, workerDispatchCapacity)}
	r.dispatcher = newWorkerDispatcher(s.store, blocked)
	s.startWorkerDispatch()
	at := time.Now()
	for _, ent := range s.entities {
		s.scheduler.Cancel(ent)
		s.checks[ent.ID()].next = at.UnixNano()
		s.scheduler.Schedule(ent, at)
	}
	s.dispatchChecks(at)
	for range workerDispatchConcurrency {
		select {
		case <-blocked.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("preparation did not reach capacity")
		}
	}
	if r.preparing != 16 || r.ready.Len() != 4 {
		t.Fatal("fixture did not saturate preparation")
	}
	view := s.store.SLOView(at)
	if len(view.Reports) != 1 || view.Reports[0].Pending != 20 || view.Reports[0].Samples != 0 {
		t.Fatal("ready or preparing schedules disappeared", view)
	}
	view = s.store.SLOView(at.Add(12 * time.Second))
	if view.Reports[0].Expected != 20 || view.Reports[0].Overdue != 20 {
		t.Fatal("unadmitted schedules improved attainment", view)
	}
	// Configuration/control cancellation consumes a ready ticket once and does
	// not remove an already due obligation from the denominator.
	for id, ent := range s.entities {
		if s.checks[ent.ID()].ready != 0 {
			s.cancelSchedules(ent, id)
			s.cancelSchedules(ent, id)
			break
		}
	}
	view = s.store.SLOView(at)
	if view.Reports[0].Missed != 1 || view.Reports[0].Pending != 19 {
		t.Fatal("ready cancellation lost or doubled the obligation", view)
	}
	s.StopAdmission()
	s.Finalize(s.world)
	if s.lastError != nil {
		t.Fatal(s.lastError)
	}
	view = s.store.SLOView(at)
	if view.Reports[0].Missed != 20 || view.Reports[0].Pending != 0 || view.Reports[0].Samples != 0 {
		t.Fatal("shutdown lost accepted schedules", view)
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("accounting invoked a handler or admitted execution", err)
	}
}

type retryScheduleCatalog struct {
	*management.Catalog
	attempts atomic.Int32
}

func (c *retryScheduleCatalog) WorkerExecutionTimeout(ctx context.Context, ref persistence.JobTypeReference, timeout time.Duration) (time.Duration, error) {
	if c.attempts.Add(1) == 1 {
		return 0, persistence.ErrWorkerExecutionQuota
	}
	return c.Catalog.WorkerExecutionTimeout(ctx, ref, timeout)
}

func TestWorkerScheduleRetryKeepsOneOriginalObligation(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	s.finishWorkerDispatch()
	retry := &retryScheduleCatalog{Catalog: catalog}
	s.workerRuntime.dispatcher = newWorkerDispatcher(s.store, retry)
	s.startWorkerDispatch()
	at := time.Now()
	scheduleWorkerCheck(t, s, at)
	awaitWorkerPreparation(t, s)
	if s.pending["remote"] || s.workerRuntime.ready.Len() != 1 {
		t.Fatal("confirmed rejection did not requeue the original check")
	}
	ent := s.entities["remote"]
	ticket := s.checks[ent.ID()].workerSchedule
	if ticket.Sequence != 1 || s.store.SLOView(at).Reports[0].Pending != 1 {
		t.Fatal("retry lost its original obligation")
	}
	key := s.jobs.Get(ent).ExternalJobs.Check.Key.JobType
	retryAt := s.workerRuntime.admission.types[workerAdmissionKey{id: key.JobTypeID, uid: key.JobTypeUID}].until
	timer := time.NewTimer(time.Until(retryAt))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	s.dispatchWorkerChecks()
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	record, ok, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	if err != nil || !ok || record.Schedule != ticket || retry.attempts.Load() != 2 {
		t.Fatal("retry changed scheduling identity", err)
	}
	view := s.store.SLOView(at)
	if view.Reports[0].Pending != 1 || view.Reports[0].Missed != 0 {
		t.Fatal("retry counted the check twice", view)
	}
}
