package systems

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func releaseMonitor(t *testing.T, w *ecs.World, unhealthy, healthy int) ecs.Entity {
	t.Helper()
	mgr := entities.NewEntityManager(w)
	m := manifest.Monitor{Name: "release", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: unhealthy, HealthyThreshold: healthy, Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: map[string]manifest.CodeConfig{}}
	for _, color := range []string{"yellow", "red", "green", "cyan"} {
		m.Codes[color] = manifest.CodeConfig{Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "alerts.jsonl"}}
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

func newIncidentSystem(t *testing.T, world *ecs.World, intervention, notification queue.Queue) *DurableSystem {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	log, err := jobs.OpenNotificationLog(filepath.Join(t.TempDir(), "notifications"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	system := NewDurableSystem(world, store, cfg, fleetview.NewHolder(), noopLogger{}, &admissionQueue{full: true}, intervention, notification, nil, time.Minute, true)
	system.SetNotificationLog(log)
	if err := system.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	system.Initialize(world)
	return system
}

func applyIncidentPulse(t *testing.T, system *DurableSystem, ent ecs.Entity, result jobs.Result) persistence.Monitor {
	t.Helper()
	state := system.states.Get(ent)
	result.Ent, result.Type = ent, "pulse"
	result.MonitorID, result.Revision, result.Generation = state.MonitorID, state.Revision, state.PulseGeneration+1
	if !system.commitResults([]jobs.Result{result}) || system.lastError != nil {
		t.Fatal("observation did not commit", system.lastError)
	}
	monitor, ok := system.store.Get(state.MonitorID)
	if !ok {
		t.Fatal("committed monitor missing")
	}
	return monitor
}

func incidentActions(m persistence.Monitor, kind, color string) []persistence.Action {
	var actions []persistence.Action
	for _, action := range m.Actions {
		if action.Kind == kind && action.Color == color {
			actions = append(actions, action)
		}
	}
	return actions
}

func TestRecoveryRequiresConsecutiveSuccesses(t *testing.T) {
	for _, unhealthy := range []int{1, 5} {
		for _, attempted := range []bool{false, true} {
			t.Run(fmt.Sprintf("threshold-%d/attempted-%t", unhealthy, attempted), func(t *testing.T) {
				world := ecs.NewWorld()
				ent := releaseMonitor(t, &world, unhealthy, 2)
				notifications := &admissionQueue{}
				system := newIncidentSystem(t, &world, nil, notifications)
				failure := jobs.Result{Err: errors.New("unhealthy")}
				applyIncidentPulse(t, system, ent, failure)
				if attempted {
					system.dispatchActions(time.Now().UTC())
					if len(notifications.admitted) == 0 {
						t.Fatal("initial notification not admitted")
					}
					for _, notification := range notifications.admitted {
						result := notification.Execute()
						if result.Err != nil || !system.commitResults([]jobs.Result{result}) {
							t.Fatal("initial notification attempt did not complete", result.Err, system.lastError)
						}
					}
				}
				applyIncidentPulse(t, system, ent, jobs.Result{})
				applyIncidentPulse(t, system, ent, failure)
				m := applyIncidentPulse(t, system, ent, jobs.Result{})
				if len(incidentActions(m, "code", "green")) != 0 {
					t.Fatal("recovered without consecutive successful checks")
				}
				m = applyIncidentPulse(t, system, ent, jobs.Result{})
				expected := 0
				if attempted {
					expected = 1
				}
				if len(incidentActions(m, "code", "green")) != expected || m.Recovering {
					t.Fatal("healthy threshold or earlier-attempt eligibility was not applied", m.Recovering, m.Actions)
				}
				if system.states.Get(ent).Recovering {
					t.Fatal("controller projection did not apply recovery")
				}
			})
		}
	}
}

func TestInterventionIsSingleFlightPerIncident(t *testing.T) {
	world := ecs.NewWorld()
	ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
	interventions := &admissionQueue{}
	system := newIncidentSystem(t, &world, interventions, &admissionQueue{})
	var monitor persistence.Monitor
	for range 10 {
		monitor = applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("unhealthy")})
	}
	actions := incidentActions(monitor, "intervention", "")
	if len(actions) != 1 || actions[0].State != persistence.Queued {
		t.Fatal("repeated failures admitted overlapping interventions", actions)
	}
	system.dispatchActions(time.Now().UTC())
	if len(interventions.admitted) != 1 {
		t.Fatal("queued intervention was not dispatched exactly once")
	}
	results, err := system.store.Submit(t.Context(), []persistence.Command{{Kind: "start", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: actions[0].ID, At: time.Now().UTC()}})
	if err != nil || len(results) != 1 || !results[0].Allowed {
		t.Fatal("start was not committed", err)
	}
	for range 10 {
		monitor = applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("still unhealthy")})
		system.dispatchActions(time.Now().UTC())
	}
	actions = incidentActions(monitor, "intervention", "")
	if len(actions) != 1 || actions[0].State != persistence.Started || len(interventions.admitted) != 1 {
		t.Fatal("started intervention was duplicated")
	}
}

