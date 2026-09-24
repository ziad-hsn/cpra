package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

type controllerContextRead struct {
	name string
	read func(context.Context) error
}

func controllerContextReads(s *Store, m Monitor, guard *CatalogGuard, cursor ControlCursor) []controllerContextRead {
	return []controllerContextRead{
		{"health", s.ControllerHealthContext},
		{"check admission", func(ctx context.Context) error {
			return s.CheckCheckAdmissionContext(ctx, m.ID, guard, m.ControlRevision, time.Now().UTC())
		}},
		{"catalog guard", func(ctx context.Context) error { return s.CheckCatalogGuardContext(ctx, m.ID, guard) }},
		{"owner observation", func(ctx context.Context) error { return s.MarkMonitorObservedContext(ctx, m.ID, *guard, 1) }},
		{"monitor", func(ctx context.Context) error { _, _, err := s.GetContext(ctx, m.ID); return err }},
		{"monitor status", func(ctx context.Context) error { _, _, err := s.MonitorStatusContext(ctx, m.ID); return err }},
		{"retained catalog", func(ctx context.Context) error {
			_, _, err := s.CatalogRetainedContext(ctx, CatalogKey{Kind: "Monitor", ID: m.ID})
			return err
		}},
		{"action", func(ctx context.Context) error { _, _, err := s.ActionContext(ctx, "missing-action"); return err }},
		{"control snapshot", func(ctx context.Context) error { _, err := s.ControlSnapshotContext(ctx); return err }},
		{"control changes", func(ctx context.Context) error { _, err := s.ControlsChangedSinceContext(ctx, cursor, 10); return err }},
	}
}

func TestControllerContextReadsHonorLockDeadlines(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "bounded-reads")
	view, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range controllerContextReads(s, m, guard, view.Cursor) {
		for _, blocked := range []string{"store", "fsm"} {
			t.Run(check.name+"/"+blocked, func(t *testing.T) {
				var release func()
				if blocked == "store" {
					s.mu.Lock()
					release = s.mu.Unlock
				} else {
					s.fsm.mu.Lock()
					release = s.fsm.mu.Unlock
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				completed := make(chan error, 1)
				go func() { completed <- check.read(ctx) }()
				select {
				case err := <-completed:
					release()
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("lock wait lost caller deadline", err)
					}
				case <-time.After(time.Second):
					release()
					<-completed
					t.Fatal("read blocked beyond caller deadline")
				}
				if check.name == "owner observation" {
					got, _ := s.Get(m.ID)
					if got.ownerObserved.generation != 0 {
						t.Fatal("timed-out observation marked uninstalled runtime")
					}
				} else if err := check.read(context.Background()); err != nil {
					t.Fatal("lock cancellation prevented a later read", err)
				}
			})
		}
	}
	if err := s.MarkMonitorObservedContext(context.Background(), m.ID, *guard, 1); err != nil {
		t.Fatal("owner mark did not recover", err)
	}
	assertObserved(t, s, m.ID, 1)
}

func TestControllerContextAdmissionCancellationSkipsProvider(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "bounded-admission")
	called := 0
	dispatch := jobs.NewDispatch(&completionRecoveryJob{execute: func() jobs.Result { called++; return jobs.Result{} }}, ecs.Entity{}, "pulse", "", 1, 0)
	dispatch.Before = func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		return s.CheckCheckAdmissionContext(bounded, m.ID, guard, m.ControlRevision, time.Now().UTC())
	}
	s.fsm.mu.Lock()
	completed := make(chan jobs.Result, 1)
	go func() { completed <- dispatch.Execute() }()
	select {
	case result := <-completed:
		s.fsm.mu.Unlock()
		if called != 0 || !result.ExecutionStart.IsZero() || !errors.Is(result.Err, context.DeadlineExceeded) {
			t.Fatal("canceled admission invoked provider", called, result)
		}
	case <-time.After(time.Second):
		s.fsm.mu.Unlock()
		<-completed
		t.Fatal("admission outlived its deadline")
	}
	if result := dispatch.Execute(); result.Err != nil || called != 1 || result.ExecutionStart.IsZero() {
		t.Fatal("healthy admission did not recover", called, result)
	}
}

