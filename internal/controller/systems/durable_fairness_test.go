package systems

import (
	"context"
	"errors"
	"math"
	"testing"
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

type rejectFirstDestinationQueue struct {
	admissionQueue
	flow     uint64
	attempts []string
}

func (q *rejectFirstDestinationQueue) Enqueue(job jobs.Job) error {
	d := job.(*jobs.Dispatch)
	if d.FlowKey == q.flow {
		q.attempts = append(q.attempts, d.ActionID)
		if len(q.attempts) == 1 {
			return queue.ErrQueueFull
		}
	}
	return q.admissionQueue.Enqueue(job)
}

func TestActionDestinationRejectionPreservesOrderAndUnrelatedProgress(t *testing.T) {
	w := ecs.NewWorld()
	mgr := entities.NewEntityManager(&w)
	mgr.Endpoints = map[string]manifest.Endpoint{
		"first":  {Type: "log", Config: &manifest.CodeNotificationLog{File: "shared.jsonl"}},
		"second": {Type: "log", Config: &manifest.CodeNotificationLog{File: "shared.jsonl"}},
		"other":  {Type: "log", Config: &manifest.CodeNotificationLog{File: "other.jsonl"}},
	}
	mgr.NotificationGroups = manifest.NotificationGroups{"all": {"first", "second", "other"}}
	m := manifest.Monitor{Name: "ordered", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second,
		UnhealthyThreshold: 1, HealthyThreshold: 1, Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: manifest.Codes{"red": {Dispatch: true, NotifyGroup: "all"}}}
	if err := mgr.CreateEntityFromMonitor(&m, &w); err != nil {
		t.Fatal(err)
	}
	query := ecs.NewFilter1[components.MonitorState](&w).Query()
	var ent ecs.Entity
	for query.Next() {
		ent = query.Entity()
	}
	q := &rejectFirstDestinationQueue{}
	s := newIncidentSystem(t, &w, nil, q)
	flows := s.jobs.Get(ent).CodeFlows["red"]
	if len(flows) != 3 || flows[0] != flows[1] || flows[0] == flows[2] {
		t.Fatal("test requires two shared and one independent destination", flows)
	}
	q.flow = flows[0].Key
	observed := applyIncidentPulse(t, s, ent, jobs.Result{Err: errors.New("down")})
	s.dispatchActions(time.Now().UTC())
	if s.lastError != nil || len(q.attempts) != 1 || len(q.admitted) != 1 || q.admitted[0].(*jobs.Dispatch).FlowKey == q.flow {
		t.Fatal("later shared delivery overtook rejection or blocked unrelated destination", s.lastError, q.attempts, len(q.admitted))
	}
	// At the next scheduled turn both shared deliveries enter in original order.
	next := time.Now().UTC().Add(time.Second)
	s.scheduleActionAt(ent, next)
	s.dispatchActions(next.Add(time.Second))
	if len(q.attempts) != 3 || q.attempts[0] != q.attempts[1] || len(q.admitted) != 3 {
		t.Fatal("retry lost original order", q.attempts)
	}
	for _, a := range observed.Actions {
		stored, ok := s.store.Get(observed.ID)
		if !ok || stored.Actions[a.ID].State != persistence.Queued {
			t.Fatal("admission invoked provider/start guard")
		}
	}
}

func TestConfiguredDemandRemovalBoundsRounding(t *testing.T) {
	w := ecs.NewWorld()
	entities := make([]ecs.Entity, 11)
	for i := range entities {
		entities[i] = releaseMonitor(t, &w, 1, 1)
	}
	s := NewDurableSystem(&w, nil, runtimeconfig.Default(), &fleetview.Holder{}, noopLogger{}, nil, nil, nil, nil, time.Minute, false)
	s.checks = make([]checkSchedule, int(entities[len(entities)-1].ID())+1)
	for i, e := range entities {
		cfg := s.configs.Get(e)
		cfg.Type = "http"
		cfg.Timeout = time.Second
		cfg.Interval = 10 * time.Second
		if i == 0 {
			cfg.Timeout = cfg.Interval
		}
		s.setPulseDemand(e, cfg.Interval)
	}
	q, err := queue.NewFairQueue(64, "http")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities[1:] {
		s.resetCheckSchedule(e)
		_, demand := s.PulseDriverDemand(0)
		if err := q.SetDemand(demand); err != nil {
			t.Fatal("valid removal failed capacity admission", err)
		}
	}
	if d := s.pulseDemand["http"]; d.Monitors != 1 || math.Abs(d.Occupancy-1) > 1e-9 {
		t.Fatal("surviving configured reservation lost", d)
	}
}

func TestConfiguredPulseDemandTracksChangedAndPausedChecks(t *testing.T) {
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	s := NewDurableSystem(&w, nil, runtimeconfig.Default(), &fleetview.Holder{}, noopLogger{}, nil, nil, nil, nil, time.Minute, false)
	s.checks = make([]checkSchedule, int(ent.ID())+1)
	cfg := s.configs.Get(ent)
	cfg.Interval, cfg.Timeout = 10*time.Second, 5*time.Second
	s.setPulseDemand(ent, cfg.Interval)
	assertDemand := func(want float64) {
		t.Helper()
		sum := 0.0
		for _, demand := range s.pulseDemand {
			sum += demand.Occupancy
		}
		if math.Abs(sum-want) > 1e-9 {
			t.Fatalf("configured occupancy=%g want=%g", sum, want)
		}
	}
	assertDemand(.5)
	version := s.pulseDemandVersion
	s.setPulseDemand(ent, cfg.Interval)
	assertDemand(.5)
	if version != s.pulseDemandVersion {
		t.Fatal("unchanged projection republished demand")
	}
	// Replacement invalidates observations and releases the previous demand.
	s.resetCheckSchedule(ent)
	cfg.Timeout = 20 * time.Second
	s.setPulseDemand(ent, cfg.Interval)
	assertDemand(1)
	s.resetCheckSchedule(ent) // Disable/snooze/removal releases demand.
	assertDemand(0)
	if s.PulseArrivalRate() != 0 {
		t.Fatal("inactive check retains arrival demand")
	}
	s.setPulseDemand(ent, cfg.Interval) // Re-enable restores the configured reservation.
	assertDemand(1)
}

func TestDurableResultDrainDoesNotStarveActionPipelines(t *testing.T) {
	pulse := make(chan []jobs.Result, 3)
	intervention := make(chan []jobs.Result, 1)
	code := make(chan []jobs.Result, 1)
	for n := 0; n < 3; n++ {
		pulse <- []jobs.Result{{Type: "pulse"}}
	}
	intervention <- []jobs.Result{{Type: "intervention"}}
	code <- []jobs.Result{{Type: "code"}}
	cfg := runtimeconfig.Default()
	cfg.Storage.BatchSize = 1
	s := DurableSystem{config: cfg, entities: map[string]ecs.Entity{}, results: []<-chan []jobs.Result{pulse, intervention, code}}
	for n := 0; n < 3; n++ {
		s.drain()
	}
	if len(intervention) != 0 || len(code) != 0 || len(pulse) != 2 {
		t.Fatal("continuous check results starved external-action results")
	}
}

func TestDurableFinalizeCommitsEveryBufferedResultAfterWorkersStop(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	cfg.Storage.BatchSize = 1
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	// Initialization adds the control projection, which can move an Ark table.
	// Retain identity values, never a component pointer across structural changes.
	state := *ecs.NewMap1[components.MonitorState](&w).Get(ent)
	pulse := make(chan []jobs.Result, 40)
	intervention := make(chan []jobs.Result)
	code := make(chan []jobs.Result)
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, nil, nil, nil, []<-chan []jobs.Result{pulse, intervention, code}, time.Minute, false)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	s.StopAdmission()
	now := time.Now()
	for generation := uint64(1); generation <= 40; generation++ {
		store.SLO().Expect("http", now)
		pulse <- []jobs.Result{{Type: "pulse", Driver: "http", Scheduled: now, ExecutionStart: now, ExecutionEnd: now, MonitorID: state.MonitorID, Revision: state.Revision, Generation: generation}}
	}
	close(pulse)
	close(intervention)
	close(code)
	s.Update(&w)
	if s.lastError != nil {
		t.Fatalf("first shutdown drain failed: %v", s.lastError)
	}
	s.Finalize(&w)
	m, ok := store.Get(state.MonitorID)
	if !ok || m.Generation != 40 || m.SuccessfulChecks != 40 {
		t.Fatalf("final buffered outcomes were lost: %+v", m)
	}
}
