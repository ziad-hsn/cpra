//go:build externaljobs

package systems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func workerDispatchOwner(t *testing.T, count ...int) (*DurableSystem, *management.Catalog) {
	t.Helper()
	cfg := catalogExternalConfig(t)
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(), Principals: []persistence.AuthenticationPrincipal{{ID: "team/operator", Role: "operator", TokenSHA256: strings.Repeat("12", 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{91}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	drivers := make(map[string]api.DriverConfig)
	var refs []persistence.JobTypeReference
	for _, category := range []string{"check", "recovery", "notification"} {
		job := api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "dispatch-" + category}, Spec: api.JobTypeSpec{Kind: category, Version: "1", Handler: "dispatch-" + category, ProtocolVersion: "1", Timeout: "30s", ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), ResultSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}}
		prepared, err := catalog.PrepareJobType(t.Context(), job, "", true, "team/operator")
		if err != nil {
			t.Fatal(err)
		}
		job, err = catalog.CommitJobType(t.Context(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, persistence.JobTypeReference{JobTypeID: job.Metadata.ID, JobTypeUID: job.Metadata.UID, Revision: job.Metadata.ResourceVersion, Version: "1", Category: category})
		drivers[category] = api.DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"dispatch-` + category + `","version":"1","parameters":{}}`)}
	}
	rules := map[string]api.AlertRule{"yellow": {Driver: api.Pointer(drivers["notification"])}}
	spec, err := json.Marshal(api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Driver: drivers["check"], Interval: "60s", Timeout: "30s", UnhealthyThreshold: api.Pointer(int64(1))}, Recovery: &api.RecoverySpec{Driver: drivers["recovery"]}, Notifications: &rules})
	if err != nil {
		t.Fatal(err)
	}
	commitCatalogExternalFixture(t, store, sealer, api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "remote"}, Spec: spec}, nil, refs)
	if len(count) > 0 {
		for n := 1; n < count[0]; n++ {
			commitCatalogExternalFixture(t, store, sealer, api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: fmt.Sprintf("remote-%02d", n)}, Spec: spec}, nil, refs)
		}
	}
	w := ecs.NewWorld()
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, &admissionQueue{}, &admissionQueue{}, nil, time.Minute, false)
	if err := s.LoadCatalog(t.Context(), catalog, entities.NewEntityManager(&w)); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	if s.lastError != nil {
		t.Fatal(s.lastError)
	}
	t.Cleanup(func() { s.StopAdmission(); s.Finalize(&w) })
	return s, catalog
}

func awaitWorkerPreparation(t *testing.T, s *DurableSystem) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	// Wait on a real completion, then return it to the owner without making a
	// worker goroutine mutate the ECS world. The pool has a completion per request.
	select {
	case result := <-s.workerRuntime.dispatcher.results:
		s.workerRuntime.dispatcher.results <- result
		s.receiveWorkerDispatch()
		if s.lastError != nil {
			t.Fatal(s.lastError)
		}
	case <-deadline.C:
		t.Fatal("worker preparation did not complete")
	}
}

func scheduleWorkerCheck(t *testing.T, s *DurableSystem, at time.Time) {
	t.Helper()
	ent := s.entities["remote"]
	s.scheduler.Cancel(ent)
	s.checks[ent.ID()].next = at.UnixNano()
	s.scheduler.Schedule(ent, at)
	s.dispatchChecks(at)
}

func TestWorkerDispatchCheckProgressesWithFullLocalQueue(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	scheduleWorkerCheck(t, s, time.Now())
	if !s.pending["remote"] || s.workerRuntime.preparing != 1 {
		t.Fatal("full local queue starved remote dispatch")
	}
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	execution, found, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	if err != nil || !found || execution.Terminal || execution.Generation != 1 {
		t.Fatal("missing committed assignment", execution, err)
	}
	record, _, err := s.store.WorkerExecution(t.Context(), execution.ID)
	if err != nil || !strings.HasPrefix(record.Intent.ID, "wc.") || record.Intent.Deadline.Sub(record.Intent.Scheduled) != 30*time.Second {
		t.Fatal("identity allocation or pinned timeout missing", err)
	}
	if len(s.queues["pulse"].(*admissionQueue).admitted) != 0 || s.PulseArrivalRate() != 0 {
		t.Fatal("remote check occupied a local worker")
	}
	count, err := s.store.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline.Add(time.Millisecond), 100)
	if err != nil || count != 1 {
		t.Fatal("finalize missing check", count, err)
	}
	s.receiveWorkerDispatch()
	if s.pending["remote"] || len(s.workerRuntime.pending) != 0 || s.states.Get(s.entities["remote"]).PulseGeneration != 1 {
		t.Fatal("committed completion did not release owner generation")
	}
}

func TestWorkerDispatchActionsKeepSeparateOriginalIdentities(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	m, _ := s.store.Get("remote")
	guard := s.managed[m.ID].guard.Clone()
	results, err := s.store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, CheckControlRevision: m.ControlRevision, Generation: 1, Outcome: "failure", At: time.Now().UTC(), Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatal("commit failed check", err, results)
	}
	m = *results[0].Monitor
	ent := s.entities[m.ID]
	s.project(ent, m)
	s.scheduleActions(ent, m)
	s.dispatchActions(time.Now())
	if s.workerRuntime.preparing != 2 {
		t.Fatalf("expected recovery and yellow notification, got %d", s.workerRuntime.preparing)
	}
	for s.workerRuntime.preparing > 0 {
		awaitWorkerPreparation(t, s)
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("distinct committed action assignments", err, len(page.Items))
	}
	for _, record := range page.Items {
		if record.Lifecycle != nil || record.Intent.ActionID == "" || m.Actions[record.Intent.ActionID].State != persistence.Queued || !record.Intent.Scheduled.Equal(m.Actions[record.Intent.ActionID].NotBefore) {
			t.Fatal("dispatch granted execution or changed action timing")
		}
	}
	s.dispatchActions(time.Now())
	if s.workerRuntime.preparing != 0 || len(s.queues["intervention"].(*admissionQueue).admitted)+len(s.queues["code"].(*admissionQueue).admitted) != 0 {
		t.Fatal("queued external actions were duplicated or sent to local pools")
	}
}

type dispatchLostReplyStore struct {
	*persistence.Store
	commits atomic.Int32
	flushes chan struct{}
	release chan struct{}
}

func (s *dispatchLostReplyStore) CommitWorkerExecution(ctx context.Context, in persistence.WorkerExecutionIntent) (persistence.WorkerExecutionRecord, error) {
	s.commits.Add(1)
	r, err := s.Store.CommitWorkerExecution(ctx, in)
	if err != nil {
		return r, err
	}
	return persistence.WorkerExecutionRecord{}, errors.Join(persistence.ErrCommitUnconfirmed, context.DeadlineExceeded)
}
func (s *dispatchLostReplyStore) Flush(ctx context.Context) error {
	select {
	case s.flushes <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return s.Store.Flush(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestWorkerDispatchLostReplyKeepsIdentityAndShutdownOwner(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	s.finishWorkerDispatch()
	lost := &dispatchLostReplyStore{Store: s.store, flushes: make(chan struct{}, 1), release: make(chan struct{})}
	r := s.workerRuntime
	r.dispatcher = newWorkerDispatcher(lost, catalog)
	r.dispatcher.start()
	defer close(lost.release)
	scheduleWorkerCheck(t, s, time.Now())
	select {
	case <-lost.flushes:
	case <-time.After(5 * time.Second):
		t.Fatal("uncertain commit was not reconciled")
	}
	view := s.store.SLOView(time.Now())
	if len(view.Reports) != 1 || view.Reports[0].Pending != 1 || view.Reports[0].Missed != 0 {
		t.Fatal("lost reply double-counted scheduler and committed assignment", view)
	}
	r.dispatcher.cancel()
	select {
	case <-r.dispatcher.done:
		t.Fatal("shutdown abandoned an uncertain committed assignment")
	default:
	}
	// Let the exact FIFO barrier settle. Cancellation must observe the committed
	// identity before the pool can join, without a second admission attempt.
	lost.release <- struct{}{}
	awaitWorkerPreparation(t, s)
	<-r.dispatcher.done
	if lost.commits.Load() != 1 || r.preparing != 0 || !s.pending["remote"] {
		t.Fatal("lost reply changed identity or discarded committed work")
	}
}

func TestWorkerDispatchExpiredScheduleDoesNotInvokeOrFabricateHealth(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	ent := s.entities["remote"]
	at := time.Now().Add(-time.Minute)
	s.checks[ent.ID()].ready = at.UnixNano()
	if !s.dispatchWorkerCheck(ent) {
		t.Fatal("external check not recognized")
	}
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 || m.TotalChecks != 0 || s.pending[m.ID] || s.states.Get(ent).MissedChecks != 1 {
		t.Fatal("expired schedule was dispatched, hidden, or fabricated a health result", err)
	}
	reports := s.store.SLOView(time.Now()).Reports
	if len(reports) != 1 || reports[0].Missed != 1 || reports[0].Samples != 0 || reports[0].Expected != 1 {
		t.Fatal("missed schedule missing from SLO", reports)
	}
}

func queuedExpiredWorkerActions(t *testing.T, s *DurableSystem) persistence.Monitor {
	t.Helper()
	m, _ := s.store.Get("remote")
	guard := s.managed[m.ID].guard.Clone()
	results, err := s.store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID,
		Revision: m.Revision, CheckControlRevision: m.ControlRevision, Generation: 1, Outcome: "failure",
		At: time.Now().UTC().Add(-time.Minute), Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatal("queue original actions", err, results)
	}
	m = *results[0].Monitor
	s.project(s.entities[m.ID], m)
	return m
}

func TestWorkerDispatchCommitsExpiredActionsWithoutAssignments(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	before := queuedExpiredWorkerActions(t, s)
	s.scheduleActions(s.entities[before.ID], before)
	s.dispatchActions(time.Now())
	if s.workerRuntime.preparing != 2 {
		t.Fatal("recovery and notification were not dispatched for expiry")
	}
	for s.workerRuntime.preparing > 0 {
		awaitWorkerPreparation(t, s)
	}
	after, _ := s.store.Get(before.ID)
	if len(after.Actions) != 2 || after.TotalChecks != before.TotalChecks || after.InterventionAttempts != before.InterventionAttempts {
		t.Fatal("expiry changed health or attempts")
	}
	for _, a := range after.Actions {
		if a.State != persistence.Cancelled || a.Outcome != "worker_admission_expired" || !a.StartedAt.IsZero() || a.ExecutorKind != "" || s.admitted[a.ID] {
			t.Fatal("expired action remained queued or acquired an executor", a)
		}
	}
	s.dispatchActions(time.Now())
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 || s.workerRuntime.preparing != 0 || len(s.workerRuntime.pending) != 0 {
		t.Fatal("expired actions were resubmitted", err)
	}
	view := s.states.Get(s.entities[after.ID])
	if view.PulseGeneration != after.Generation || view.IsInterventionPending() || view.IsCodePending() {
		t.Fatal("owner did not project committed expiry")
	}
}

type dispatchExpiryLostReplyStore struct {
	*dispatchLostReplyStore
	skipFirst bool
	first     *persistence.WorkerExecutionIntent
	changed   bool
}

func (s *dispatchExpiryLostReplyStore) ExpireWorkerAction(ctx context.Context, in persistence.WorkerExecutionIntent) error {
	n := s.commits.Add(1)
	if s.first == nil {
		copy := in.Clone()
		s.first = &copy
	} else if !reflect.DeepEqual(*s.first, in) {
		s.changed = true
	}
	if !s.skipFirst || n != 1 {
		if err := s.Store.ExpireWorkerAction(ctx, in); err != nil {
			return err
		}
	}
	if n == 1 {
		return errors.Join(persistence.ErrCommitUnconfirmed, context.DeadlineExceeded)
	}
	return nil
}

func TestWorkerDispatchExpiryReconcilesLostReplies(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			s, catalog := workerDispatchOwner(t)
			before := queuedExpiredWorkerActions(t, s)
			s.finishWorkerDispatch()
			lost := &dispatchExpiryLostReplyStore{dispatchLostReplyStore: &dispatchLostReplyStore{
				Store: s.store, flushes: make(chan struct{}, 1), release: make(chan struct{})}, skipFirst: !committed}
			d := newWorkerDispatcher(lost, catalog)
			s.workerRuntime.dispatcher = d
			d.start()
			defer close(lost.release)
			var action persistence.Action
			for _, a := range before.Actions {
				if a.Kind == "intervention" {
					action = a
				}
			}
			if !s.dispatchWorkerAction(s.entities[before.ID], action) {
				t.Fatal("worker recovery not recognized")
			}
			select {
			case <-lost.flushes:
			case <-time.After(5 * time.Second):
				t.Fatal("uncertain expiry was not reconciled")
			}
			if committed {
				d.cancel()
				select {
				case <-d.done:
					t.Fatal("shutdown abandoned unconfirmed expiry")
				default:
				}
			}
			lost.release <- struct{}{}
			awaitWorkerPreparation(t, s)
			if committed {
				<-d.done
			}
			wantCalls := int32(2)
			if committed {
				wantCalls = 1
			}
			after, _ := s.store.Get(before.ID)
			page, err := s.store.History().Page(before.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			expired := 0
			for _, event := range page.Events {
				if event.Outcome == "worker_admission_expired" {
					expired++
				}
			}
			if lost.changed || lost.commits.Load() != wantCalls || after.Actions[action.ID].State != persistence.Cancelled || expired != 1 || len(s.workerRuntime.pending) != 0 {
				t.Fatal("lost reply changed identity, duplicated expiry or lost owner completion", lost.commits.Load(), expired)
			}
		})
	}
}

func TestWorkerDispatchOldCompletionCannotReleaseReplacement(t *testing.T) {
	s, _ := workerDispatchOwner(t)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	state, _, _ := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	record, _, _ := s.store.WorkerExecution(t.Context(), state.ID)
	if _, err := s.store.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline.Add(time.Millisecond), 100); err != nil {
		t.Fatal(err)
	}
	newToken := uuid.New()
	s.pendingCheckID[m.ID] = newToken
	s.pending[m.ID] = true
	s.receiveWorkerDispatch()
	if !s.pending[m.ID] || s.pendingCheckID[m.ID] != newToken {
		t.Fatal("old completion cleared a replacement's pending identity")
	}
}

func TestWorkerDispatchReconcilesExistingCheckWithoutSecondAdmission(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	m, _ := s.store.Get("remote")
	before, _, _ := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	// Recreate only volatile scheduling ownership. The committed store is the
	// recovery oracle; this is a controller restart, not a process-crash claim.
	s.finishWorkerDispatch()
	delete(s.pending, m.ID)
	delete(s.pendingCheckID, m.ID)
	s.initWorkerDispatch(catalog)
	s.startWorkerDispatch()
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	after, _, err := s.store.FindWorkerExecution(t.Context(), m.ID, m.CatalogUID, "")
	page, pageErr := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || pageErr != nil || before != after || len(page.Items) != 1 {
		t.Fatal("recovery allocated a duplicate check", err, pageErr)
	}
	reports := s.store.SLOView(time.Now()).Reports
	if len(reports) != 1 || reports[0].Missed != 1 || reports[0].Pending != 1 {
		t.Fatal("adopting prior work hid the newly missed slot", reports)
	}
}

type dispatchUnsubmittedStore struct {
	*persistence.Store
	first   *persistence.WorkerExecutionIntent
	changed bool
}

func (s *dispatchUnsubmittedStore) CommitWorkerExecution(ctx context.Context, in persistence.WorkerExecutionIntent) (persistence.WorkerExecutionRecord, error) {
	if s.first == nil {
		copy := in.Clone()
		s.first = &copy
		return persistence.WorkerExecutionRecord{}, errors.Join(persistence.ErrCommitUnconfirmed, context.DeadlineExceeded)
	}
	s.changed = !reflect.DeepEqual(*s.first, in)
	return s.Store.CommitWorkerExecution(ctx, in)
}

func TestWorkerDispatchRetriesOnlyOriginalPreparedIntent(t *testing.T) {
	s, catalog := workerDispatchOwner(t)
	s.finishWorkerDispatch()
	lost := &dispatchUnsubmittedStore{Store: s.store}
	s.workerRuntime.dispatcher = newWorkerDispatcher(lost, catalog)
	s.startWorkerDispatch()
	scheduleWorkerCheck(t, s, time.Now())
	awaitWorkerPreparation(t, s)
	if lost.first == nil || lost.changed {
		t.Fatal("retry regenerated identity, timestamps or ciphertext")
	}
	record, found, err := s.store.WorkerExecution(t.Context(), lost.first.ID)
	if err != nil || !found || !reflect.DeepEqual(record.Intent, *lost.first) {
		t.Fatal("original prepared intent was not retained", err)
	}
}

type blockedDispatchCatalog struct {
	*management.Catalog
	entered chan struct{}
}

func (c *blockedDispatchCatalog) WorkerExecutionTimeout(ctx context.Context, _ persistence.JobTypeReference, _ time.Duration) (time.Duration, error) {
	c.entered <- struct{}{}
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestWorkerDispatchBoundsPreparationAndJoinsWithoutResultConsumer(t *testing.T) {
	s, catalog := workerDispatchOwner(t, 20)
	s.finishWorkerDispatch()
	blocked := &blockedDispatchCatalog{Catalog: catalog, entered: make(chan struct{}, workerDispatchCapacity)}
	r := s.workerRuntime
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
			t.Fatal("preparation pool did not reach its bound")
		}
	}
	s.dispatchChecks(at)
	if r.preparing != workerDispatchCapacity || len(r.pending) != workerDispatchCapacity || r.ready.Len() != 4 || s.scheduler.ReadyLen() != 0 || len(blocked.entered) != 0 || r.bytes > workerDispatchBytes {
		t.Fatal("preparation overflow dropped work or exceeded its bounds")
	}
	r.dispatcher.cancel()
	select {
	case <-r.dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown required an ECS result consumer")
	}
	page, err := s.store.WorkerExecutions(t.Context(), "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("canceled preparation committed an assignment", err)
	}
}
