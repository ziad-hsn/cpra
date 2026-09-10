package systems

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/durable"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"cpra/internal/runtimeconfig"
	"cpra/internal/web/snapshot"
	"github.com/mlange-42/ark/ecs"
)

// DurableSystem owns the live projection and all scheduling. The Raft goroutine
// sees only copied commands. Workers see private jobs and immutable identities.
type checkSchedule struct{ next, ready int64 }

type DurableSystem struct {
	checks         []checkSchedule
	store          *durable.Store
	world          *ecs.World
	states         *ecs.Map1[components.MonitorState]
	configs        *ecs.Map1[components.PulseConfig]
	jobs           *ecs.Map1[components.JobStorage]
	codes          *ecs.Map1[components.CodeConfig]
	disabled       *ecs.Map1[components.Disabled]
	interventions  *ecs.Map1[components.InterventionConfig]
	scheduler      *PulseScheduler
	actions        *PulseScheduler
	actionDue      map[ecs.Entity]time.Time
	queues         map[string]queue.Queue
	results        []<-chan []jobs.Result
	resultCursor   int
	processed      int
	entities       map[string]ecs.Entity
	admitted       map[string]bool
	pending        map[string]bool
	holder         *snapshot.Holder
	index          *snapshot.Index
	config         runtimeconfig.Config
	cooldown       time.Duration
	recoveryBypass bool
	stopping       bool
	lastSLO        time.Time
	lastError      error
	logger         Logger
}

func NewDurableSystem(w *ecs.World, store *durable.Store, cfg runtimeconfig.Config, holder *snapshot.Holder, logger Logger, pq, iq, cq queue.Queue, channels []<-chan []jobs.Result, cooldown time.Duration, bypass bool) *DurableSystem {
	return &DurableSystem{actionDue: map[ecs.Entity]time.Time{}, store: store, world: w, states: ecs.NewMap1[components.MonitorState](w), configs: ecs.NewMap1[components.PulseConfig](w), jobs: ecs.NewMap1[components.JobStorage](w), codes: ecs.NewMap1[components.CodeConfig](w), disabled: ecs.NewMap1[components.Disabled](w), interventions: ecs.NewMap1[components.InterventionConfig](w), scheduler: NewPulseScheduler(), actions: NewPulseScheduler(), queues: map[string]queue.Queue{"pulse": pq, "intervention": iq, "code": cq}, results: channels, entities: map[string]ecs.Entity{}, admitted: map[string]bool{}, pending: map[string]bool{}, holder: holder, config: cfg, cooldown: cooldown, recoveryBypass: bypass, logger: logger}
}

