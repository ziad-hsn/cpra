package systems

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func interventionMonitor(t *testing.T, w *ecs.World, target string) ecs.Entity {
	ent := releaseMonitor(t, w, 1, 2)
	ecs.NewMap1[components.InterventionConfig](w).Add(ent, &components.InterventionConfig{Action: "webhook", MaxFailures: 1, Target: &manifest.InterventionTargetWebhook{URL: target, Method: http.MethodPost, Timeout: time.Second}})
	storage := ecs.NewMap1[components.JobStorage](w).Get(ent)
	storage.InterventionJob = &jobs.InterventionWebhookJob{URL: target, Method: http.MethodPost, Timeout: time.Second, Entity: ent}
	return ent
}

func TestRecoveryCancelsUnadmittedIntervention(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	world := ecs.NewWorld()
	ent := interventionMonitor(t, &world, server.URL)
	queue := &admissionQueue{rejected: true}
	system := newIncidentSystem(t, &world, queue, &admissionQueue{})
	m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target down")})
	system.dispatchActions(time.Now())
	actions := incidentActions(m, "intervention", "")
	if len(actions) != 1 || actions[0].State != persistence.Queued || len(queue.admitted) != 0 {
		t.Fatal("fixture did not retain unadmitted recovery")
	}
	applyIncidentPulse(t, system, ent, jobs.Result{})
	m = applyIncidentPulse(t, system, ent, jobs.Result{})
	if m.Recovering || m.Actions[actions[0].ID].State != persistence.Cancelled {
		t.Fatal("confirmed health did not cancel unsent recovery")
	}
	queue.rejected = false
	system.actions.EnqueueReady([]ecs.Entity{ent})
	system.dispatchActions(time.Now())
	for _, job := range queue.admitted {
		job.Execute()
	}
	if len(queue.admitted) != 0 || calls.Load() != 0 {
		t.Fatal("recovered target received a stale intervention")
	}
}

func TestRestartGatedInterventionWaitsOutsideScheduler(t *testing.T) {
	world := ecs.NewWorld()
	ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
	queue := &admissionQueue{}
	system := newIncidentSystem(t, &world, queue, &admissionQueue{})
	m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target down")})
	original := incidentActions(m, "intervention", "")[0]
	restarted := time.Now().UTC()
	results, err := system.store.Submit(t.Context(), []persistence.Command{{Kind: "recover", At: restarted}})
	if err != nil || len(results) == 0 || results[0].Err != nil {
		t.Fatal("restart recovery failed", err, results)
	}
	for n := 0; n < 10; n++ {
		system.dispatchActions(time.Now().Add(time.Second))
		if system.actions.Len() != 0 || len(queue.admitted) != 0 {
			t.Fatal("restart-gated intervention repeatedly scheduled or dispatched")
		}
	}
	m = applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("still down"), ExecutionStart: restarted.Add(time.Millisecond)})
	if !m.RecoveryCheckAfter.IsZero() || m.Actions[original.ID].State != persistence.Queued {
		t.Fatal("fresh observation did not retain and release the original intent")
	}
	system.dispatchActions(time.Now().Add(2 * time.Second))
	if len(queue.admitted) != 1 {
		t.Fatal("fresh failed observation did not schedule the original intervention")
	}
	if dispatch, ok := queue.admitted[0].(*jobs.Dispatch); !ok || dispatch.ActionID != original.ID {
		t.Fatal("fresh observation created a replacement intervention")
	}
}

func TestMaintenanceSuppressesDelayedIntervention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(11 * time.Second)
		world := ecs.NewWorld()
		ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
		// This test crosses a minute boundary to exercise maintenance, within
		// an explicitly longer original intervention budget.
		ecs.NewMap1[components.InterventionConfig](&world).Get(ent).Target.(*manifest.InterventionTargetWebhook).Timeout = 2 * time.Minute
		state := ecs.NewMap1[components.MonitorState](&world).Get(ent)
		windows, err := manifest.CompileMaintenance([]manifest.MaintenanceWindow{{Cron: "* * * * *", Duration: "10s", Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		state.Maintenance = windows
		if manifest.InMaintenance(windows, time.Now()) {
			t.Fatal("fixture began inside maintenance")
		}
		queue := &admissionQueue{rejected: true}
		system := newIncidentSystem(t, &world, queue, &admissionQueue{})
		m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target down")})
		system.dispatchActions(time.Now())
		time.Sleep(50 * time.Second)
		if !manifest.InMaintenance(windows, time.Now()) {
			t.Fatal("fixture did not enter maintenance")
		}
		queue.rejected = false
		system.dispatchActions(time.Now())
		if len(queue.admitted) != 0 {
			t.Fatal("maintenance admitted an intervention")
		}
		current, _ := system.store.Get(m.ID)
		actions := incidentActions(current, "intervention", "")
		if len(actions) != 1 || actions[0].State != persistence.Queued {
			t.Fatal("maintenance discarded queued recovery")
		}
		time.Sleep(10 * time.Second)
		system.dispatchActions(time.Now())
		if len(queue.admitted) != 1 {
			t.Fatal("still-unhealthy target did not resume recovery after maintenance")
		}
	})
}

func TestProjectionReportsConsecutivePulseFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	world := ecs.NewWorld()
	ent := interventionMonitor(t, &world, server.URL)
	queue := &admissionQueue{}
	system := newIncidentSystem(t, &world, queue, &admissionQueue{})
	applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("down")})
	m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("still down")})
	row, ok := system.index.Get(ent.ID())
	if !ok || row.ConsecutiveFailures != 2 {
		t.Fatal("projection lost consecutive failed checks")
	}
	system.dispatchActions(time.Now())
	if len(queue.admitted) != 1 {
		t.Fatal("recovery was not dispatched")
	}
	result := queue.admitted[0].Execute()
	if result.Err != nil || !system.commitResults([]jobs.Result{result}) {
		t.Fatal("recovery result failed", result.Err, system.lastError)
	}
	m, _ = system.store.Get(m.ID)
	row, _ = system.index.Get(ent.ID())
	if m.ConsecutiveFailures != 2 || row.ConsecutiveFailures != 2 || row.Status != "verifying" {
		t.Fatal("intervention success reset failures or claimed restored health")
	}
	m = applyIncidentPulse(t, system, ent, jobs.Result{})
	if m.ConsecutiveFailures != 0 {
		t.Fatal("healthy observation did not reset failures")
	}
	m = applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("failed again")})
	if m.ConsecutiveFailures != 1 {
		t.Fatal("new failure did not start a fresh consecutive count")
	}
}

func TestExpiredInterventionNeverInvokesProvider(t *testing.T) {
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer receiver.Close()
	synctest.Test(t, func(t *testing.T) {
		world := ecs.NewWorld()
		ent := interventionMonitor(t, &world, receiver.URL)
		queue := &admissionQueue{rejected: true}
		system := newIncidentSystem(t, &world, queue, &admissionQueue{})
		monitor := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("down")})
		original := incidentActions(monitor, "intervention", "")[0]
		system.dispatchActions(time.Now())
		time.Sleep(time.Until(original.Deadline) + time.Second)
		queue.rejected = false
		system.dispatchActions(time.Now())
		for _, job := range queue.admitted {
			result := job.Execute()
			if !system.commitResults([]jobs.Result{result}) {
				t.Fatal("expired result did not reconcile", system.lastError)
			}
		}
		current, _ := system.store.Get(monitor.ID)
		if calls.Load() != 0 || current.Actions[original.ID].State != persistence.Cancelled {
			t.Fatal("expired queue did not cancel without a provider invocation")
		}
	})
}
