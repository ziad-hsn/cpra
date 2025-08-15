package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/ark/ecs"
	"log"
	"strings"
	"time"
)

/* -----------------------------  SCHEDULE  ----------------------------- */

type PulseScheduleSystem struct {
	PulseFilter *ecs.Filter2[components.PulseConfig, components.PulseStatus]
	Mappers     *entities.EntityManager
}

func (s *PulseScheduleSystem) Initialize(w *ecs.World) {
	s.PulseFilter = (*ecs.Filter2[components.PulseConfig, components.PulseStatus])(ecs.NewFilter2[components.PulseConfig, components.PulseNeeded](w)).Without(ecs.C[components.DisabledMonitor]()).
		Without(ecs.C[components.PulsePending]()).
		Without(ecs.C[components.InterventionNeeded]()).
		Without(ecs.C[components.InterventionPending]()).
		Without(ecs.C[components.CodeNeeded]()).
		Without(ecs.C[components.CodePending]())
	//s.Mappers = entities.InitializeMappers(w)
}

func (s *PulseScheduleSystem) collectWork(w *ecs.World) []ecs.Entity {
	fmt.Println("started")
	fmt.Println(w.Stats())
	var toCheck []ecs.Entity
	query := s.PulseFilter.Query()

	for query.Next() {
		ent := query.Entity()
		interval := s.Mappers.PulseConfig.Get(ent).Interval
		lastCheckTime := s.Mappers.PulseStatus.Get(ent).LastCheckTime

		// first‑time check?
		if s.Mappers.PulseFirstCheck.HasAll(ent) {
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

func (s *PulseScheduleSystem) applyWork(w *ecs.World, entities []ecs.Entity) {
	for _, ent := range entities {

		if s.Mappers.World.Alive(ent) && !s.Mappers.PulseNeeded.HasAll(ent) {
			s.Mappers.PulseNeeded.Set(ent, &components.PulseNeeded{})
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
	JobChan     chan<- jobs.Job
	PulseNeeded *ecs.Filter1[components.PulseNeeded]
}

func (s *PulseDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseNeeded = ecs.NewFilter1[components.PulseNeeded](w.Mappers.World)
}

func (s *PulseDispatchSystem) collectWork(w *controller.CPRaWorld) map[ecs.Entity]dispatchablePulse {
	out := make(map[ecs.Entity]dispatchablePulse)
	query := s.PulseNeeded.Query()

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
			if w.Mappers.PulseFirstCheck.HasAll(e) {
				commandBuffer.removeFirstCheck(e)
			}

			commandBuffer.MarkPulsePending(e)

			// exchange PulseNeeded -> PulsePending

			name := strings.Clone(string(*w.Mappers.Name.Get(e)))
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

func (s *PulseDispatchSystem) Finalize(w *controller.CPRaWorld) {}

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

		if !w.Mappers.World.Alive(entity) || !w.Mappers.PulsePending.HasAll(entity) {
			continue
		}

		name := strings.Clone(string(*w.Mappers.Name.Get(entity)))

		//fmt.Printf("entity is %v for %s pulse result.\n", entity, name)

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
				w.Mappers.YellowCode.HasAll(entity) {
				commandBuffer.scheduleCode(entity, "yellow")
			}

			// interventions
			if statusCopy.ConsecutiveFailures%maxFailures == 0 &&
				w.Mappers.InterventionConfig.HasAll(entity) {
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

func (s *PulseResultSystem) Finalize(w *controller.CPRaWorld) {}
