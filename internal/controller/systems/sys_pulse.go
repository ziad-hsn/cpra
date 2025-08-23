package systems

import (
	"context"
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"

	"github.com/mlange-42/ark/ecs"
	"time"
)

/* -----------------------------  SCHEDULE  ----------------------------- */

type PulseScheduleSystem struct {
	PulseFilter *ecs.Filter2[components.PulseConfig, components.PulseStatus]
	Mapper      *entities.EntityManager
}

func (s *PulseScheduleSystem) Initialize(w *ecs.World) {
	s.PulseFilter = ecs.NewFilter2[components.PulseConfig, components.PulseStatus](w).
		Without(ecs.C[components.DisabledMonitor]()).
		Without(ecs.C[components.PulseNeeded]()).
		Without(ecs.C[components.PulsePending]()).
		Without(ecs.C[components.InterventionNeeded]()).
		Without(ecs.C[components.InterventionPending]()).
		Without(ecs.C[components.CodeNeeded]()).
		Without(ecs.C[components.CodePending]())
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseScheduleSystem) collectWork(w *ecs.World) []ecs.Entity {
	start := time.Now()
	
	// Start tracing for this operation
	ctx := context.Background()
	ctx, span := controller.SchedulerLogger.StartTrace(ctx, "pulse_schedule_collect")
	defer func() {
		controller.SchedulerLogger.FinishTrace(span, nil)
	}()
	
	var toCheck []ecs.Entity
	query := s.PulseFilter.Query()

	for query.Next() {
		ent := query.Entity()
		interval := s.Mapper.PulseConfig.Get(ent).Interval
		lastCheckTime := s.Mapper.PulseStatus.Get(ent).LastCheckTime
		timeSinceLast := time.Since(lastCheckTime)

		// first‑time check?
		if s.Mapper.PulseFirstCheck.HasAll(ent) {
			toCheck = append(toCheck, ent)
			controller.SchedulerLogger.SetTraceEntity(span, uint64(ent.ID()))
			controller.SchedulerLogger.AddTraceTag(span, "check_type", "first_check")
			controller.SchedulerLogger.Debug("Entity[%d] first check scheduled (age: %v, interval: %v)",
				ent.ID(), timeSinceLast, interval)
			continue
		}

		// interval check
		if timeSinceLast >= interval {
			toCheck = append(toCheck, ent)
			controller.SchedulerLogger.Debug("Entity[%d] interval check scheduled (age: %v, interval: %v)",
				ent.ID(), timeSinceLast, interval)
		}
	}

	controller.SchedulerLogger.LogSystemPerformance("PulseScheduler", time.Since(start), len(toCheck))
	return toCheck
}

func (s *PulseScheduleSystem) applyWork(w *ecs.World, entities []ecs.Entity) {
	for _, ent := range entities {
		if w.Alive(ent) && !s.Mapper.PulseNeeded.HasAll(ent) {
			// Update lastCheckTime immediately when scheduling to prevent race conditions
			pulseStatusPtr := s.Mapper.PulseStatus.Get(ent)
			if pulseStatusPtr != nil {
				pulseStatusPtr.LastCheckTime = time.Now()
				controller.SchedulerLogger.LogComponentState(ent.ID(), "PulseStatus", "LastCheckTime updated")
			}

			// Remove first-check flag if present
			if s.Mapper.PulseFirstCheck.HasAll(ent) {
				s.Mapper.PulseFirstCheck.Remove(ent)
				controller.SchedulerLogger.LogComponentState(ent.ID(), "PulseFirstCheck", "removed")
			}

			s.Mapper.PulseNeeded.Add(ent, &components.PulseNeeded{})
			controller.SchedulerLogger.LogComponentState(ent.ID(), "PulseNeeded", "added")
		}
	}
}

func (s *PulseScheduleSystem) Update(w *ecs.World) {
	toCheck := s.collectWork(w)
	s.applyWork(w, toCheck)
}

func (s *PulseScheduleSystem) Finalize(w *ecs.World) {}

/* -----------------------------  DISPATCH  ----------------------------- */

type dispatchablePulse struct {
	Job    jobs.Job
	Status components.PulseStatus // updated LastCheckTime copy
}

type PulseDispatchSystem struct {
	JobChan      chan<- jobs.Job
	PulseNeeded  *ecs.Filter1[components.PulseNeeded]
	Mapper       *entities.EntityManager
	QueueManager *queue.QueueManager
}

