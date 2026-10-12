//go:build externaljobs

package systems

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

type systemExtensions struct{ workerRuntime *workerRuntime }

func (s *DurableSystem) workerNeedsUpdate() bool {
	r := s.workerRuntime
	return r != nil && (r.ready.Len() != 0 || len(r.pending) != 0)
}

type checkScheduleExtensions struct {
	workerSchedule persistence.WorkerScheduleTicket
}

func (s *DurableSystem) expectCheck(ent ecs.Entity, at time.Time) bool {
	if !s.workerCheck(ent) {
		s.store.SLO().Expect(s.configs.Get(ent).Type, at)
		return true
	}
	ticket, err := s.store.ScheduleWorkerCheck(at)
	if err != nil {
		s.fail(err)
		return false
	}
	s.checks[ent.ID()].workerSchedule = ticket
	if s.workerRuntime != nil {
		s.workerRuntime.schedulesFinished = false
	}
	return true
}

func (s *DurableSystem) checkpointWorkerSchedules(commands []persistence.Command, at time.Time) []persistence.Command {
	if command := s.store.WorkerScheduleCheckpoint(at); command != nil {
		commands = append(commands, *command)
	}
	return commands
}

// All fields except dispatcher cancellation belong to the ECS owner. Each
// accepted item holds a preparation slot until its result is handed off (bounded
// by 16 requests/16 MiB of guards), or an observation slot (bounded by the store's
// 4096-record limit). Full observation capacity must not prevent action expiry.
type workerRuntime struct {
	ready             ReadyQueue
	dispatcher        *workerDispatcher
	pending           map[uuid.UUID]*workerPending
	order             []uuid.UUID
	observations      map[string]int
	completed         []uuid.UUID
	cursor            int
	preparing         int
	bytes             int
	schedulesFinished bool
	actionsFirst      bool
	admission         workerAdmissionBackoff
}

func (s *DurableSystem) dispatchReady(now time.Time) {
	if r := s.workerRuntime; r != nil {
		first := r.actionsFirst
		r.actionsFirst = !first
		// Checks and incident actions share the bounded preparation pool. Rotate
		// first access so a continuous backlog cannot take every newly freed slot.
		if first {
			s.dispatchActions(now)
			if s.AdmissionReady() {
				s.dispatchChecks(now)
			}
			return
		}
	}
	s.dispatchChecks(now)
	s.dispatchActions(now)
}

type workerPending struct {
	typeKey                            workerAdmissionKey
	schedule                           persistence.WorkerScheduleTicket
	token                              uuid.UUID
	monitorID, uid, revision, actionID string
	scheduled                          time.Time
	executionID                        string
	preparing, finished                bool
	bytes                              int
	err                                error
}

func (s *DurableSystem) initWorkerDispatch(catalog *management.Catalog) {
	if s.config.ExternalJobs.Enabled && s.config.Management.Enabled && s.config.Storage.Mode == "raft" {
		s.workerRuntime = &workerRuntime{dispatcher: newWorkerDispatcher(s.store, catalog), pending: make(map[uuid.UUID]*workerPending), observations: make(map[string]int)}
	}
}
func (s *DurableSystem) startWorkerDispatch() {
	if r := s.workerRuntime; r != nil {
		r.dispatcher.start()
	}
}
func (s *DurableSystem) stopWorkerDispatch() {
	if r := s.workerRuntime; r != nil {
		r.dispatcher.cancel()
	}
}
func (s *DurableSystem) finishWorkerDispatch() {
	if r := s.workerRuntime; r != nil {
		r.dispatcher.cancel()
		r.dispatcher.start()
		<-r.dispatcher.done
		// Finalize runs after the owner loop stopped. Every request still in the
		// input buffer is known not to have reached preparation or admission.
		for {
			select {
			case request := <-r.dispatcher.requests:
				r.dispatcher.results <- workerDispatchResult{token: request.token, err: context.Canceled}
			default:
				s.receiveWorkerDispatch()
				if err := s.store.FinishWorkerSchedules(); err != nil {
					s.fail(err)
				} else {
					r.schedulesFinished = true
				}
				return
			}
		}
	}
}
func (s *DurableSystem) workerCheck(ent ecs.Entity) bool {
	j := s.jobs.Get(ent)
	return j != nil && j.ExternalJobs != nil && j.ExternalJobs.Check != nil
}
func (s *DurableSystem) enqueueCheck(ent ecs.Entity) {
	if s.workerRuntime != nil && s.workerCheck(ent) {
		s.workerRuntime.ready.Enqueue([]ecs.Entity{ent})
	} else {
		s.scheduler.EnqueueReady([]ecs.Entity{ent})
	}
}
func (s *DurableSystem) cancelWorkerCheck(ent ecs.Entity) {
	if s.workerRuntime != nil {
		s.workerRuntime.ready.Cancel(ent)
		if int(ent.ID()) < len(s.checks) {
			s.abandonWorkerSchedule(&s.checks[ent.ID()].workerSchedule)
		}
	}
}