// Load validates the whole loaded identity set before committing configuration.
// It runs before workers and the owner loop start.
func (s *DurableSystem) Load(ctx context.Context) error {
	f := ecs.NewFilter1[components.MonitorState](s.world)
	q := f.Query()
	for q.Next() {
		m := q.Get()
		if _, ok := s.entities[m.MonitorID]; ok {
			q.Close()
			return fmt.Errorf("duplicate effective monitor id %q", m.MonitorID)
		}
		s.entities[m.MonitorID] = q.Entity()
	}
	q = f.Query()
	commands := make([]durable.Command, 0, s.config.Storage.BatchSize)
	flush := func() error {
		if len(commands) == 0 {
			return nil
		}
		_, err := s.store.Submit(ctx, commands)
		commands = commands[:0]
		return err
	}
	for q.Next() {
		ent := q.Entity()
		state := q.Get()
		cfg := s.configs.Get(ent)
		js := s.jobs.Get(ent)
		policy := durable.Policy{Interval: cfg.Interval, Unhealthy: cfg.UnhealthyThreshold, Healthy: cfg.HealthyThreshold, Enabled: s.disabled.Get(ent) == nil, Intervention: s.interventions.Get(ent) != nil, Cooldown: s.cooldown, RecoveryBypass: s.recoveryBypass, Endpoints: map[string]int{}}
		if policy.Healthy <= 0 {
			policy.Healthy = 2
		}
		if cc := s.codes.Get(ent); cc != nil {
			for color, c := range cc.Configs {
				if c.Dispatch {
					policy.Endpoints[color] = len(js.CodeJobs[color])
				}
			}
		}
		m := durable.Monitor{ID: state.MonitorID, Revision: state.Revision, Name: state.Name, Policy: policy}
		commands = append(commands, durable.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()})
		if len(commands) == cap(commands) {
			if err := flush(); err != nil {
				q.Close()
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	present := make(map[string]bool, len(s.entities))
	for id := range s.entities {
		present[id] = true
	}
	return s.store.Reconcile(ctx, present, time.Now().UTC())
}

func (s *DurableSystem) Initialize(*ecs.World) {
	now := time.Now()
	rows := make([]snapshot.MonitorSummary, 0, len(s.entities))
	var maximum uint32
	for _, ent := range s.entities {
		maximum = max(maximum, ent.ID())
	}
	s.checks = make([]checkSchedule, int(maximum)+1)
	for id, ent := range s.entities {
		m, ok := s.store.Get(id)
		if !ok {
			continue
		}
		if m.Policy.Enabled {
			if m.Generation == 0 {
				m.NextCheck = now.Add(staggerPhase(ent.ID(), m.Policy.Interval))
			}
			// An overdue restart resumes one safe check; its result accounts for
			// elapsed cadence slots rather than dispatching a catch-up burst.
			s.checks[ent.ID()].next = m.NextCheck.UnixNano()
			s.scheduler.Schedule(ent, m.NextCheck)
		}
		s.project(ent, m)
		s.scheduleActions(ent, m)
		rows = append(rows, s.summary(ent, m))
	}
	s.index = snapshot.NewIndex(rows)
	s.holder.SetIndex(s.index)
}
func (s *DurableSystem) StopAdmission()      { s.stopping = true }
func (s *DurableSystem) Finalize(*ecs.World) { s.persistSLO(time.Now()) }

func (s *DurableSystem) Update(*ecs.World) {
	started := time.Now()
	s.processed = 0
	defer func() { s.logger.LogSystemPerformance("DurableSystem", time.Since(started), s.processed) }()
	if s.lastError != nil || !s.store.Status().Ready {
		s.discardResults()
		return
	}
	s.drain()
	if s.lastError != nil {
		return
	}
	now := time.Now()
	if now.Sub(s.lastSLO) >= s.config.SLO.EvaluationInterval {
		s.persistSLO(now)
	}
	if s.index != nil {
		s.index.Touch(now)
	}
	if s.stopping || s.lastError != nil {
		return
	}
	s.dispatchChecks(now)
	s.dispatchActions(now)
}

func (s *DurableSystem) fail(err error) {
	s.store.MarkUnavailable(err)
	s.lastError = err
	s.stopping = true
	s.logger.Error("Durable admission stopped: %v", err)
}
func (s *DurableSystem) persistSLO(now time.Time) {
	state := s.store.SLO().Snapshot(now)
	if _, err := s.store.Submit(context.Background(), []durable.Command{{Kind: "slo", At: now, SLO: &state}}); err != nil {
		s.fail(err)
		return
	}
	s.lastSLO = now
}

func (s *DurableSystem) drain() {
	var batch []jobs.Result
	// Rotate the first pipeline so sustained check load cannot indefinitely
	// postpone committed intervention or notification results.
	first := s.resultCursor
	s.resultCursor = (s.resultCursor + 1) % len(s.results)
	for i := range s.results {
		n := (first + i) % len(s.results)
		ch := s.results[n]
	loop:
		for ch != nil && len(batch) < s.config.Storage.BatchSize {
			select {
			case rows, ok := <-ch:
				if !ok {
					s.results[n] = nil
					break loop
				}
				batch = append(batch, rows...)
			default:
				break loop
			}
		}
	}
	for len(batch) > 0 {
		count := min(len(batch), s.config.Storage.BatchSize)
		s.commitResults(batch[:count])
		batch = batch[count:]
		if s.lastError != nil {
			return
		}
	}
}

func (s *DurableSystem) commitResults(batch []jobs.Result) {
	commands := make([]durable.Command, 0, len(batch))
	accepted := make([]jobs.Result, 0, len(batch))
	now := time.Now()
	for _, r := range batch {
		ent, ok := s.entities[r.MonitorID]
		if !ok {
			continue
		}
		state := s.states.Get(ent)
		if state.Revision != r.Revision {
			continue
		}
		outcome := "success"
		if r.Err != nil {
			outcome = "failure"
		}
		timeout := errors.Is(r.Err, context.DeadlineExceeded) || errors.Is(r.Err, context.Canceled)
		if timeout {
			outcome = "timeout"
		}
		c := durable.Command{Kind: "pulse", MonitorID: r.MonitorID, Revision: r.Revision, At: now, Generation: r.Generation, Outcome: outcome, Scheduled: r.Scheduled, ExecutionStart: r.ExecutionStart, ExecutionEnd: r.ExecutionEnd, Warning: r.Warning != "", Maintenance: schema.InMaintenance(state.Maintenance, now), Driver: r.Driver}
		if r.Type != "pulse" {
			c.Kind, c.ActionID = "result", r.ActionID
			c.Generation = state.PulseGeneration
			if s.pending[state.MonitorID] {
				c.Generation++
			}
			var rejection *jobs.DeliveryError
			definite := errors.As(r.Err, &rejection)
			c.Retryable = definite && rejection.Retryable
			c.Ambiguous = r.Err != nil && !definite
		}
		commands = append(commands, c)
		accepted = append(accepted, r)
	}
	if len(commands) == 0 {
		return
	}
	results, err := s.store.Submit(context.Background(), commands)
	if err != nil {
		s.fail(err)
		return
	}
	s.processed += len(results)
	committed := time.Now()
	for n, r := range results {
		job := accepted[n]
		delete(s.admitted, job.ActionID)
		if r.Monitor == nil {
			// A worker may defer before the started marker (for example when
			// a maintenance window begins while it waits in the queue).
			if job.Type != "pulse" {
				if m, ok := s.store.Get(job.MonitorID); ok {
					s.scheduleActions(s.entities[job.MonitorID], m)
				}
			}
			continue
		}
		ent := s.entities[job.MonitorID]
		m := *r.Monitor
		if job.Type == "pulse" {
			delete(s.pending, job.MonitorID)
			s.store.SLO().Observe(job.Driver, job.Scheduled, job.ExecutionStart, job.ExecutionEnd, committed, commands[n].Outcome == "timeout", 0)
		}
		s.project(ent, m)
		s.scheduleActions(ent, m)
		if s.index != nil {
			s.index.Put(s.summary(ent, m))
		}
	}
}

func (s *DurableSystem) dispatchChecks(now time.Time) {
	for _, ent := range s.scheduler.Due(now) {
		slot := &s.checks[ent.ID()]
		due := time.Unix(0, slot.next)
		if due.After(now) {
			s.scheduler.Schedule(ent, due)
			continue
		}
		cfg := s.configs.Get(ent)
		elapsed := uint64(now.Sub(due) / cfg.Interval)
		latest := due.Add(time.Duration(elapsed) * cfg.Interval)
		slot.next = latest.Add(cfg.Interval).UnixNano()
		s.scheduler.Schedule(ent, time.Unix(0, slot.next))
		state := s.states.Get(ent)
		state.NextCheckTime = time.Unix(0, slot.next)
		if s.pending[state.MonitorID] || slot.ready != 0 {
			s.store.SLO().Missed(cfg.Type, now, elapsed+1)
			continue
		}
		s.store.SLO().Missed(cfg.Type, now, elapsed)
		s.store.SLO().Expect(cfg.Type, latest)
		slot.ready = latest.UnixNano()
		s.scheduler.EnqueueReady([]ecs.Entity{ent})
	}
	budget := min(s.config.Storage.BatchSize, s.queues["pulse"].Stats().Capacity-s.queues["pulse"].Stats().QueueDepth)
	if budget <= 0 {
		return
	}
	for _, ent := range s.scheduler.ConsumeReady(budget) {
		state := s.states.Get(ent)
		if state == nil || s.pending[state.MonitorID] {
			continue
		}
		js := s.jobs.Get(ent)
		if js == nil || isNilJob(js.PulseJob) {
			continue
		}
		cfg := s.configs.Get(ent)
		d := jobs.NewDispatch(js.PulseJob, ent, "pulse", "", state.PulseGeneration+1, 0)
		d.MonitorID, d.Revision, d.Driver, d.Scheduled = state.MonitorID, state.Revision, cfg.Type, time.Unix(0, s.checks[ent.ID()].ready)
		if err := s.queues["pulse"].Enqueue(d); err != nil {
			s.scheduler.Requeue([]ecs.Entity{ent})
			continue
		}
		s.checks[ent.ID()].ready = 0
		s.pending[state.MonitorID] = true
		state.SetPulsePending(true)
	}
}

func (s *DurableSystem) scheduleActions(ent ecs.Entity, m durable.Monitor) {
	var due time.Time
	for _, a := range m.Actions {
		if a.State == durable.Queued && !s.admitted[a.ID] && (due.IsZero() || a.NotBefore.Before(due)) {
			due = a.NotBefore
		}
	}
	if !due.IsZero() {
		s.scheduleActionAt(ent, due)
	}
}

func (s *DurableSystem) dispatchActions(now time.Time) {
	s.actions.EnqueueReady(s.actions.Due(now))
	for _, ent := range s.actions.ConsumeReady(s.config.Storage.BatchSize) {
		due, exists := s.actionDue[ent]
		if !exists {
			continue
		}
		if due.After(now) {
			s.actions.Schedule(ent, due)
			continue
		}
		delete(s.actionDue, ent)
		state := s.states.Get(ent)
		m, ok := s.store.Get(state.MonitorID)
		if !ok || !m.Policy.Enabled {
			continue
		}
		if schema.InMaintenance(state.Maintenance, now) {
			s.scheduleActionAt(ent, now.Add(time.Second))
			continue
		}
		ids := make([]string, 0, len(m.Actions))
		for id := range m.Actions {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			a := m.Actions[id]
			if a.State != durable.Queued || a.Revision != state.Revision || s.admitted[id] || a.NotBefore.After(now) {
				continue
			}
			js := s.jobs.Get(ent)
			prototype := js.InterventionJob
			if a.Kind == "code" {
				list := js.CodeJobs[a.Color]
				if a.Endpoint < 0 || a.Endpoint >= len(list) {
					s.fail(fmt.Errorf("configured endpoint missing for committed action"))
					return
				}
				prototype = list[a.Endpoint]
			}
			if isNilJob(prototype) {
				s.fail(fmt.Errorf("configured job missing for committed action"))
				return
			}
			d := jobs.NewDispatch(prototype, ent, a.Kind, a.Color, 0, a.Endpoint)
			d.MonitorID, d.Revision, d.ActionID = m.ID, a.Revision, id
			d.Scheduled = a.NotBefore
			monitorID, revision, actionID := m.ID, a.Revision, id
			maintenance := slices.Clone(state.Maintenance)
			d.Before = func(ctx context.Context) error {
				if schema.InMaintenance(maintenance, time.Now()) {
					return fmt.Errorf("action deferred during maintenance")
				}
				results, err := s.store.Submit(ctx, []durable.Command{{Kind: "start", MonitorID: monitorID, Revision: revision, ActionID: actionID, At: time.Now().UTC()}})
				if err != nil {
					return err
				}
				if !results[0].Allowed {
					return fmt.Errorf("action is held or no longer eligible")
				}
				return nil
			}
			if err := s.queues[a.Kind].Enqueue(d); err != nil {
				continue
			}
			s.admitted[id] = true
		}
		s.scheduleActions(ent, m)
	}
}

func (s *DurableSystem) project(ent ecs.Entity, m durable.Monitor) {
	state := s.states.Get(ent)
	state.PulseGeneration = m.Generation
	state.LastCheckTime, state.LastSuccessTime, state.NextCheckTime = m.LastCheck, m.LastSuccess, m.NextCheck
	if int(ent.ID()) < len(s.checks) && s.checks[ent.ID()].next != 0 {
		state.NextCheckTime = time.Unix(0, s.checks[ent.ID()].next)
	}
	state.LastLatency, state.LatencyAvailable = m.LastLatency, m.LatencyAvailable
	state.ConsecutiveFailures, state.PulseFailures, state.InterventionFailures = m.ConsecutiveFailures, m.PulseFailures, m.InterventionFailures
	state.Recovering, state.InterventionAttempted, state.RecoveryStreak, state.VerifyRemaining = m.Recovering, m.InterventionAttempted, m.RecoveryStreak, m.VerifyRemaining
	state.VerificationAfter = m.VerificationAfter
	state.Flags = 0
	if m.Incident {
		state.Flags |= components.StateIncidentOpen
	}
	if m.VerifyRemaining > 0 {
		state.Flags |= components.StateVerifying
	}
	if s.pending[m.ID] {
		state.SetPulsePending(true)
	}
	state.LastError = nil
	if m.LastOutcome != "" && m.LastOutcome != "success" {
		state.LastError = fmt.Errorf("health check %s", m.LastOutcome)
	}
	state.PulseWarning = ""
	if m.Warning {
		state.PulseWarning = "check requires attention"
	}
	state.PendingCode = ""
	for _, a := range m.Actions {
		if a.Kind == "intervention" && (a.State == durable.Queued || a.State == durable.Started || a.State == durable.Unknown) {
			state.SetInterventionPending(true)
		}
		if a.Kind == "code" && (a.State == durable.Queued || a.State == durable.Started) {
			state.SetCodePending(true)
			if state.PendingCode == "" || a.Color < state.PendingCode {
				state.PendingCode = a.Color
			}
		}
	}
}

func (s *DurableSystem) summary(ent ecs.Entity, m durable.Monitor) snapshot.MonitorSummary {
	state, cfg := s.states.Get(ent), s.configs.Get(ent)
	status := classifyStatus(state)
	if !m.Policy.Enabled {
		status = "disabled"
	}
	colors := make([]string, 0, len(m.Policy.Endpoints))
	for c := range m.Policy.Endpoints {
		colors = append(colors, c)
	}
	slices.Sort(colors)
	unknown := 0
	for _, a := range m.Actions {
		if a.State == durable.Unknown {
			unknown++
		}
	}
	uptime := float64(0)
	if m.TotalChecks > 0 {
		uptime = float64(m.SuccessfulChecks) / float64(m.TotalChecks)
	}
	return snapshot.MonitorSummary{Uptime: uptime, ID: ent.ID(), MonitorID: m.ID, Name: m.Name, PulseType: cfg.Type, Status: status, Incident: m.Incident, PendingCode: state.PendingCode, ConsecutiveFailures: m.ConsecutiveFailures, LastCheck: m.LastCheck, LastSuccess: m.LastSuccess, NextCheck: state.NextCheckTime, ActiveCodes: colors, Target: pulseTarget(cfg), IntervalMs: cfg.Interval.Milliseconds(), LatencyMs: float64(m.LastLatency) / float64(time.Millisecond), LatencyAvailable: m.LatencyAvailable, UnknownActions: unknown, Warning: state.PulseWarning}
}

// Once persistence fails, results cannot be acknowledged. Drain the runtime
// channels so shutdown can finish; committed started markers remain recoverable
// as unknown. No new action is admitted while the store is unavailable.
func (s *DurableSystem) discardResults() {
	for n, ch := range s.results {
	drain:
		for count := 0; count < 32 && ch != nil; count++ {
			select {
			case _, ok := <-ch:
				if !ok {
					s.results[n] = nil
					break drain
				}
			default:
				break drain
			}
		}
	}
}

func (s *DurableSystem) scheduleActionAt(ent ecs.Entity, due time.Time) {
	if existing, ok := s.actionDue[ent]; ok && !existing.After(due) {
		return
	}
	s.actionDue[ent] = due
	s.actions.Schedule(ent, due)
}
