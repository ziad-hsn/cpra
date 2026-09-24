package systems

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

// DurableSystem owns the live projection and all scheduling. The Raft goroutine
// sees only copied commands. Workers see private jobs and immutable identities.
type checkSchedule struct {
	next, ready int64
	arrivalRate float64
}

type DurableSystem struct {
	catalogRuntime      *catalogRuntime
	managed             map[string]managedMonitor
	checks              []checkSchedule
	pulseArrivalRate    float64
	activePulseMonitors int
	store               *persistence.Store
	world               *ecs.World
	states              *ecs.Map1[components.MonitorState]
	configs             *ecs.Map1[components.PulseConfig]
	jobs                *ecs.Map1[components.JobStorage]
	codes               *ecs.Map1[components.CodeConfig]
	disabled            *ecs.Map1[components.Disabled]
	pauses              *ecs.Map1[components.CheckPause]
	controls            *ecs.Map1[components.ControlState]
	controlCursor       persistence.ControlCursor
	controlView         *persistence.ControlView
	controlAfter        string
	snoozes             *controlDeadlines
	interventions       *ecs.Map1[components.InterventionConfig]
	scheduler           *PulseScheduler
	actions             *PulseScheduler
	actionDue           map[ecs.Entity]time.Time
	queues              map[string]queue.Queue
	results             []<-chan []jobs.Result
	resultCursor        int
	processed           int
	entities            map[string]ecs.Entity
	admitted            map[string]bool
	pending             map[string]bool
	pendingCheckID      map[string]uuid.UUID
	holder              *fleetview.Holder
	index               *fleetview.Index
	config              runtimeconfig.Config
	cooldown            time.Duration
	recoveryBypass      bool
	stopping            atomic.Bool
	recovering          atomic.Bool
	nextRecovery        time.Time
	pendingWrite        *ownerWrite
	resultBacklog       []jobs.Result
	committer           ownerCommitter
	controlsPending     bool
	ownerInitialized    bool
	lastSLO             time.Time
	lastError           error
	logger              Logger
}