func (s *DurableSystem) abandonWorkerSchedule(ticket *persistence.WorkerScheduleTicket) {
	if ticket.Sequence == 0 {
		return
	}
	if s.workerRuntime != nil && s.workerRuntime.schedulesFinished {
		*ticket = persistence.WorkerScheduleTicket{}
		return
	}
	if err := s.store.AbandonWorkerCheck(*ticket); err != nil {
		s.fail(err)
		return
	}
	*ticket = persistence.WorkerScheduleTicket{}
}
func (s *DurableSystem) dispatchWorkerChecks() {
	r := s.workerRuntime
	if r == nil {
		return
	}
	// Separate ready queues prevent a saturated local pool from delaying a
	// remote check behind the entire local fleet. Each queue has one slot per
	// full entity identity and cancellation recycles that slot.
	for _, ent := range r.ready.Consume(min(s.config.Storage.BatchSize, workerDispatchCapacity-r.preparing)) {
		if !s.world.Alive(ent) || s.disabled.Get(ent) != nil || s.isSnoozed(ent) || s.pending[s.states.Get(ent).MonitorID] {
			continue
		}
		s.dispatchWorkerCheck(ent)
	}
}

func (s *DurableSystem) queueWorker(ent ecs.Entity, key manifest.ExternalRuntimeKey, actionID string, scheduled time.Time, timeout time.Duration) bool {
	r := s.workerRuntime
	state := s.states.Get(ent)
	if r == nil || s.stopping.Load() || r.preparing >= workerDispatchCapacity {
		return false
	}
	typeKey := workerAdmissionKey{id: key.JobType.JobTypeID, uid: key.JobType.JobTypeUID}
	if !r.admission.ready(typeKey, time.Now()) {
		return false
	}
	p, ok := s.managed[state.MonitorID]
	if !ok {
		s.fail(persistence.ErrWorkerExecutionInvalid)
		return false
	}
	// Charge before copying the dependency closure. No descriptors, credential
	// aliases, clients or ECS pointers cross the dispatch channel.
	cost := 2048
	for _, condition := range p.guard.Conditions {
		cost += 128 + len(condition.Key.Kind) + len(condition.Key.ID) + len(condition.UID) + len(condition.Revision)
	}
	if cost > workerDispatchBytes-r.bytes {
		return false
	}
	in := persistence.WorkerExecutionIntent{MonitorID: state.MonitorID, MonitorUID: p.uid,
		MonitorRevision: state.Revision, ControlRevision: s.controls.Get(ent).Revision,
		Category: key.JobType.Category, ActionID: actionID, Scheduled: scheduled,
		Source: persistence.CatalogKey{Kind: key.SourceKind, ID: key.SourceID}, SourceUID: key.SourceUID, SourceRevision: key.SourceRevision,
		JobType: persistence.JobTypeReference{JobTypeID: key.JobType.JobTypeID, JobTypeUID: key.JobType.JobTypeUID,
			Version: key.JobType.Version, Revision: key.JobType.Revision, Category: key.JobType.Category}, Guard: p.guard.Clone()}
	if actionID == "" {
		in.Generation = state.PulseGeneration + 1
		ticket := s.checks[ent.ID()].workerSchedule
		in.Schedule = &ticket
	}
	token := uuid.New()
	request := workerDispatchRequest{token: token, intent: in, timeout: timeout}
	select {
	case r.dispatcher.requests <- request:
	default:
		return false
	}
	r.pending[token] = &workerPending{typeKey: typeKey, token: token, monitorID: in.MonitorID, uid: in.MonitorUID,
		revision: in.MonitorRevision, actionID: actionID, scheduled: scheduled, preparing: true, bytes: cost}
	r.preparing++
	r.bytes += cost
	if actionID == "" {
		r.pending[token].schedule = *in.Schedule
		s.checks[ent.ID()].workerSchedule = persistence.WorkerScheduleTicket{}
		s.pending[in.MonitorID], s.pendingCheckID[in.MonitorID] = true, token
		s.checks[ent.ID()].ready = 0
		state.SetPulsePending(true)
		s.publishCheckAdmission(ent)
	} else {
		s.admitted[actionID] = true
	}
	return true
}

