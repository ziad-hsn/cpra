package systems

import (
	"cpra/internal/alerts"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"errors"
	"github.com/mlange-42/ark/ecs"
	"testing"
	"time"
)

func releaseMonitor(t *testing.T, w *ecs.World, unhealthy, healthy int) ecs.Entity {
	t.Helper()
	mgr := entities.NewEntityManager(w)
	m := schema.Monitor{Name: "release", Enabled: true, Pulse: schema.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: unhealthy, HealthyThreshold: healthy, Config: &schema.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: map[string]schema.CodeConfig{}}
	for _, color := range []string{"yellow", "red", "green", "cyan"} {
		m.Codes[color] = schema.CodeConfig{Dispatch: true, Notify: "log", Config: &schema.CodeNotificationLog{File: t.TempDir() + "/alerts.jsonl"}}
	}
	if err := mgr.CreateEntityFromMonitor(&m, w); err != nil {
		t.Fatal(err)
	}
	q := ecs.NewFilter1[components.MonitorState](w).Query()
	var ent ecs.Entity
	for q.Next() {
		ent = q.Entity()
	}
	return ent
}
func TestRecoveryRequiresConsecutiveSuccesses(t *testing.T) {
	for _, unhealthy := range []int{1, 5} {
		t.Run(string(rune('0'+unhealthy)), func(t *testing.T) {
			w := ecs.NewWorld()
			ent := releaseMonitor(t, &w, unhealthy, 2)
			state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
			system := NewBatchPulseResultSystem(&w, nil, NewPulseScheduler(), &ReadyQueue{}, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
			apply := func(err error) { state.SetPulsePending(true); system.ProcessBatch([]jobs.Result{{Ent: ent, Err: err}}) }
			hasGreen := func() bool {
				for _, r := range state.PendingAlerts {
					if r.Color == "green" {
						return true
					}
				}
				return false
			}
			apply(errors.New("unhealthy"))
			apply(nil)
			apply(errors.New("failed again"))
			apply(nil)
			if hasGreen() {
				t.Fatal("recovered without consecutive healthy checks")
			}
			apply(nil)
			if !hasGreen() || state.Recovering {
				t.Fatal("consecutive healthy checks did not recover")
			}
		})
	}
}
func TestInterventionIsSingleFlightPerIncident(t *testing.T) {
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 2)
	ecs.NewMap1[components.InterventionConfig](&w).Add(ent, &components.InterventionConfig{Action: "webhook", MaxFailures: 1})
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	ready := &ReadyQueue{}
	system := NewBatchPulseResultSystem(&w, nil, NewPulseScheduler(), ready, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
	for i := 0; i < 10; i++ {
		state.SetPulsePending(true)
		system.ProcessBatch([]jobs.Result{{Ent: ent, Err: errors.New("unhealthy")}})
	}
	if ready.Len() != 1 {
		t.Fatalf("scheduled %d overlapping interventions", ready.Len())
	}
	state.SetInterventionNeeded(false)
	state.SetInterventionPending(true)
	for i := 0; i < 10; i++ {
		state.SetPulsePending(true)
		system.ProcessBatch([]jobs.Result{{Ent: ent, Err: errors.New("unhealthy")}})
	}
	if ready.Len() != 1 {
		t.Fatal("scheduled while intervention was pending")
	}
}
func TestAlertFanoutAndColorsRetainIndependentDelivery(t *testing.T) {
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	storage := ecs.NewMap1[components.JobStorage](&w).Get(ent)
	storage.CodeJobs["red"] = []jobs.Job{storage.CodeJobs["red"][0], storage.CodeJobs["red"][0], storage.CodeJobs["red"][0]}
	q, err := queue.NewHybridQueue(queue.HybridQueueConfig{RingCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	sched := NewCodeScheduler()
	mgr := alerts.NewManager(&w, alerts.NewDefaultPolicy(&w, true), time.Minute)
	cfg := ecs.NewMap1[components.CodeConfig](&w).Get(ent)
	dispatch := NewBatchCodeSystem(&w, q, sched, 20, noopLogger{}, NewStateLogger(false), mgr)
	results := NewBatchCodeResultSystem(&w, nil, sched, noopLogger{}, NewStateLogger(false), mgr)
	requestCode(ent, state, "red", cfg, sched, mgr)
	requestCode(ent, state, "green", cfg, sched, mgr)
	seen := map[string]map[int]bool{}
	var last jobs.Result
	for tick := 0; tick < 5; tick++ {
		dispatch.Update(&w)
		batch, _ := q.DequeueBatch(20)
		for _, job := range batch {
			d := job.(*jobs.Dispatch)
			if seen[d.Color] == nil {
				seen[d.Color] = map[int]bool{}
			}
			if seen[d.Color][d.Endpoint] {
				t.Fatalf("duplicate %s endpoint %d after partial admission", d.Color, d.Endpoint)
			}
			seen[d.Color][d.Endpoint] = true
			var outcome error
			if d.Color == "green" {
				outcome = errors.New("permanent rejection")
			}
			last = jobs.Result{Ent: ent, Color: d.Color, Endpoint: d.Endpoint, Generation: d.Generation, Err: outcome}
			results.ProcessBatch([]jobs.Result{last, last}) // duplicate delivery must be ignored
		}
	}
	if len(seen["red"]) != 3 || len(seen["green"]) != 1 {
		t.Fatalf("fanout lost endpoints: %v", seen)
	}
	if state.IsCodePending() {
		t.Fatal("completed deliveries left pending state")
	}
	status := ecs.NewMap1[components.CodeStatus](&w).Get(ent)
	if status.Status["red"].LastStatus != "success" || status.Status["green"].LastStatus != "failed" {
		t.Fatalf("color results mixed: %+v %+v", status.Status["red"], status.Status["green"])
	}
	results.ProcessBatch([]jobs.Result{last})
	if status.Status["green"].ConsecutiveFailures != 1 {
		t.Fatal("late duplicate applied twice")
	}
}
func TestAlertFailureRetriesAreBounded(t *testing.T) {
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	sched := NewCodeScheduler()
	results := NewBatchCodeResultSystem(&w, nil, sched, noopLogger{}, NewStateLogger(false), nil)
	for attempt := 1; attempt <= maxAlertAttempts; attempt++ {
		state.Deliveries = map[string]*components.CodeDelivery{"red": {Generation: uint64(attempt), Next: 1, Completed: make([]bool, 1), Attempts: attempt, Retryable: true}}
		results.ProcessBatch([]jobs.Result{{Ent: ent, Color: "red", Generation: uint64(attempt), Err: &jobs.DeliveryError{Status: 503, Retryable: true}}})
		if attempt < maxAlertAttempts && len(state.PendingAlerts) != 1 {
			t.Fatal("missing retry")
		}
		if attempt == maxAlertAttempts && len(state.PendingAlerts) != 0 {
			t.Fatal("unbounded retries")
		}
		state.PendingAlerts = nil
	}
}
