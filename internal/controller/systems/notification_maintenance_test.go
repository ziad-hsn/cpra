package systems

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestNotificationMaintenanceBetweenChecksCommitsCancellation(t *testing.T) {
	for _, scenario := range []struct {
		name                     string
		waiting, missed, restart bool
		batchSize                int
	}{
		{name: "before_queue_admission"}, {name: "waiting_in_worker_queue", waiting: true},
		{name: "missed_window", missed: true}, {name: "waiting_worker_missed_window", waiting: true, missed: true},
		{name: "restart_across_window", missed: true, restart: true},
		{name: "bounded_two_deliveries", batchSize: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testNotificationMaintenanceController(t, scenario.waiting, scenario.missed, scenario.restart, scenario.batchSize)
		})
	}
}

type oneMaintenanceStart struct{ at time.Time }

func (s oneMaintenanceStart) Next(after time.Time) time.Time {
	if after.Before(s.at) {
		return s.at
	}
	return time.Time{}
}

func testNotificationMaintenanceController(t *testing.T, waiting, missed, restart bool, batchSize int) {
	t.Helper()
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer receiver.Close()
	cfg := runtimeconfig.Default()
	if batchSize > 0 {
		cfg.Storage.BatchSize = batchSize
	}
	cfg.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	// A next-minute wall boundary can pass during Load, placing start-1s
	// behind the timing wheel before the test asks to enqueue the delivery.
	// These scenarios drive the owner at explicit times; use a controlled
	// one-shot boundary safely beyond initialization instead of a real cron.
	start := time.Now().UTC().Add(time.Minute)
	windows := []manifest.CompiledWindow{{Schedule: oneMaintenanceStart{start}, Duration: 5 * time.Second}}
	if missed {
		// Queue execution uses real current time after this completed window.
		start = time.Now().UTC().Add(-10 * time.Second)
		windows = []manifest.CompiledWindow{{Schedule: oneMaintenanceStart{start}, Duration: 5 * time.Second}}
	}
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	state.Maintenance = windows
	ecs.NewMap1[components.CodeConfig](&w).Get(ent).Configs = map[string]*components.ColorCodeConfig{"red": {Dispatch: true, Notify: "webhook"}}
	ecs.NewMap1[components.JobStorage](&w).Get(ent).CodeJobs = map[string][]jobs.Job{"red": {&jobs.CodeWebhookJob{URL: receiver.URL, Color: "red"}}}
	if batchSize == 1 {
		ecs.NewMap1[components.CodeConfig](&w).Get(ent).Configs["yellow"] = &components.ColorCodeConfig{Dispatch: true, Notify: "webhook"}
		ecs.NewMap1[components.JobStorage](&w).Get(ent).CodeJobs["yellow"] = []jobs.Job{&jobs.CodeWebhookJob{URL: receiver.URL, Color: "yellow"}}
	}
	ecs.NewMap1[components.PulseConfig](&w).Get(ent).Interval = time.Hour
	code := &admissionQueue{}
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, code, nil, time.Minute, false)
	if err := s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	state = s.states.Get(ent)
	id, revision := state.MonitorID, state.Revision
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: id, Revision: revision, Generation: 1, Outcome: "failure", At: start.Add(-time.Second)}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("failed observation", err, results)
	}
	m := *results[0].Monitor
	var actionID string
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == "red" && a.State == persistence.Queued {
			actionID = a.ID
		}
	}
	if actionID == "" {
		t.Fatal("missing queued outage notification")
	}
	s.project(ent, m)
	s.scheduleActions(ent, m)
	if waiting {
		if missed {
			s.actions.EnqueueReady([]ecs.Entity{ent})
		} // Past fixture time precedes the timing wheel's real creation time.
		s.dispatchActions(start.Add(-time.Second))
		if len(code.admitted) != 1 {
			t.Fatal("notification did not enter the worker queue")
		}
		if due := s.actionDue[ent]; !missed && (due.IsZero() || due.After(start)) {
			t.Fatal("queued worker notification has no maintenance cancellation wakeup")
		}
	}
	// No health observation occurs inside or after the five-second window.
	if restart {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = persistence.Open(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		s = NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, code, nil, time.Minute, false)
		if err := s.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
		s.Initialize(&w)
	}
	if missed {
		if waiting {
			for _, queued := range code.admitted {
				queued.Execute()
			}
		} else {
			s.dispatchActions(time.Now().UTC())
		}
	} else {
		s.dispatchActions(start.Add(time.Second))
	}
	if batchSize == 1 {
		if s.lastError != nil || !store.Status().Ready || s.stopping.Load() {
			t.Fatal("valid cancellation batch permanently stopped usable storage", s.lastError)
		}
		for turn := 2; turn <= 3; turn++ {
			s.dispatchActions(start.Add(time.Duration(turn) * time.Second))
		}
	}
	m, _ = store.Get(id)
	if m.Actions[actionID].State != persistence.Cancelled || m.Actions[actionID].Outcome != "maintenance" {
		t.Fatal("the actual controller deferred a notification covered by maintenance")
	}
	for _, a := range m.Actions {
		if a.Kind == "code" && a.State != persistence.Cancelled {
			t.Fatal("not every covered delivery was cancelled")
		}
	}
	s.dispatchActions(start.Add(10 * time.Second))
	if !waiting && len(code.admitted) != 0 || waiting && len(code.admitted) != 1 {
		t.Fatal("maintenance released accumulated provider work")
	}
	for _, queued := range code.admitted {
		queued.Execute()
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled notification invoked its provider")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	m, ok := restarted.Get(id)
	if !ok || m.Generation != 1 || m.Actions[actionID].State != persistence.Cancelled {
		t.Fatal("restart lost maintenance cancellation")
	}
	for _, a := range m.Actions {
		if a.Kind == "code" && a.State != persistence.Cancelled {
			t.Fatal("reopen lost a bounded cancellation prefix")
		}
	}
}