func (s *DurableSystem) dispatchWorkerCheck(ent ecs.Entity) bool {
	if !s.workerCheck(ent) {
		return false
	}
	j, cfg := s.jobs.Get(ent), s.configs.Get(ent)
	if s.checks[ent.ID()].workerSchedule.Sequence == 0 && !s.expectCheck(ent, time.Unix(0, s.checks[ent.ID()].ready)) {
		return true
	}
	if !s.queueWorker(ent, j.ExternalJobs.Check.Key, "", time.Unix(0, s.checks[ent.ID()].ready), cfg.Timeout) {
		s.enqueueCheck(ent)
	}
	return true
}

func (s *DurableSystem) dispatchWorkerAction(ent ecs.Entity, action persistence.Action) bool {
	j := s.jobs.Get(ent)
	if j == nil || j.ExternalJobs == nil {
		return false
	}
	binding := j.ExternalJobs.Recovery
	if action.Kind == "code" {
		binding = nil
		if slots := j.ExternalJobs.Notifications[action.Color]; action.Endpoint >= 0 && action.Endpoint < len(slots) {
			binding = slots[action.Endpoint]
		}
	}
	if binding == nil {
		return false
	}
	s.queueWorker(ent, binding.Key, action.ID, action.NotBefore, 24*time.Hour)
	return true
}