func (s *PulseDispatchSystem) Initialize(w *ecs.World) {
	s.PulseNeeded = ecs.NewFilter1[components.PulseNeeded](w)
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseDispatchSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchablePulse {
	// Start tracing for dispatch collection
	ctx := context.Background()
	ctx, span := controller.DispatchLogger.StartTrace(ctx, "pulse_dispatch_collect")
	defer func() {
		controller.DispatchLogger.FinishTrace(span, nil)
	}()
	
	out := make(map[ecs.Entity]dispatchablePulse)
	query := s.PulseNeeded.Query()

	for query.Next() {
		ent := query.Entity()
		job := s.Mapper.PulseJob.Get(ent).Job
		
		// Add entity to trace
		controller.DispatchLogger.SetTraceEntity(span, uint64(ent.ID()))
		controller.DispatchLogger.AddTraceTag(span, "job_type", "pulse")

		out[ent] = dispatchablePulse{Job: job}
	}
	return out
}

func (s *PulseDispatchSystem) applyWork(w *ecs.World, list map[ecs.Entity]dispatchablePulse) {

	for e, item := range list {
		// Prevent component duplication
		if s.Mapper.PulsePending.HasAll(e) {
			namePtr := s.Mapper.Name.Get(e)
			if namePtr != nil {
				controller.DispatchLogger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, e.ID())
			}
			continue
		}

		err := s.QueueManager.EnqueuePulse(e, item.Job)

		if err != nil {
			controller.DispatchLogger.Warn("Failed to enqueue pulse for entity %d: %v", e.ID(), err)
			continue
		}

		// Safe component transition (lastCheckTime already updated in schedule system)
		if s.Mapper.PulseNeeded.HasAll(e) {
			s.Mapper.PulseNeeded.Remove(e)
			s.Mapper.PulsePending.Add(e, &components.PulsePending{})

			namePtr := s.Mapper.Name.Get(e)
			if namePtr != nil {
				controller.DispatchLogger.Debug("Dispatched %s job for entity: %d", *namePtr, e.ID())
			}

			controller.DispatchLogger.LogComponentState(e.ID(), "PulseNeeded->PulsePending", "transitioned")
		}

	}
}

func (s *PulseDispatchSystem) Update(w *ecs.World) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch)
}

func (s *PulseDispatchSystem) Finalize(w *ecs.World) {
	//close(s.JobChan)
}

/* -----------------------------  RESULT  ----------------------------- */

type PulseResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
}

func (s *PulseResultSystem) Initialize(w *ecs.World) {
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseResultSystem) collectResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			out[ent] = res
		default:
			break loop
		}
	}
	return out
}

func (s *PulseResultSystem) processResultsAndQueueStructuralChanges(w *ecs.World, results map[ecs.Entity]jobs.Result) {

	for _, res := range results {
		entity := res.Entity()

		if !w.Alive(entity) || !s.Mapper.PulsePending.HasAll(entity) {
			continue
		}

		// Safe component access with nil checks
		namePtr := s.Mapper.Name.Get(entity)
		if namePtr == nil {
			controller.ResultLogger.Warn("Entity %d has nil name component", entity.ID())
			continue
		}
		name := *namePtr

		// Validate required components exist
		if s.Mapper.PulseConfig.Get(entity) == nil ||
			s.Mapper.PulseStatus.Get(entity) == nil ||
			s.Mapper.MonitorStatus.Get(entity) == nil {
			controller.ResultLogger.Warn("Entity %d (%s) missing required components", entity.ID(), name)
			continue
		}

		//fmt.Printf("entity is %v for %s pulse result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			maxFailures := s.Mapper.PulseConfig.Get(entity).MaxFailures
			statusCopy := s.Mapper.PulseStatus.Get(entity)
			monitorCopy := s.Mapper.MonitorStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++

			controller.ResultLogger.Debug("Monitor %s failed (attempt %d/%d): %v",
				name, statusCopy.ConsecutiveFailures, maxFailures, res.Error())

			// yellow code on first failure
			if statusCopy.ConsecutiveFailures == 1 &&
				s.Mapper.YellowCode.HasAll(entity) {
				controller.ResultLogger.Info("Monitor %s triggering yellow code on first failure", name)
				if !s.Mapper.CodeNeeded.HasAll(entity) {
					s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "yellow"})
					controller.ResultLogger.LogComponentState(entity.ID(), "CodeNeeded", "yellow added")
				}
			}

			// interventions
			if statusCopy.ConsecutiveFailures%maxFailures == 0 &&
				s.Mapper.InterventionConfig.HasAll(entity) {
				controller.ResultLogger.Warn("Monitor %s failed %d times, triggering intervention", name, statusCopy.ConsecutiveFailures)
				s.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
				monitorCopy.Status = "failed"
				controller.ResultLogger.LogComponentState(entity.ID(), "InterventionNeeded", "added")
			}

			// deferred data writes
			//s.Mapper.PulseStatus.Set(entity, &statusCopy)
			//s.Mapper.MonitorStatus.Set(entity, &monitorCopy)

		} else {
			// ---- SUCCESS ----
			statusCopy := s.Mapper.PulseStatus.Get(entity)
			monitorCopy := s.Mapper.MonitorStatus.Get(entity)

			lastStatus := statusCopy.LastStatus
			wasFailure := statusCopy.ConsecutiveFailures > 0

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			monitorCopy.Status = "success"

			if wasFailure {
				controller.ResultLogger.Info("Monitor %s recovered after failure", name)
			} else {
				controller.ResultLogger.Debug("Monitor %s pulse successful", name)
			}

			// Trigger green code for recovery
			if lastStatus != "success" && lastStatus != "" {
				if !s.Mapper.CodeNeeded.HasAll(entity) {
					s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "green"})
					controller.ResultLogger.Info("Monitor %s triggering green code for recovery", name)
					controller.ResultLogger.LogComponentState(entity.ID(), "CodeNeeded", "green added")
				}
			}
		}

		// always remove PulsePending
		s.Mapper.PulsePending.Remove(entity)
		controller.ResultLogger.LogComponentState(entity.ID(), "PulsePending", "removed")
	}
}

func (s *PulseResultSystem) Update(w *ecs.World) {
	results := s.collectResults()
	s.processResultsAndQueueStructuralChanges(w, results)
}

func (s *PulseResultSystem) Finalize(w *ecs.World) {

}
