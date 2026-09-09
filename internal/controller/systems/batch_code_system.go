package systems

import (
	"cpra/internal/alerts"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"github.com/mlange-42/ark/ecs"
	"time"
)

type BatchCodeSystem struct {
	queue            queue.Queue
	logger           Logger
	stateLogger      *StateLogger
	world            *ecs.World
	alertMgr         *alerts.Manager
	codeSched        *CodeScheduler
	stateMapper      *ecs.Map1[components.MonitorState]
	codeConfigMapper *ecs.Map1[components.CodeConfig]
	jobStorageMapper *ecs.Map1[components.JobStorage]
	batchSize        int
}

func NewBatchCodeSystem(w *ecs.World, q queue.Queue, sched *CodeScheduler, batch int, logger Logger, sl *StateLogger, mgr *alerts.Manager) *BatchCodeSystem {
	return &BatchCodeSystem{q, logger, sl, w, mgr, sched, ecs.NewMap1[components.MonitorState](w), ecs.NewMap1[components.CodeConfig](w), ecs.NewMap1[components.JobStorage](w), batch}
}
func (s *BatchCodeSystem) Initialize(*ecs.World) {}
func (s *BatchCodeSystem) Finalize(*ecs.World)   {}
func (s *BatchCodeSystem) Update(*ecs.World) {
	now := time.Now()
	budget := s.batchSize
	if budget <= 0 {
		budget = 1000
	}
	for _, ent := range s.codeSched.ConsumeReady(budget) {
		if !s.world.Alive(ent) {
			continue
		}
		state, storage, cfg := s.stateMapper.Get(ent), s.jobStorageMapper.Get(ent), s.codeConfigMapper.Get(ent)
		if state == nil || storage == nil || cfg == nil {
			continue
		}
		// Compatibility with callers that explicitly set a single pending color.
		if len(state.PendingAlerts) == 0 && state.IsCodeNeeded() && state.PendingCode != "" {
			state.PendingAlerts = append(state.PendingAlerts, components.AlertRequest{Color: state.PendingCode})
		}
		waiting := state.PendingAlerts[:0]
		nextDue := time.Time{}
		retryAdmission := false
		for _, request := range state.PendingAlerts {
			color := request.Color
			prototypes := storage.CodeJobs[color]
			if cfg.Configs[color] == nil || !cfg.Configs[color].Dispatch || len(prototypes) == 0 {
				continue
			}
			if schema.InMaintenance(state.Maintenance, now) {
				continue
			}
			if request.NotBefore.After(now) || (state.Deliveries[color] != nil && state.Deliveries[color].Next == len(prototypes)) {
				waiting = append(waiting, request)
				due := request.NotBefore
				if !due.After(now) {
					due = now.Add(time.Second)
				}
				if nextDue.IsZero() || due.Before(nextDue) {
					nextDue = due
				}
				continue
			}
			if state.Deliveries == nil {
				state.Deliveries = make(map[string]*components.CodeDelivery)
			}
			delivery := state.Deliveries[color]
			if delivery == nil {
				state.CodeSequence++
				delivery = &components.CodeDelivery{Generation: state.CodeSequence, Completed: make([]bool, len(prototypes)), Attempts: request.Attempts + 1, Retryable: true, Deadline: now.Add(codeResultTTL)}
				state.Deliveries[color] = delivery
				s.codeSched.active[ent] = struct{}{}
			}
			for delivery.Next < len(prototypes) && budget > 0 {
				n := delivery.Next
				if isNilJob(prototypes[n]) {
					delivery.Completed[n] = true
					delivery.Next++
					delivery.Seen++
					delivery.Failed++
					delivery.Retryable = false
					continue
				}
				job := jobs.NewDispatch(prototypes[n], ent, "code", color, delivery.Generation, n)
				job.Deadline = delivery.Deadline
				if err := s.queue.Enqueue(job); err != nil {
					break
				}
				if delivery.Next == 0 && s.alertMgr != nil {
					s.alertMgr.OnEnqueued(ent, color, now)
				}
				delivery.Next++
				budget--
			}
			if delivery.Next < len(prototypes) {
				waiting = append(waiting, request)
				retryAdmission = true
			}
		}
		state.PendingAlerts = waiting
		state.PendingCode = ""
		if len(waiting) > 0 {
			state.PendingCode = waiting[0].Color
		}
		state.SetCodeNeeded(len(waiting) > 0)
		state.SetCodePending(len(state.Deliveries) > 0)
		if retryAdmission {
			s.codeSched.Requeue([]ecs.Entity{ent})
		} else if !nextDue.IsZero() {
			s.codeSched.ScheduleDeferred(ent, nextDue)
		}
	}
}
func isNilJob(j jobs.Job) bool { return j == nil || j.IsNil() }