func (s *DurableSystem) receiveWorkerDispatch() {
	r := s.workerRuntime
	if r == nil {
		return
	}
	for range workerDispatchCapacity {
		select {
		case result := <-r.dispatcher.results:
			p := r.pending[result.token]
			if p == nil || !p.preparing {
				s.fail(persistence.ErrWorkerExecutionConflict)
				return
			}
			p.preparing = false
			r.bytes -= p.bytes
			p.bytes = 0
			p.executionID, p.err = result.state.ID, result.err
			r.admission.observe(p.typeKey, result.err, time.Now())
			p.finished = result.err != nil || result.state.Terminal
			if result.err == nil && (result.state.ID == "" || result.state.MonitorID != p.monitorID || result.state.MonitorUID != p.uid || result.state.ActionID != p.actionID) {
				s.fail(persistence.ErrWorkerExecutionConflict)
				return
			}
			if result.err == nil && p.actionID == "" && result.state.Schedule != p.schedule {
				// Reconciliation adopted an older active assignment. This newly due
				// slot did not create another check and must not disappear from SLOs.
				s.abandonWorkerSchedule(&p.schedule)
				if ent, ok := s.entities[p.monitorID]; ok && s.world.Alive(ent) && s.pendingCheckID[p.monitorID] == p.token {
					s.states.Get(ent).MissedChecks++
					s.publishCheckAdmission(ent)
				}
			}
			if result.err == nil {
				p.schedule = persistence.WorkerScheduleTicket{}
			}
			r.completed = append(r.completed, p.token)
		default:
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Expiry and rejected preparation need no execution-observation slot. Finish
	// them before the rotation so they cannot wait behind slow remote handlers.
	if err := s.finishWorkerPreparations(ctx); err != nil {
		s.recoveryError(err, ctx)
		return
	}
	// Rotation bounds each tick and prevents an early slow execution from
	// starving later completions. These reads copy only scalar status metadata.
	for visits := min(16, len(r.order)); visits > 0 && len(r.order) > 0; visits-- {
		r.cursor %= len(r.order)
		token := r.order[r.cursor]
		p := r.pending[token]
		if !p.preparing && !p.finished {
			status, exists, err := s.store.WorkerDispatchStatus(ctx, p.executionID)
			if err != nil {
				s.recoveryError(err, ctx)
				return
			}
			p.finished = !exists || status.Terminal
			if !exists && p.actionID != "" {
				s.fail(persistence.ErrWorkerExecutionUnavailable)
				return
			}
		}
		if !p.preparing && p.finished {
			if err := s.finishWorkerPending(ctx, p); err != nil {
				s.recoveryError(err, ctx)
				return
			}
			delete(r.pending, token)
			delete(r.observations, p.executionID)
			if r.cursor < len(r.order)-1 {
				moved := r.order[len(r.order)-1]
				r.order[r.cursor] = moved
				r.observations[r.pending[moved].executionID] = r.cursor
			}
			r.order = r.order[:len(r.order)-1]
		} else {
			r.cursor++
		}
	}
	// Retired checks may have held every observation slot at the start of this
	// tick. Transfer waiting admissions into the slots just released, retaining
	// their original identities and preparation capacity until that handoff.
	if err := s.finishWorkerPreparations(ctx); err != nil {
		s.recoveryError(err, ctx)
	}
}

func (s *DurableSystem) finishWorkerPreparations(ctx context.Context) error {
	r := s.workerRuntime
	for i := 0; i < len(r.completed); {
		token := r.completed[i]
		p := r.pending[token]
		if p.finished {
			if err := s.finishWorkerPending(ctx, p); err != nil {
				return err
			}
			delete(r.pending, token)
		} else if p.actionID == "" && s.pendingCheckID[p.monitorID] != token {
			// This confirmed admission belongs to an obsolete owner projection.
			// The store still owns execution/finalization; a later preparation must
			// retain the current monitor token even if this reply arrives last.
			delete(r.pending, token)
		} else if position, exists := r.observations[p.executionID]; exists {
			previousToken := r.order[position]
			previous := r.pending[previousToken]
			if previous.monitorID != p.monitorID || previous.uid != p.uid || previous.actionID != p.actionID {
				return persistence.ErrWorkerExecutionConflict
			}
			if p.actionID == "" {
				// Metadata-only replacement can adopt the same committed check.
				// Transfer its one observation to the current correlation token.
				r.order[position] = token
				delete(r.pending, previousToken)
			} else {
				delete(r.pending, token)
			}
		} else if len(r.order) < persistence.MaxWorkerExecutions {
			r.observations[p.executionID] = len(r.order)
			r.order = append(r.order, token)
		} else {
			// Continue past a waiting admission: a later cancellation must still
			// release its preparation slot while observation capacity is full.
			i++
			continue
		}
		r.preparing--
		r.completed[i] = r.completed[len(r.completed)-1]
		r.completed = r.completed[:len(r.completed)-1]
	}
	return nil
}

func (s *DurableSystem) finishWorkerPending(ctx context.Context, p *workerPending) error {
	if p.err != nil && !workerDispatchRetryable(p.err) {
		s.abandonWorkerSchedule(&p.schedule)
		return p.err
	}
	var m persistence.Monitor
	ent, present := s.entities[p.monitorID]
	present = present && s.world.Alive(ent)
	if present {
		var ok bool
		var err error
		m, ok, err = s.store.GetContext(ctx, p.monitorID)
		if err != nil {
			return err
		}
		present = ok && !m.Removed && m.CatalogUID == p.uid && m.Revision == s.states.Get(ent).Revision
	}
	if p.actionID == "" {
		if s.pendingCheckID[p.monitorID] == p.token {
			delete(s.pending, p.monitorID)
			delete(s.pendingCheckID, p.monitorID)
			if present && p.revision == m.Revision && p.err != nil {
				if errors.Is(p.err, persistence.ErrWorkerExecutionExpired) {
					s.states.Get(ent).MissedChecks++
				} else if !s.stopping.Load() && s.disabled.Get(ent) == nil && !s.isSnoozed(ent) {
					s.checks[ent.ID()].ready = p.scheduled.UnixNano()
					s.checks[ent.ID()].workerSchedule = p.schedule
					p.schedule = persistence.WorkerScheduleTicket{}
					s.enqueueCheck(ent)
				}
			}
		}
		s.abandonWorkerSchedule(&p.schedule)
	} else {
		delete(s.admitted, p.actionID)
	}
	if present {
		s.project(ent, m)
		s.scheduleActions(ent, m)
		if s.index != nil {
			s.index.Put(s.summary(ent, m))
		}
	}
	return nil
}

func workerDispatchRetryable(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		persistence.IsLeadershipUnavailable(err) || errors.Is(err, persistence.ErrCommitUnconfirmed) ||
		errors.Is(err, persistence.ErrCatalogConflict) || errors.Is(err, persistence.ErrCatalogDependency) ||
		errors.Is(err, persistence.ErrWorkerExecutionConflict) || errors.Is(err, persistence.ErrWorkerExecutionQuota) ||
		errors.Is(err, persistence.ErrWorkerExecutionExpired) || errors.Is(err, persistence.ErrWorkerCheckExpired) ||
		errors.Is(err, persistence.ErrControlConflict)
}