func NewDurableSystem(w *ecs.World, store *persistence.Store, cfg runtimeconfig.Config, holder *fleetview.Holder, logger Logger, pq, iq, cq queue.Queue, channels []<-chan []jobs.Result, cooldown time.Duration, bypass bool) *DurableSystem {
	return &DurableSystem{actionDue: map[ecs.Entity]time.Time{}, store: store, world: w, states: ecs.NewMap1[components.MonitorState](w), configs: ecs.NewMap1[components.PulseConfig](w), jobs: ecs.NewMap1[components.JobStorage](w), codes: ecs.NewMap1[components.CodeConfig](w), disabled: ecs.NewMap1[components.Disabled](w), pauses: ecs.NewMap1[components.CheckPause](w), controls: ecs.NewMap1[components.ControlState](w), snoozes: newControlDeadlines(), pendingCheckID: make(map[string]uuid.UUID), interventions: ecs.NewMap1[components.InterventionConfig](w), scheduler: NewPulseScheduler(), actions: NewPulseScheduler(), queues: map[string]queue.Queue{"pulse": pq, "intervention": iq, "code": cq}, results: channels, entities: map[string]ecs.Entity{}, admitted: map[string]bool{}, pending: map[string]bool{}, holder: holder, config: cfg, cooldown: cooldown, recoveryBypass: bypass, logger: logger}
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
	commands := make([]persistence.Command, 0, s.config.Storage.BatchSize)
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
		policy := persistence.Policy{Interval: cfg.Interval, Unhealthy: cfg.UnhealthyThreshold, Healthy: cfg.HealthyThreshold, Enabled: s.disabled.Get(ent) == nil, Intervention: s.interventions.Get(ent) != nil, Cooldown: s.cooldown, RecoveryBypass: s.recoveryBypass, Endpoints: map[string]int{}}
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
		m := persistence.Monitor{ID: state.MonitorID, Revision: state.Revision, Name: state.Name, Policy: policy}
		commands = append(commands, persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()})
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
	view, err := s.store.ControlSnapshot()
	if err != nil {
		s.fail(err)
		return
	}
	s.controlCursor = view.Cursor
	rows := make([]fleetview.MonitorSummary, 0, len(s.entities))
	var maximum uint32
	for _, ent := range s.entities {
		maximum = max(maximum, ent.ID())
	}
	s.checks = make([]checkSchedule, int(maximum)+1)
	s.pulseArrivalRate, s.activePulseMonitors = 0, 0
	for id, ent := range s.entities {
		m, ok := s.store.Get(id)
		if !ok {
			continue
		}
		if m.Policy.Enabled && m.SnoozedUntil.IsZero() {
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
	s.index = fleetview.NewIndex(rows)
	s.holder.SetIndex(s.index)
	if s.catalogRuntime != nil {
		if err := s.observeLoadedCatalog(); err != nil {
			s.fail(err)
			return
		}
		s.catalogRuntime.start()
	}
	s.ownerInitialized = true
}

// PulseArrivalRate returns owner-maintained scheduled demand in checks/second.
// Read only on the owner loop after initialization.
func (s *DurableSystem) PulseArrivalRate() float64 { return s.pulseArrivalRate }

func (s *DurableSystem) setPulseDemand(ent ecs.Entity, interval time.Duration) {
	if int(ent.ID()) >= len(s.checks) {
		return
	}
	slot := &s.checks[ent.ID()]
	rate := 0.0
	if interval > 0 {
		rate = 1 / interval.Seconds()
	}
	if rate == slot.arrivalRate {
		return
	}
	if slot.arrivalRate > 0 {
		s.activePulseMonitors--
	}
	if rate > 0 {
		s.activePulseMonitors++
	}
	s.pulseArrivalRate += rate - slot.arrivalRate
	slot.arrivalRate = rate
	if s.activePulseMonitors == 0 {
		s.pulseArrivalRate = 0
	}
}

func (s *DurableSystem) resetCheckSchedule(ent ecs.Entity) {
	s.setPulseDemand(ent, 0)
	if int(ent.ID()) < len(s.checks) {
		s.checks[ent.ID()] = checkSchedule{}
	}
}

// AdmissionReady is safe for readiness readers outside the owner loop.
func (s *DurableSystem) AdmissionReady() bool { return !s.stopping.Load() && !s.recovering.Load() }

func (s *DurableSystem) StopAdmission() {
	s.stopping.Store(true)
	if s.catalogRuntime != nil {
		s.catalogRuntime.cancel()
	}
}

// Finalize runs on the owner after all worker pools and result producers have
// joined. Normal updates intentionally bound result admission, so one update
// cannot flush an arbitrary buffered tail. Commit every available outcome
// before recording the final SLO window and closing durable storage.
func (s *DurableSystem) Finalize(*ecs.World) {
	if s.catalogRuntime != nil {
		s.catalogRuntime.cancel()
		// Initialization may have failed before starting reconciliation. Join
		// the same once-started, already-cancelled goroutine in that case too.
		s.catalogRuntime.start()
		<-s.catalogRuntime.done
		select {
		case p := <-s.catalogRuntime.updates:
			if p.prepared != nil {
				_ = p.prepared.Close()
			}
		default:
		}
	}
	// StopContext bounds the caller, not the lifetime of already accepted work.
	// Keep this owner (and therefore storage/dependencies) alive until its exact
	// writes settle. A permanent fault with pending work requires process restart.
	for s.pendingWrite != nil || s.resultsBuffered() {
		s.Update(s.world)
		if s.pendingWrite != nil || s.resultsBuffered() {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if s.lastError != nil {
		return // Initialization failed without accepting any work.
	}
	s.persistSLO(time.Now())
	for s.pendingWrite != nil {
		s.Update(s.world)
		if s.pendingWrite != nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := s.store.FinishReturnedExecutions(ctx)
		cancel()
		if err == nil {
			break
		}
		// Do not report Done or let the caller close dependencies while a
		// completion marker remains unconfirmed, even after its wait times out.
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *DurableSystem) Update(*ecs.World) {
	started := time.Now()
	s.processed = 0
	defer func() { s.logger.LogSystemPerformance("DurableSystem", time.Since(started), s.processed) }()
	if s.lastError != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	err := s.store.ControllerHealthContext(ctx)
	if err != nil {
		s.recoveryError(err, ctx)
		cancel()
		return
	}
	cancel()
	if s.recovering.Load() && !s.recoverWrites(started) {
		return
	}
	if !s.drain() {
		return
	}
	s.receiveCatalogProjection()
	if s.pendingWrite != nil || s.lastError != nil {
		return
	}
	s.receiveControlProjection()
	if s.lastError != nil || s.controlsPending {
		return
	}
	now := time.Now()
	if now.Sub(s.lastSLO) >= s.config.SLO.EvaluationInterval {
		s.persistSLO(now)
	}
	if s.pendingWrite != nil || s.lastError != nil {
		return
	}
	if s.index != nil {
		s.index.Touch(now)
	}
	if s.stopping.Load() || s.lastError != nil {
		return
	}
	s.expireSnoozes(now)
	if s.lastError != nil || s.pendingWrite != nil {
		return
	}
	if s.recovering.Load() {
		if s.resultsBuffered() {
			return
		}
		// A follower transition may happen after the initial health check.
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := s.store.ControllerHealthContext(ctx)
		if err != nil {
			s.recoveryError(err, ctx)
			cancel()
			return
		}
		cancel()
		s.recovering.Store(false)
	}
	s.dispatchChecks(now)
	s.dispatchActions(now)
}

func (s *DurableSystem) fail(err error) {
	if persistence.IsLeadershipUnavailable(err) && s.lastError == nil && s.ownerInitialized {
		// The next bounded health observation checks permanent failure first.
		// Do not wait without a deadline while closing admission here.
		s.recovering.Store(true)
		return
	}
	s.store.MarkUnavailable(err)
	s.lastError = err
	s.stopping.Store(true)
	s.logger.Error("Durable admission stopped: %v", err)
}
func (s *DurableSystem) persistSLO(now time.Time) {
	state := s.store.SLO().Snapshot(now)
	s.beginOwnerWrite(&ownerWrite{kind: sloWrite, commands: []persistence.Command{{Kind: "slo", At: now, SLO: &state}}})
}

func (s *DurableSystem) drain() bool {
	if s.pendingWrite != nil {
		return false
	}
	if len(s.resultBacklog) != 0 {
		return s.commitBacklog()
	}
	if len(s.results) == 0 {
		return true
	}
	// Rotate the first pipeline so sustained check load cannot indefinitely
	// postpone committed intervention or notification results.
	first := s.resultCursor
	s.resultCursor = (s.resultCursor + 1) % len(s.results)
	for i := range s.results {
		n := (first + i) % len(s.results)
		ch := s.results[n]
	loop:
		for ch != nil && len(s.resultBacklog) < s.config.Storage.BatchSize {
			select {
			case rows, ok := <-ch:
				if !ok {
					s.results[n] = nil
					break loop
				}
				s.resultBacklog = append(s.resultBacklog, rows...)
			default:
				break loop
			}
		}
	}
	return s.commitBacklog()
}

func (s *DurableSystem) commitBacklog() bool {
	count := min(len(s.resultBacklog), s.config.Storage.BatchSize)
	if count == 0 {
		return true
	}
	batch := s.resultBacklog[:count]
	if !s.commitResults(batch) {
		// Commands own the consumed prefix on uncertainty; only an error before
		// preparation leaves the complete received batch in the backlog.
		if s.pendingWrite != nil {
			clear(batch)
			s.resultBacklog = s.resultBacklog[count:]
		}
		return false
	}
	clear(batch)
	s.resultBacklog = s.resultBacklog[count:]
	return true
}

func (s *DurableSystem) commitResults(batch []jobs.Result) bool {
	// Keep provider outcomes intact while their separately owned completion
	// markers recover. No external callback is run by this reconciliation.
	needsCompletion := false
	for _, r := range batch {
		if r.FinalizationErr != nil {
			if !persistence.IsLeadershipUnavailable(r.FinalizationErr) {
				s.fail(r.FinalizationErr)
				return false
			}
			needsCompletion = true
		}
	}
	if needsCompletion {
		s.recovering.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		status, err := s.store.RetryReturnedExecutions(ctx)
		if err != nil {
			s.recoveryError(err, ctx)
			cancel()
			return false
		}
		cancel()
		if status.Returned != 0 {
			return false
		}
	}
	commands := make([]persistence.Command, 0, len(batch))
	accepted := make([]jobs.Result, 0, len(batch))
	uids := make([]string, 0, len(batch))
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	now := time.Now()
	for _, r := range batch {
		if r.Type == "pulse" && r.ExecutionStart.IsZero() && (persistence.IsLeadershipUnavailable(r.Err) || errors.Is(r.Err, errAdmissionRecovering) || errors.Is(r.Err, context.Canceled) || errors.Is(r.Err, context.DeadlineExceeded)) {
			s.requeueWithheldCheck(r)
			s.recovering.Store(true)
			continue
		}
		var current persistence.Monitor
		var found bool
		if r.ActionID != "" || r.MonitorID != "" {
			var err error
			current, found, err = s.store.GetContext(ctx, r.MonitorID)
			if err != nil {
				s.recoveryError(err, ctx)
				return false
			}
		}
		// An action already granted a durable start belongs to its original
		// identity even if its monitor was replaced or removed while it ran.
		// Keep the conservative unknown disposition and append late evidence;
		// never apply that old result to the new incident or verification cycle.
		if r.Type != "pulse" && r.ActionID != "" && !r.ExecutionStart.IsZero() {
			if found {
				if a, ok := current.Actions[r.ActionID]; ok && a.State == persistence.Unknown && a.Revision == r.Revision {
					outcome := "success"
					if r.Err != nil {
						outcome = "failure"
					}
					var rejection *jobs.DeliveryError
					definite := errors.As(r.Err, &rejection)
					if r.Err != nil && !definite {
						s.releaseResult(r)
						continue
					} // No additional known outcome beyond the retained unknown marker.
					at := now
					if r.ExecutionEnd.After(at) {
						at = r.ExecutionEnd
					}
					commands = append(commands, persistence.Command{Kind: "late_result", MonitorID: r.MonitorID, Revision: r.Revision, ActionID: r.ActionID, At: at, Outcome: outcome, ExecutionStart: r.ExecutionStart, ExecutionEnd: r.ExecutionEnd})
					accepted = append(accepted, r)
					uids = append(uids, a.CatalogUID)
					continue
				}
			}
		}
		ent, ok := s.entities[r.MonitorID]
		if !ok {
			s.releaseResult(r)
			continue
		}
		state := s.states.Get(ent)
		if state.Revision != r.Revision {
			s.releaseResult(r)
			continue
		}
		projection, managed := s.managed[r.MonitorID]
		if r.Type == "pulse" && managed && projection.version != r.ProjectionVersion {
			s.releaseResult(r)
			continue
		}
		if r.Type == "pulse" && r.ExecutionStart.IsZero() && (errors.Is(r.Err, persistence.ErrCatalogDependency) || errors.Is(r.Err, persistence.ErrControlConflict)) {
			s.releaseResult(r)
			continue // expired execution grant; the new catalog view reschedules it
		}
		outcome := "success"
		if r.Err != nil {
			outcome = "failure"
		}
		timeout := errors.Is(r.Err, context.DeadlineExceeded) || errors.Is(r.Err, context.Canceled)
		if timeout {
			outcome = "timeout"
		}
		c := persistence.Command{Kind: "pulse", MonitorID: r.MonitorID, Revision: r.Revision, At: now, Generation: r.Generation, CheckControlRevision: r.CheckControlRevision, Outcome: outcome, Scheduled: r.Scheduled, ExecutionStart: r.ExecutionStart, ExecutionEnd: r.ExecutionEnd, Warning: r.Warning != "", Maintenance: manifest.InMaintenance(state.Maintenance, now), Driver: r.Driver}
		if r.Type == "pulse" && managed {
			guard := projection.guard.Clone()
			c.Guard = &guard
		}
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
		uids = append(uids, current.CatalogUID)
	}
	if len(commands) == 0 {
		return true
	}
	return s.beginOwnerWrite(&ownerWrite{kind: resultWrite, commands: commands, observations: accepted, monitorUIDs: uids})
}

func (s *DurableSystem) finishResultWrite(ctx context.Context, p *ownerWrite) error {
	results, commands := p.results, p.commands
	// Check the entire response before any projection/SLO side effects. Only
	// documented stale grants and conflicting late evidence are terminal rejections.
	for n, result := range results {
		if result.Monitor != nil && result.Monitor.ID != commands[n].MonitorID {
			return errors.New("controller command returned a different monitor identity")
		}
		if result.Err == nil {
			continue
		}
		if result.Monitor != nil {
			return errors.New("failed controller command returned a monitor projection")
		}
		if errors.Is(result.Err, persistence.ErrCatalogDependency) || errors.Is(result.Err, persistence.ErrControlConflict) ||
			(commands[n].Kind == "late_result" && errors.Is(result.Err, persistence.ErrLateEvidenceConflict)) {
			continue
		}
		return result.Err
	}
	for p.completed < len(results) {
		n := p.completed
		r := results[n]
		job := p.observations[n]
		if commands[n].Kind == "late_result" {
			// No old action result may touch the replacement's live incident.
			// The late evidence is available through retained durable history.
			if errors.Is(r.Err, persistence.ErrLateEvidenceConflict) {
				s.logger.Error("Conflicting late evidence rejected for held action %s", job.ActionID)
			} else if r.Err != nil {
				s.logger.Error("Late evidence could not be retained for held action %s; review original outcome", job.ActionID)
			}
			s.releaseResult(job)
			p.completed++
			continue
		}
		var current persistence.Monitor
		var found bool
		if r.Monitor == nil {
			var err error
			current, found, err = s.store.GetContext(ctx, job.MonitorID)
			if err != nil {
				return err
			}
		}
		// Everything below is local owner work. Advance this item's cursor once;
		// a later contextual read cannot cause its SLO observation to run again.
		s.releaseResult(job)
		p.completed++
		s.processed++
		// Duplicate lifecycle commands can return an empty result. A pulse is
		// accepted only when the original generation, time, outcome and incarnation
		// are still evidenced by committed state. Replaying is not that evidence.
		if r.Monitor == nil && job.Type == "pulse" {
			if found && current.CatalogUID == p.monitorUIDs[n] && current.Revision == commands[n].Revision && current.Generation == commands[n].Generation && current.LastCheck.Equal(commands[n].At) && current.LastOutcome == commands[n].Outcome {
				r.Monitor = &current
			}
		}
		if r.Monitor == nil {
			// A worker may defer before the started marker (for example when
			// a maintenance window begins while it waits in the queue).
			if job.Type != "pulse" {
				if m := current; found && m.CatalogUID == p.monitorUIDs[n] && m.Revision == job.Revision && !m.Removed {
					if ent, exists := s.entities[job.MonitorID]; exists && s.world.Alive(ent) && s.states.Get(ent).Revision == m.Revision {
						s.project(ent, m)
						s.scheduleActions(ent, m)
						if s.index != nil {
							s.index.Put(s.summary(ent, m))
						}
					}
				}
			}
			continue
		}
		ent, exists := s.entities[job.MonitorID]
		m := *r.Monitor
		if m.CatalogUID != p.monitorUIDs[n] || m.Revision != job.Revision {
			continue
		}
		if job.Type == "pulse" {
			s.store.SLO().Observe(job.Driver, job.Scheduled, job.ExecutionStart, job.ExecutionEnd, time.Now(), commands[n].Outcome == "timeout", 0)
		}
		if !exists || !s.world.Alive(ent) || s.states.Get(ent).Revision != m.Revision || m.CatalogUID != p.monitorUIDs[n] {
			continue
		}
		s.project(ent, m)
		s.scheduleActions(ent, m)
		if s.index != nil {
			s.index.Put(s.summary(ent, m))
		}
	}
	return nil
}

func (s *DurableSystem) dispatchChecks(now time.Time) {
	for _, ent := range s.scheduler.Due(now) {
		if s.stopping.Load() {
			return
		}
		if !s.world.Alive(ent) || s.disabled.Get(ent) != nil || s.isSnoozed(ent) {
			continue
		}
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
			state.MissedChecks += elapsed + 1
			s.publishCheckAdmission(ent)
			continue
		}
		s.store.SLO().Missed(cfg.Type, now, elapsed)
		state.MissedChecks += elapsed
		s.store.SLO().Expect(cfg.Type, latest)
		slot.ready = latest.UnixNano()
		s.scheduler.EnqueueReady([]ecs.Entity{ent})
		s.publishCheckAdmission(ent)
	}
	budget := min(s.config.Storage.BatchSize, s.queues["pulse"].Stats().Capacity-s.queues["pulse"].Stats().QueueDepth)
	if budget <= 0 {
		return
	}
	for _, ent := range s.scheduler.ConsumeReady(budget) {
		if s.stopping.Load() {
			return
		}
		if !s.world.Alive(ent) || s.disabled.Get(ent) != nil || s.isSnoozed(ent) {
			continue
		}
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
		var guard *persistence.CatalogGuard
		if projection, ok := s.managed[state.MonitorID]; ok {
			g := projection.guard.Clone()
			guard = &g
			d.ProjectionVersion = projection.version
		}
		id, controlRevision := state.MonitorID, s.controls.Get(ent).Revision
		d.CheckControlRevision = controlRevision
		d.Before = func(ctx context.Context) error {
			if s.stopping.Load() {
				return persistence.ErrControlConflict
			}
			if s.recovering.Load() {
				return errAdmissionRecovering
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			admission, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			defer cancel()
			err := s.store.CheckCheckAdmissionContext(admission, id, guard, controlRevision, time.Now().UTC())
			if err != nil && admission.Err() != nil && errors.Is(err, admission.Err()) {
				return errors.Join(errAdmissionRecovering, err)
			}
			return err
		}
		if err := s.queues["pulse"].Enqueue(d); err != nil {
			state.QueueRejections++
			s.publishCheckAdmission(ent)
			s.scheduler.Requeue([]ecs.Entity{ent})
			continue
		}
		s.checks[ent.ID()].ready = 0
		s.pending[state.MonitorID] = true
		s.pendingCheckID[state.MonitorID] = d.ID
		state.SetPulsePending(true)
		s.publishCheckAdmission(ent)
	}
}

func (s *DurableSystem) scheduleActions(ent ecs.Entity, m persistence.Monitor) {
	if !m.Policy.Enabled || m.Removed || !m.SnoozedUntil.IsZero() {
		return
	}
	var due time.Time
	for _, a := range m.Actions {
		if a.State == persistence.Queued && a.CatalogUID == m.CatalogUID && !s.admitted[a.ID] && (due.IsZero() || a.NotBefore.Before(due)) {
			due = a.NotBefore
		}
	}
	if !due.IsZero() {
		s.scheduleActionAt(ent, due)
	}
}

func (s *DurableSystem) dispatchActions(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.actions.EnqueueReady(s.actions.Due(now))
	ready := s.actions.ConsumeReady(s.config.Storage.BatchSize)
	for n, ent := range ready {
		if s.stopping.Load() {
			return
		}
		if !s.world.Alive(ent) || s.disabled.Get(ent) != nil || s.isSnoozed(ent) {
			continue
		}
		due, exists := s.actionDue[ent]
		if !exists {
			continue
		}
		if due.After(now) {
			s.actions.Schedule(ent, due)
			continue
		}
		state := s.states.Get(ent)
		m, ok, err := s.store.GetContext(ctx, state.MonitorID)
		if err != nil {
			s.actions.Requeue(ready[n:])
			s.recoveryError(err, ctx)
			return
		}
		delete(s.actionDue, ent)
		if !ok || !m.Policy.Enabled || !m.SnoozedUntil.IsZero() {
			continue
		}
		if manifest.InMaintenance(state.Maintenance, now) {
			s.scheduleActionAt(ent, now.Add(time.Second))
			continue
		}
		ids := make([]string, 0, len(m.Actions))
		for id := range m.Actions {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if s.stopping.Load() {
				return
			}
			a := m.Actions[id]
			if a.State != persistence.Queued || a.CatalogUID != m.CatalogUID || a.Revision != state.Revision || s.admitted[id] || a.NotBefore.After(now) {
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
			var guard *persistence.CatalogGuard
			if projection, ok := s.managed[m.ID]; ok {
				g := projection.guard.Clone()
				guard = &g
				d.ProjectionVersion = projection.version
			}
			maintenance := slices.Clone(state.Maintenance)
			d.Authorize = func(ctx context.Context) (func() error, error) {
				if s.stopping.Load() {
					return nil, persistence.ErrControlConflict
				}
				if s.recovering.Load() {
					return nil, errAdmissionRecovering
				}
				if manifest.InMaintenance(maintenance, time.Now()) {
					return nil, fmt.Errorf("action deferred during maintenance")
				}
				handle, err := s.store.BeginLocalAction(ctx, persistence.Command{Kind: "start", MonitorID: monitorID, Revision: revision, ActionID: actionID, At: time.Now().UTC(), Guard: guard})
				if handle == nil {
					return nil, err
				}
				// Always retain the reserved claim, including an uncertain start.
				// Execute invokes this only once its provider/Before path returned.
				return func() error { return handle.Finish(context.Background()) }, err
			}
			if err := s.queues[a.Kind].Enqueue(d); err != nil {
				continue
			}
			s.admitted[id] = true
		}
		s.scheduleActions(ent, m)
	}
}

func (s *DurableSystem) project(ent ecs.Entity, m persistence.Monitor) {
	s.projectControls(ent, m)
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
		if a.CatalogUID != m.CatalogUID {
			continue
		}
		if a.Kind == "intervention" && (a.State == persistence.Queued || a.State == persistence.Started || a.Held()) {
			state.SetInterventionPending(true)
		}
		if a.Kind == "code" && (a.State == persistence.Queued || a.State == persistence.Started) {
			state.SetCodePending(true)
			if state.PendingCode == "" || a.Color < state.PendingCode {
				state.PendingCode = a.Color
			}
		}
	}
}

func (s *DurableSystem) summary(ent ecs.Entity, m persistence.Monitor) fleetview.MonitorSummary {
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
		if a.State == persistence.Unknown {
			unknown++
		}
	}
	uptime := float64(0)
	if m.TotalChecks > 0 {
		uptime = float64(m.SuccessfulChecks) / float64(m.TotalChecks)
	}
	summary := fleetview.MonitorSummary{Uptime: uptime, ID: ent.ID(), MonitorID: m.ID, Name: m.Name, PulseType: cfg.Type, Status: status, Incident: m.Incident, PendingCode: state.PendingCode, ConsecutiveFailures: m.ConsecutiveFailures, LastCheck: m.LastCheck, LastSuccess: m.LastSuccess, NextCheck: state.NextCheckTime, ActiveCodes: colors, Target: pulseTarget(cfg), IntervalMs: cfg.Interval.Milliseconds(), LatencyMs: float64(m.LastLatency) / float64(time.Millisecond), LatencyAvailable: m.LatencyAvailable, UnknownActions: unknown, Warning: state.PulseWarning}
	s.checkAdmission(ent, &summary)
	return summary
}

func (s *DurableSystem) scheduleActionAt(ent ecs.Entity, due time.Time) {
	if existing, ok := s.actionDue[ent]; ok && !existing.After(due) {
		return
	}
	s.actionDue[ent] = due
	s.actions.Schedule(ent, due)
}

// checkAdmission reports owner-observed scheduling state without claiming a
// provider outcome. Counters are scoped to this runtime monitor incarnation.
func (s *DurableSystem) checkAdmission(ent ecs.Entity, summary *fleetview.MonitorSummary) {
	state := s.states.Get(ent)
	summary.MissedChecks, summary.QueueRejections = state.MissedChecks, state.QueueRejections
	summary.NextCheck, summary.Warning = state.NextCheckTime, state.PulseWarning
	summary.CheckAdmission = ""
	if s.pending[state.MonitorID] {
		summary.CheckAdmission = "pending"
	} else if int(ent.ID()) < len(s.checks) && s.checks[ent.ID()].ready != 0 {
		summary.CheckAdmission = "awaiting_capacity"
		summary.Warning = "check awaiting queue admission"
	}
}

func (s *DurableSystem) publishCheckAdmission(ent ecs.Entity) {
	if s.index == nil {
		return
	}
	if summary, ok := s.index.Get(ent.ID()); ok {
		s.checkAdmission(ent, &summary)
		s.index.Put(summary)
	}
}
