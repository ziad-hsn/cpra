package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"log"
	"time"
)

/* -----------------------------  SCHEDULE  ----------------------------- */

type PulseScheduleSystem struct {
	PulseFilter *generic.Filter2[components.PulseConfig, components.PulseStatus]
}

func (s *PulseScheduleSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
		Without(generic.T[components.DisabledMonitor]()).
		Without(generic.T[components.PulsePending]()).
		Without(generic.T[components.InterventionNeeded]()).
		Without(generic.T[components.InterventionPending]()).
		Without(generic.T[components.CodeNeeded]()).
		Without(generic.T[components.CodePending]())
}

func (s *PulseScheduleSystem) collectWork(w *controller.CPRaWorld) []ecs.Entity {
	var toCheck []ecs.Entity
	query := s.PulseFilter.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		interval := w.Mappers.PulseConfig.Get(ent).Interval
		lastCheckTime := w.Mappers.PulseStatus.Get(ent).LastCheckTime

		// first‑time check?
		if w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(lastCheckTime), interval)
			continue
		}

		// interval check
		if time.Since(lastCheckTime) >= interval {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(lastCheckTime), interval)
		}
	}
	return toCheck
}

func (s *PulseScheduleSystem) applyWork(w *controller.CPRaWorld, entities []ecs.Entity, commandBuffer *CommandBufferSystem) {
	for _, ent := range entities {

		if w.IsAlive(ent) && !w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
			commandBuffer.schedulePulse(ent)
		}
	}
}

func (s *PulseScheduleSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	toCheck := s.collectWork(w)
	s.applyWork(w, toCheck, cb)
}

/* -----------------------------  DISPATCH  ----------------------------- */

type dispatchablePulse struct {
	Job    jobs.Job
	Status components.PulseStatus // updated LastCheckTime copy
}

type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded *generic.Filter1[components.PulseNeeded]
}

func (s *PulseDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseNeeded = generic.NewFilter1[components.PulseNeeded]()
}

func (s *PulseDispatchSystem) collectWork(w *controller.CPRaWorld) map[ecs.Entity]dispatchablePulse {
	out := make(map[ecs.Entity]dispatchablePulse)
	query := s.PulseNeeded.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		job := w.Mappers.PulseJob.Get(ent).Job

		stCopy := *w.Mappers.PulseStatus.Get(ent)
		stCopy.LastCheckTime = time.Now()

		out[ent] = dispatchablePulse{Job: job, Status: stCopy}
	}
	return out
}

func (s *PulseDispatchSystem) applyWork(w *controller.CPRaWorld, list map[ecs.Entity]dispatchablePulse, commandBuffer *CommandBufferSystem) {

	for e, item := range list {
		select {
		case s.JobChan <- item.Job:

			commandBuffer.SetPulseStatus(e, item.Status)

			// first‑check removal (if present)
			if w.Mappers.World.Has(e, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				commandBuffer.removeFirstCheck(e)
			}

			commandBuffer.MarkPulsePending(e)

			// exchange PulseNeeded -> PulsePending

			name := string([]byte(*w.Mappers.Name.Get(e)))
			log.Printf("sent %s job\n", name)

		default:
			log.Printf("Job channel full, skipping dispatch for entity %v", e)
		}
	}
}

func (s *PulseDispatchSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch, cb)
}

/* -----------------------------  RESULT  ----------------------------- */

type PulseResultSystem struct {
	ResultChan <-chan jobs.Result
}

func (s *PulseResultSystem) Initialize(w *controller.CPRaWorld) {}

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

func (s *PulseResultSystem) processResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results map[ecs.Entity]jobs.Result, commandBuffer *CommandBufferSystem) {

	for _, res := range results {
		entity := res.Entity()

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.PulsePending](w.Mappers.World)) {
			continue
		}

		name := string([]byte(*w.Mappers.Name.Get(entity)))
		fmt.Printf("entity is %v for %s pulse result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			maxFailures := w.Mappers.PulseConfig.Get(entity).MaxFailures
			statusCopy := *w.Mappers.PulseStatus.Get(entity)
			monitorCopy := *w.Mappers.MonitorStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++

			// yellow code on first failure
			if statusCopy.ConsecutiveFailures == 1 &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				commandBuffer.scheduleCode(entity, "yellow")
			}

			// interventions
			if statusCopy.ConsecutiveFailures%maxFailures == 0 &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) {
				log.Printf("Monitor %s failed %d times and needs intervention\n", name, statusCopy.ConsecutiveFailures)
				commandBuffer.scheduleIntervention(entity)
				monitorCopy.Status = "failed"
			}

			// deferred data writes
			commandBuffer.SetPulseStatus(entity, statusCopy)
			commandBuffer.setMonitorStatus(entity, monitorCopy)

		} else {
			// ---- SUCCESS ----
			statusCopy := *w.Mappers.PulseStatus.Get(entity)
			monitorCopy := *w.Mappers.MonitorStatus.Get(entity)

			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			monitorCopy.Status = "success"

			commandBuffer.SetPulseStatus(entity, statusCopy)
			commandBuffer.setMonitorStatus(entity, monitorCopy)

			if lastStatus != "success" && lastStatus != "" {
				commandBuffer.scheduleCode(entity, "green")
			}
		}

		// always remove PulsePending
		commandBuffer.RemovePulsePending(entity)
	}
}

func (s *PulseResultSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	results := s.collectResults()
	s.processResultsAndQueueStructuralChanges(w, results, cb)
}