func TestControllerContextReadsRejectCanceledContextAndPreserveLegacyGet(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "canceled-read")
	view, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range controllerContextReads(s, m, guard, view.Cursor) {
		if err := check.read(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s lost cancellation: %v", check.name, err)
		}
	}
	if got, _ := s.Get(m.ID); got.ownerObserved.generation != 0 {
		t.Fatal("canceled mark changed owner observation")
	}
	s.MarkUnavailable(errors.New("storage fault"))
	if _, _, err := s.GetContext(context.Background(), m.ID); err == nil {
		t.Fatal("contextual owner read ignored permanent storage fault")
	}
	if got, ok := s.Get(m.ID); !ok || !reflect.DeepEqual(got, m) {
		t.Fatal("legacy diagnostic Get no longer exposes recorded state")
	}
}

func TestControllerContextActionReadMatchesIndexedAndInitialImages(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := manualFixture(t, s)
	m = *mustResult(t, s, manualCommand(m, guard, "context-action", policyTime().Add(2*time.Second))).Monitor
	a := latestManual(t, m)
	wantView, err := s.ActionSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	want, ok := wantView.Get(a.ID)
	if !ok {
		t.Fatal("missing fixture action")
	}
	for _, indexed := range []bool{true, false} {
		if !indexed {
			s.fsm.mu.Lock()
			s.fsm.actionIndex, s.fsm.actionsByMonitor = nil, nil
			s.fsm.mu.Unlock()
		}
		got, ok, err := s.ActionContext(context.Background(), a.ID)
		if err != nil || !ok || !reflect.DeepEqual(got, want) {
			t.Fatal("point read changed action evidence", indexed, got, err)
		}
	}
}

type controllerReadCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *controllerReadCancelContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestControllerContextSnapshotCancellationDiscardsPartialIndex(t *testing.T) {
	s := openCatalogMemory(t)
	for i := range 12 {
		m, guard := managedControlMonitor(t, s, fmt.Sprintf("initial-index-%02d", i))
		controlPulse(t, s, m, guard, policyTime().Add(time.Second), "failure")
	}
	before, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := before.Page("", 500)
	if err != nil || len(want) != 12 {
		t.Fatal("invalid fixture control index", len(want), err)
	}
	s.fsm.mu.Lock()
	s.fsm.controls, s.fsm.incidents, s.fsm.incidentsByMonitor = nil, nil, nil
	s.fsm.mu.Unlock()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &controllerReadCancelContext{Context: parent, cancel: cancel, remaining: 8}
	if _, err := s.ControlSnapshotContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("initial-index rebuild ignored cancellation", err)
	}
	s.fsm.mu.RLock()
	partial := s.fsm.controls != nil || s.fsm.incidents != nil || s.fsm.incidentsByMonitor != nil
	s.fsm.mu.RUnlock()
	if partial {
		t.Fatal("canceled initialization published incomplete indexes")
	}
	after, err := s.ControlSnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := after.Page("", 500)
	if err != nil || !reflect.DeepEqual(got, want) || after.Cursor != before.Cursor {
		t.Fatal("retried initialization lost control state", got, err)
	}
}

func TestControllerContextMonitorStatusRefreshHonorsWriteLockDeadline(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "status-refresh")
	if err := s.MarkMonitorObserved(m.ID, *guard, 1); err != nil {
		t.Fatal(err)
	}
	createCatalog(t, s, catalogRecord(t, s, "Credential", "unrelated-refresh", "unrelated-uid", "unrelated-v1", "ciphertext"))
	s.fsm.mu.RLock()
	before := s.fsm.image.Monitors[m.ID].ownerObserved
	if before.catalogSequence == s.fsm.catalogSequence {
		s.fsm.mu.RUnlock()
		t.Fatal("fixture does not require exclusive refresh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	completed := make(chan error, 1)
	go func() { _, _, err := s.MonitorStatusContext(ctx, m.ID); completed <- err }()
	select {
	case err := <-completed:
		s.fsm.mu.RUnlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("status refresh did not cancel its lock upgrade", err)
		}
	case <-time.After(time.Second):
		s.fsm.mu.RUnlock()
		<-completed
		t.Fatal("status refresh blocked shutdown after its initial read")
	}
	if got, _ := s.Get(m.ID); got.ownerObserved != before {
		t.Fatal("canceled status refresh published an acknowledgement")
	}
	got, ok, err := s.MonitorStatusContext(context.Background(), m.ID)
	if err != nil || !ok || got.ObservedGeneration != 1 {
		t.Fatal("status refresh did not recover", got, err)
	}
	s.fsm.mu.RLock()
	refreshed := s.fsm.image.Monitors[m.ID].ownerObserved.catalogSequence == s.fsm.catalogSequence
	s.fsm.mu.RUnlock()
	if !refreshed {
		t.Fatal("successful refresh did not advance cached sequence")
	}
}
