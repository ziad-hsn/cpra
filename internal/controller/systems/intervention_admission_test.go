package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"cpra/internal/web/snapshot"
	"errors"
	"github.com/mlange-42/ark/ecs"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func interventionMonitor(t *testing.T, w *ecs.World, target string) ecs.Entity {
	ent := releaseMonitor(t, w, 1, 2)
	ecs.NewMap1[components.InterventionConfig](w).Add(ent, &components.InterventionConfig{Action: "webhook", MaxFailures: 1})
	storage := ecs.NewMap1[components.JobStorage](w).Get(ent)
	storage.InterventionJob = &jobs.InterventionWebhookJob{URL: target, Method: http.MethodPost, Timeout: time.Second, Entity: ent}
	return ent
}

func applyPulseResult(system *BatchPulseResultSystem, state *components.MonitorState, ent ecs.Entity, err error) {
	state.PulseGeneration++
	state.SetPulsePending(true)
	system.ProcessBatch([]jobs.Result{{Ent: ent, Generation: state.PulseGeneration, Err: err}})
}

func TestRecoveryCancelsUnadmittedIntervention(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	world := ecs.NewWorld()
	ent := interventionMonitor(t, &world, server.URL)
	state := ecs.NewMap1[components.MonitorState](&world).Get(ent)
	storage := ecs.NewMap1[components.JobStorage](&world).Get(ent)
	ready := &ReadyQueue{}
	pulse := NewBatchPulseResultSystem(&world, nil, NewPulseScheduler(), ready, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
	q, _ := queue.NewHybridQueue(queue.HybridQueueConfig{RingCapacity: 2})
	defer q.Close()
	dispatch := NewBatchInterventionSystem(&world, q, ready, 1, noopLogger{}, NewStateLogger(false))
	for i := 0; i < 2; i++ {
		if err := q.Enqueue(storage.InterventionJob.Copy()); err != nil {
			t.Fatal(err)
		}
	}
	applyPulseResult(pulse, state, ent, errors.New("target down"))
	dispatch.Update(&world)
	if !state.IsInterventionNeeded() || state.IsInterventionPending() {
		t.Fatal("setup did not leave intervention awaiting admission")
	}
	applyPulseResult(pulse, state, ent, nil)
	applyPulseResult(pulse, state, ent, nil)
	if state.Recovering || state.LastError != nil {
		t.Fatal("setup did not recover monitor")
	}
	q.DequeueBatch(2)
	dispatch.Update(&world)
	batch, _ := q.DequeueBatch(2)
	for _, job := range batch {
		result := job.Execute()
		if err := result.Error(); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("recovered target received %d stale recovery action(s), pending=%t", calls.Load(), state.IsInterventionPending())
	}
}

func TestMaintenanceSuppressesDelayedIntervention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Leave the initial ten-second maintenance window and trigger an outage.
		time.Sleep(11 * time.Second)
		world := ecs.NewWorld()
		ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
		state := ecs.NewMap1[components.MonitorState](&world).Get(ent)
		windows, err := schema.CompileMaintenance([]schema.MaintenanceWindow{{Cron: "* * * * *", Duration: "10s", Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		state.Maintenance = windows
		if schema.InMaintenance(state.Maintenance, time.Now()) {
			t.Fatal("setup inside maintenance")
		}
		ready := &ReadyQueue{}
		pulse := NewBatchPulseResultSystem(&world, nil, NewPulseScheduler(), ready, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
		q, _ := queue.NewHybridQueue(queue.HybridQueueConfig{RingCapacity: 2})
		defer q.Close()
		storage := ecs.NewMap1[components.JobStorage](&world).Get(ent)
		for i := 0; i < 2; i++ {
			if err := q.Enqueue(storage.InterventionJob.Copy()); err != nil {
				t.Fatal(err)
			}
		}
		dispatch := NewBatchInterventionSystem(&world, q, ready, 1, noopLogger{}, NewStateLogger(false))
		applyPulseResult(pulse, state, ent, errors.New("target down"))
		dispatch.Update(&world)
		time.Sleep(50 * time.Second)
		if !schema.InMaintenance(state.Maintenance, time.Now()) {
			t.Fatal("setup did not enter maintenance")
		}
		q.DequeueBatch(2)
		dispatch.Update(&world)
		if got := q.Stats().QueueDepth; got != 0 {
			t.Fatalf("admitted %d recovery action(s) after maintenance began", got)
		}
		if !state.IsInterventionNeeded() || ready.Len() != 1 {
			t.Fatal("maintenance lost the pending recovery request")
		}
		time.Sleep(10 * time.Second)
		dispatch.Update(&world)
		if got := q.Stats().QueueDepth; got != 1 || !state.IsInterventionPending() {
			t.Fatalf("unhealthy target did not resume recovery after maintenance: depth=%d", got)
		}
	})
}

func TestSnapshotReportsConsecutivePulseFailures(t *testing.T) {
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 5, 2)
	state := ecs.NewMap1[components.MonitorState](&world).Get(ent)
	pulse := NewBatchPulseResultSystem(&world, nil, NewPulseScheduler(), &ReadyQueue{}, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
	applyPulseResult(pulse, state, ent, errors.New("target down"))
	applyPulseResult(pulse, state, ent, errors.New("still down"))
	holder := snapshot.NewHolder()
	ss := NewBatchStatsSnapshotSystem(&world, noopLogger{}, holder, time.Second, 100)
	ss.Initialize(&world)
	ss.Update(&world)
	snap := holder.Get()
	if got := snap.Monitors[0].ConsecutiveFailures; got != 2 {
		t.Fatalf("snapshot reports %d consecutive failures after two failed pulses (PulseFailures=%d)", got, state.PulseFailures)
	}
	state.SetInterventionPending(true)
	intervention := NewBatchInterventionResultSystem(&world, nil, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
	intervention.ProcessBatch([]jobs.Result{{Ent: ent, Generation: state.InterventionGeneration}})
	if state.ConsecutiveFailures != 2 {
		t.Fatal("action acceptance reset failed check count without a healthy check")
	}
	applyPulseResult(pulse, state, ent, nil)
	if state.ConsecutiveFailures != 0 {
		t.Fatal("successful check did not reset consecutive failures")
	}
	applyPulseResult(pulse, state, ent, errors.New("failed again"))
	if state.ConsecutiveFailures != 1 {
		t.Fatal("new failure did not begin a new consecutive count")
	}
}