func TestAlertFanoutRetainsIndependentDelivery(t *testing.T) {
	world := ecs.NewWorld()
	mgr := entities.NewEntityManager(&world)
	mgr.Endpoints = map[string]manifest.Endpoint{}
	for i := range 3 {
		mgr.Endpoints[fmt.Sprintf("endpoint-%d", i)] = manifest.Endpoint{Type: "log", Config: &manifest.CodeNotificationLog{File: fmt.Sprintf("endpoint-%d.jsonl", i)}}
	}
	mgr.NotificationGroups = manifest.NotificationGroups{"oncall": {"endpoint-0", "endpoint-1", "endpoint-2"}}
	monitor := manifest.Monitor{Name: "fanout", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: 1, HealthyThreshold: 1, Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: manifest.Codes{
		"red":   {Dispatch: true, NotifyGroup: "oncall"},
		"green": {Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "recovery.jsonl"}},
	}}
	if err := mgr.CreateEntityFromMonitor(&monitor, &world); err != nil {
		t.Fatal(err)
	}
	q := ecs.NewFilter1[components.MonitorState](&world).Query()
	var ent ecs.Entity
	for q.Next() {
		ent = q.Entity()
	}
	notifications, err := queue.NewHybridQueue(queue.HybridQueueConfig{RingCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer notifications.Close()
	system := newIncidentSystem(t, &world, nil, notifications)
	m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("unhealthy")})
	seen := map[string]bool{}
	for turn := 0; turn < 5; turn++ {
		// Dispatch only at the owner's retained deadline. Manually inserting
		// readiness here would hide a lost suffix after queue saturation.
		now := time.Now().UTC()
		if due, ok := system.actionDue[ent]; ok && due.After(now) {
			time.Sleep(time.Until(due))
		}
		system.dispatchActions(time.Now().UTC())
		batch, err := notifications.DequeueBatch(20)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range batch {
			result := job.Execute()
			if seen[result.ActionID] || result.Err != nil {
				t.Fatal("duplicate or failed endpoint", result.ActionID, result.Err)
			}
			seen[result.ActionID] = true
			if !system.commitResults([]jobs.Result{result, result}) {
				t.Fatal("endpoint result did not commit", system.lastError)
			}
		}
	}
	m, _ = monitorWithActionHistory(t, system.store, m.ID)
	red := incidentActions(m, "code", "red")
	if len(red) != 3 || len(seen) != 3 {
		t.Fatal("fanout lost endpoints", len(red), len(seen))
	}
	for _, a := range red {
		if a.State != persistence.Succeeded || a.Attempt != 1 {
			t.Fatal("endpoint result was repeated or lost", a.State, a.Attempt)
		}
	}
	// A distinct recovery color must get its own delivery, even inside the
	// outage cooldown; successful outage endpoints must stay completed.
	m = applyIncidentPulse(t, system, ent, jobs.Result{})
	green := incidentActions(m, "code", "green")
	if len(green) != 1 || green[0].State != persistence.Queued {
		t.Fatal("recovery notification was suppressed")
	}
	for _, a := range incidentActions(m, "code", "red") {
		if a.State != persistence.Succeeded {
			t.Fatal("recovery altered a completed endpoint")
		}
	}
}

