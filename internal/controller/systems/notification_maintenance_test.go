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
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_queue_admission", true: "waiting_in_worker_queue"}[waiting], func(t *testing.T) {
			testNotificationMaintenanceController(t, waiting)
		})
	}
}

func testNotificationMaintenanceController(t *testing.T, waiting bool) {
	t.Helper()
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer receiver.Close()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	start := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	windows, err := manifest.CompileMaintenance([]manifest.MaintenanceWindow{{Cron: "* * * * *", Duration: "5s"}})
	if err != nil {
		t.Fatal(err)
	}
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	state.Maintenance = windows
	ecs.NewMap1[components.CodeConfig](&w).Get(ent).Configs = map[string]*components.ColorCodeConfig{"red": {Dispatch: true, Notify: "webhook"}}
	ecs.NewMap1[components.JobStorage](&w).Get(ent).CodeJobs = map[string][]jobs.Job{"red": {&jobs.CodeWebhookJob{URL: receiver.URL, Color: "red"}}}
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
		s.dispatchActions(start.Add(-time.Second))
		if len(code.admitted) != 1 {
			t.Fatal("notification did not enter the worker queue")
		}
		if due := s.actionDue[ent]; due.IsZero() || due.After(start) {
			t.Fatal("queued worker notification has no maintenance cancellation wakeup")
		}
	}
	// No health observation occurs inside or after the five-second window.
	s.dispatchActions(start.Add(time.Second))
	m, _ = store.Get(id)
	if m.Actions[actionID].State != persistence.Cancelled || m.Actions[actionID].Outcome != "maintenance" {
		t.Fatal("the actual controller deferred a notification covered by maintenance")
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
}
