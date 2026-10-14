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
	ecs.NewMap1[components.InterventionConfig](w).Add(ent, &components.InterventionConfig{Action: "webhook", MaxFailures: 1})
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

func TestMaintenanceSuppressesDelayedIntervention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(11 * time.Second)
		world := ecs.NewWorld()
		ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
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
