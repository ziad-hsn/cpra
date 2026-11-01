//go:build externaljobs

package systems

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func openWorkerIncident(t *testing.T, s *DurableSystem, id string) persistence.Monitor {
	t.Helper()
	m, _ := s.store.Get(id)
	guard := s.managed[m.ID].guard.Clone()
	results, err := s.store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, CheckControlRevision: m.ControlRevision, Generation: 1, Outcome: "failure", At: time.Now().UTC(), Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatal("commit failed check", err, results)
	}
	m = *results[0].Monitor
	s.project(s.entities[m.ID], m)
	s.scheduleActions(s.entities[m.ID], m)
	return m
}

func TestWorkerAdmissionActionsProgressUnderContinuousChecks(t *testing.T) {
	s, _ := workerDispatchOwner(t, 2*workerDispatchCapacity)
	m := openWorkerIncident(t, s, "remote")
	at := time.Now()
	for _, ent := range s.entities {
		s.scheduler.Cancel(ent)
		s.checks[ent.ID()].next = at.UnixNano()
		s.scheduler.Schedule(ent, at)
	}
	s.lastSLO = at
	// The normal owner tick must share preparation opportunities even while a
	// check backlog can consume every available slot. No remote handler runs.
	for range 2 {
		s.Update(s.world)
		if s.lastError != nil {
			t.Fatal(s.lastError)
		}
		for s.workerRuntime.preparing > 0 {
			awaitWorkerPreparation(t, s)
		}
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, record := range page.Items {
		counts[record.Intent.Category]++
		if record.Lifecycle != nil {
			t.Fatal("admission granted handler execution")
		}
		if record.Intent.ActionID != "" {
			original, ok := m.Actions[record.Intent.ActionID]
			if !ok || !record.Intent.Scheduled.Equal(original.NotBefore) {
				t.Fatal("fairness replaced the original action or execution window")
			}
		}
	}
	if counts["check"] == 0 || counts["recovery"] != 1 || counts["notification"] != 1 {
		t.Fatalf("continuous checks starved incident actions: %v", counts)
	}
	if s.workerRuntime.ready.Len() == 0 {
		t.Fatal("fixture drained the check backlog before testing fairness")
	}
}

func TestWorkerAdmissionChecksProgressUnderContinuousActions(t *testing.T) {
	s, _ := workerDispatchOwner(t, workerDispatchCapacity)
	for id, ent := range s.entities {
		openWorkerIncident(t, s, id)
		s.scheduler.Cancel(ent)
	}
	at := time.Now()
	ent := s.entities["remote"]
	s.checks[ent.ID()].next = at.UnixNano()
	s.scheduler.Schedule(ent, at)
	s.lastSLO = at
	s.workerRuntime.actionsFirst = true
	for range 2 {
		s.Update(s.world)
		if s.lastError != nil {
			t.Fatal(s.lastError)
		}
		for s.workerRuntime.preparing > 0 {
			awaitWorkerPreparation(t, s)
		}
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, record := range page.Items {
		counts[record.Intent.Category]++
		if record.Lifecycle != nil {
			t.Fatal("admission granted handler execution")
		}
	}
	if counts["check"] != 1 || counts["recovery"] == 0 || counts["notification"] == 0 {
		t.Fatalf("continuous actions starved health checks: %v", counts)
	}
	if len(page.Items) != 2*workerDispatchCapacity || counts["recovery"]+counts["notification"] != 2*workerDispatchCapacity-1 {
		t.Fatal("fixture did not retain a saturated action backlog", counts)
	}
}

func TestWorkerAdmissionQuotaDoesNotRetryImmediately(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	s.finishWorkerDispatch()
	retry := &retryScheduleCatalog{Catalog: catalog}
	s.workerRuntime.dispatcher = newWorkerDispatcher(s.store, retry)
	s.startWorkerDispatch()
	at := time.Now()
	scheduleWorkerCheck(t, s, at)
	awaitWorkerPreparation(t, s)
	s.dispatchWorkerChecks()
	if s.workerRuntime.preparing != 0 {
		t.Fatal("confirmed quota rejection retried on the next owner tick without backoff")
	}
	if retry.attempts.Load() != 1 || s.workerRuntime.ready.Len() != 1 || s.store.SLOView(at).Reports[0].Pending != 1 {
		t.Fatal("deferring admission changed the original scheduling obligation")
	}
}

func TestWorkerAdmissionBackoffBoundsAndTypeIsolation(t *testing.T) {
	var backoff workerAdmissionBackoff
	key := workerAdmissionKey{id: "checks", uid: "original"}
	other := workerAdmissionKey{id: "checks", uid: "replacement"}
	now := time.Now()
	for attempt := range 12 {
		backoff.observe(key, fmt.Errorf("admission rejected: %w", persistence.ErrWorkerExecutionQuota), now)
		retry := backoff.types[key]
		if retry.delay < workerAdmissionRetryMin || retry.delay > workerAdmissionRetryMax || backoff.ready(key, now) || !backoff.ready(other, now) {
			t.Fatal("quota delay was unbounded or blocked a different incarnation", attempt, retry)
		}
		for range workerDispatchCapacity {
			backoff.observe(key, persistence.ErrWorkerExecutionQuota, now)
		}
		if backoff.types[key] != retry || !backoff.ready(key, retry.until) {
			t.Fatal("one rejected batch multiplied backoff or prevented the next attempt")
		}
		now = retry.until
	}
	if backoff.types[key].delay != workerAdmissionRetryMax {
		t.Fatal("sustained saturation did not reach the bounded retry interval")
	}
	backoff.observe(key, nil, now)
	if !backoff.ready(key, now) || len(backoff.types) != 0 {
		t.Fatal("successful admission did not release its temporary delay")
	}
	backoff.observe(key, persistence.ErrCommitUnconfirmed, now)
	if len(backoff.types) != 0 {
		t.Fatal("uncertain submission entered confirmed-rejection backoff")
	}
}

func TestWorkerAdmissionBackoffStorageIsBounded(t *testing.T) {
	var backoff workerAdmissionBackoff
	now := time.Now()
	for i := range workerAdmissionTypes + 1 {
		backoff.observe(workerAdmissionKey{id: fmt.Sprint(i), uid: "incarnation"}, persistence.ErrWorkerExecutionQuota, now)
	}
	if len(backoff.types) != workerAdmissionTypes || backoff.ready(workerAdmissionKey{}, now) {
		t.Fatal("type churn exceeded its memory bound or omitted overflow throttling")
	}
	next := now.Add(workerAdmissionRetryMin)
	key := workerAdmissionKey{id: "next", uid: "incarnation"}
	backoff.observe(key, persistence.ErrWorkerExecutionQuota, next)
	if len(backoff.types) != 1 || backoff.ready(key, next) || !backoff.ready(workerAdmissionKey{}, next) {
		t.Fatal("expired delays could not release their bounded slots")
	}
}

type quotaAdmissionStore struct {
	*persistence.Store
	allocations atomic.Int32
}

func (*quotaAdmissionStore) CheckWorkerExecutionCapacity(context.Context, persistence.JobTypeReference) error {
	return persistence.ErrWorkerExecutionQuota
}

func (s *quotaAdmissionStore) AllocateWorkerCheckID(ctx context.Context) (string, error) {
	s.allocations.Add(1)
	return s.Store.AllocateWorkerCheckID(ctx)
}

func TestWorkerAdmissionFullStoreDoesNotAllocateChecks(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	s.finishWorkerDispatch()
	full := &quotaAdmissionStore{Store: s.store}
	s.workerRuntime.dispatcher = newWorkerDispatcher(full, catalog)
	s.startWorkerDispatch()
	retainWorkerCheckObservations(s, persistence.MaxWorkerExecutions)
	at := time.Now()
	scheduleWorkerCheck(t, s, at)
	awaitWorkerPreparation(t, s)
	if full.allocations.Load() != 0 || s.workerRuntime.ready.Len() != 1 || s.store.SLOView(at).Reports[0].Pending != 1 {
		t.Fatal("known capacity exhaustion allocated an identity or lost the scheduled check")
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("capacity preflight admitted an assignment", err)
	}
}

func retainWorkerCheckObservations(s *DurableSystem, count int) {
	// Completed check records may be retired before the owner next observes
	// them. Their original scalar observations still occupy tracking slots.
	r := s.workerRuntime
	for i := range count {
		token := uuid.New()
		r.pending[token] = &workerPending{token: token, monitorID: fmt.Sprintf("retired-%d", i),
			uid: uuid.NewString(), revision: uuid.NewString(), executionID: "wc." + uuid.NewString()}
		r.observations[r.pending[token].executionID] = len(r.order)
		r.order = append(r.order, token)
	}
}

func TestWorkerAdmissionFullObservationCapacityStillExpiresActions(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	before := queuedExpiredWorkerActions(t, s)
	retainWorkerCheckObservations(s, persistence.MaxWorkerExecutions)
	for _, action := range before.Actions {
		if !s.dispatchWorkerAction(s.entities[before.ID], action, action.Endpoint) {
			t.Fatal("worker action not recognized")
		}
	}
	if s.workerRuntime.preparing != 2 {
		t.Fatal("full observation capacity prevented expired actions from reaching cancellation")
	}
	for s.workerRuntime.preparing > 0 {
		awaitWorkerPreparation(t, s)
	}
	after, _ := s.store.Get(before.ID)
	for id := range before.Actions {
		action, found, err := s.store.ActionContext(t.Context(), id)
		if err != nil || !found || action.MonitorID != before.ID {
			t.Fatal("original expired action is missing", err)
		}
		if action.State != persistence.Cancelled || action.Outcome != "worker_admission_expired" ||
			!action.StartedAt.IsZero() || action.ExecutorKind != "" || s.admitted[action.ID] {
			t.Fatal("expiry did not complete independently of execution observations", action)
		}
	}
	if after.TotalChecks != before.TotalChecks || after.InterventionAttempts != before.InterventionAttempts {
		t.Fatal("expiry fabricated a check or recovery attempt")
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("expiry created an execution", err)
	}
}

func TestWorkerAdmissionFullObservationCapacityTransfersCommittedCheck(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	retainWorkerCheckObservations(s, persistence.MaxWorkerExecutions)
	at := time.Now()
	scheduleWorkerCheck(t, s, at)
	if s.workerRuntime.preparing != 1 {
		t.Fatal("full observation capacity prevented preparation")
	}
	token := s.pendingCheckID["remote"]
	awaitWorkerPreparation(t, s)
	r := s.workerRuntime
	p := r.pending[token]
	m, _ := s.store.Get("remote")
	state, exists, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	if err != nil || !exists || p == nil || p.executionID != state.ID || s.pendingCheckID[m.ID] != token ||
		r.preparing != 0 || len(r.completed) != 0 || len(r.order) > persistence.MaxWorkerExecutions || len(r.pending) != len(r.order) {
		t.Fatal("committed check lost its bounded owner handoff or original identity", err)
	}
	view := s.store.SLOView(at)
	if len(view.Reports) != 1 || view.Reports[0].Pending != 1 || view.Reports[0].Missed != 0 {
		t.Fatal("handoff counted the original schedule twice", view)
	}
	record, _, _ := s.store.WorkerExecution(t.Context(), state.ID)
	if count, err := s.store.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline, 1); err != nil || count != 1 {
		t.Fatal("finalize committed check", count, err)
	}
	for range persistence.MaxWorkerExecutions / 16 {
		s.receiveWorkerDispatch()
		if !s.pending[m.ID] {
			break
		}
	}
	if s.lastError != nil || s.pending[m.ID] || s.states.Get(s.entities[m.ID]).PulseGeneration != 1 {
		t.Fatal("committed completion disappeared behind stale observations", s.lastError)
	}
}

func TestWorkerAdmissionFullObservationCapacityBoundsPreparationAndShutdown(t *testing.T) {
	s, catalog := workerDispatchOwner(t, workerDispatchCapacity+4)
	s.finishWorkerDispatch()
	r := s.workerRuntime
	blocked := &blockedDispatchCatalog{Catalog: catalog, entered: make(chan struct{}, workerDispatchCapacity)}
	r.dispatcher = newWorkerDispatcher(s.store, blocked)
	s.startWorkerDispatch()
	retainWorkerCheckObservations(s, persistence.MaxWorkerExecutions)
	at := time.Now()
	for _, ent := range s.entities {
		s.scheduler.Cancel(ent)
		s.checks[ent.ID()].next = at.UnixNano()
		s.scheduler.Schedule(ent, at)
	}
	s.dispatchChecks(at)
	if r.preparing != workerDispatchCapacity || len(r.pending) != persistence.MaxWorkerExecutions+workerDispatchCapacity ||
		len(r.order) != persistence.MaxWorkerExecutions || r.ready.Len() != 4 || r.bytes > workerDispatchBytes {
		t.Fatal("preparation and observation capacity were not independently bounded", r.preparing, len(r.pending), len(r.order), r.ready.Len())
	}
	s.StopAdmission()
	s.Finalize(s.world)
	if s.lastError != nil || r.preparing != 0 || r.bytes != 0 {
		t.Fatal("shutdown lost preparation ownership under full observation capacity", s.lastError)
	}
	view := s.store.SLOView(at)
	if len(view.Reports) != 1 || view.Reports[0].Expected != 20 || view.Reports[0].Missed != 20 || view.Reports[0].Pending != 0 {
		t.Fatal("shutdown lost scheduled obligations", view)
	}
	for id := range s.entities {
		if s.pending[id] {
			t.Fatal("canceled preparation remained pending behind old observations", id)
		}
	}
}