// rejectedNotification models a confirmed rejection, after committed start.
// Provider-specific transport classification is covered by the jobs tests.
type rejectedNotification struct {
	jobs.Job
	calls *int
}

func (j *rejectedNotification) Copy() jobs.Job { copy := *j; return &copy }
func (j *rejectedNotification) Execute() jobs.Result {
	*j.calls++
	return jobs.Result{Err: &jobs.DeliveryError{Status: 429, Retryable: true}}
}

func TestNotificationConfirmedRejectionStopsAfterThreeAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		world := ecs.NewWorld()
		ent := releaseMonitor(t, &world, 1, 1)
		calls := 0
		storage := ecs.NewMap1[components.JobStorage](&world).Get(ent)
		storage.CodeJobs = map[string][]jobs.Job{"red": {&rejectedNotification{Job: storage.CodeJobs["red"][0], calls: &calls}}}
		codes := ecs.NewMap1[components.CodeConfig](&world).Get(ent)
		codes.Configs = map[string]*components.ColorCodeConfig{"red": codes.Configs["red"]}
		notifications := &admissionQueue{}
		system := newIncidentSystem(t, &world, nil, notifications)
		monitor := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("unhealthy")})
		var first persistence.Action
		for attempt := 1; attempt <= 3; attempt++ {
			actions := incidentActions(monitor, "code", "red")
			if len(actions) != 1 || actions[0].State != persistence.Queued || actions[0].Attempt != attempt {
				t.Fatal("incorrect retry admission", actions)
			}
			action := actions[0]
			if attempt > 1 && (action.DeliveryID != first.DeliveryID || action.Deadline != first.Deadline) {
				t.Fatal("retry changed logical delivery or overall deadline")
			}
			if wait := time.Until(action.NotBefore); wait > 0 {
				time.Sleep(wait)
			}
			system.actions.EnqueueReady([]ecs.Entity{ent})
			system.dispatchActions(time.Now().UTC())
			if len(notifications.admitted) != attempt {
				t.Fatal("retry dispatched more than once", len(notifications.admitted))
			}
			result := notifications.admitted[attempt-1].Execute()
			if calls != attempt {
				t.Fatal("started handler invocation count", calls)
			}
			started, _ := system.store.Get(monitor.ID)
			if attempt == 1 {
				first = started.Actions[action.ID]
			}
			if !system.commitResults([]jobs.Result{result, result}) {
				t.Fatal("rejection did not commit", system.lastError)
			}
			monitor, _ = system.store.Get(monitor.ID)
		}
		time.Sleep(time.Minute)
		system.actions.EnqueueReady([]ecs.Entity{ent})
		system.dispatchActions(time.Now().UTC())
		monitor, _ = monitorWithActionHistory(t, system.store, monitor.ID)
		actions := incidentActions(monitor, "code", "red")
		if len(actions) != 3 || calls != 3 || len(notifications.admitted) != 3 {
			t.Fatal("exhausted notification restarted", actions, calls)
		}
		attempts := map[int]bool{}
		for _, action := range actions {
			if action.State != persistence.Failed || action.Attempt < 1 || action.Attempt > 3 || attempts[action.Attempt] || action.DeliveryID != first.DeliveryID || action.Deadline != first.Deadline {
				t.Fatal("confirmed rejection history lost its attempt or delivery identity", action)
			}
			attempts[action.Attempt] = true
		}
	})
}
