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
		config := *(*w.Mappers.PulseConfig.Get(ent)).Copy()
		status := *(*w.Mappers.PulseStatus.Get(ent)).Copy()

		// first‑time check?
		if w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			continue
		}

		// interval check
		if time.Since(status.LastCheckTime) >= config.Interval {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
		}
	}
	return toCheck
}

func (s *PulseScheduleSystem) applyWork(w *controller.CPRaWorld, entities []ecs.Entity) []func() {
	deferred := make([]func(), 0, len(entities))
	for _, ent := range entities {
		e := ent
		deferred = append(deferred, func() {
			if !e.IsZero() && !w.Mappers.World.Has(e, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
				w.Mappers.PulseNeeded.Assign(e, &components.PulseNeeded{})
			}
		})
	}
	return deferred
}

func (s *PulseScheduleSystem) Update(w *controller.CPRaWorld) []func() {
	return s.applyWork(w, s.collectWork(w))
}

/* -----------------------------  DISPATCH  ----------------------------- */

type dispatchablePulse struct {
	Entity ecs.Entity
	Job    jobs.Job
	Status components.PulseStatus // updated LastCheckTime copy
}

type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded *generic.Filter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]
}

func (s *PulseDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseNeeded = generic.NewFilter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]()
}

func (s *PulseDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchablePulse {
	out := make([]dispatchablePulse, 0)
	query := s.PulseNeeded.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		job := w.Mappers.PulseJob.Get(ent).Job.Copy()

		stCopy := *(*w.Mappers.PulseStatus.Get(ent)).Copy()
		stCopy.LastCheckTime = time.Now()

		out = append(out, dispatchablePulse{Entity: ent, Job: job, Status: stCopy})
	}
	return out
}

func (s *PulseDispatchSystem) applyWork(w *controller.CPRaWorld, list []dispatchablePulse) []func() {
	deferred := make([]func(), 0, len(list))

	for _, item := range list {
		select {
		case s.JobChan <- item.Job:
			e := item.Entity
			st := item.Status // capture

			deferred = append(deferred, func() {
				// write updated status
				mapper := generic.NewMap[components.PulseStatus](w.Mappers.World)
				p := new(components.PulseStatus)
				*p = st
				mapper.Set(e, p)
			})

			// first‑check removal (if present)
			if w.Mappers.World.Has(e, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				e1 := e
				deferred = append(deferred, func() { w.Mappers.PulseFirstCheck.Remove(e1) })
			}

			// exchange PulseNeeded -> PulsePending
			e2 := e
			deferred = append(deferred, func() {
				w.Mappers.World.Exchange(
					e2,
					[]ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)},
					[]ecs.ID{ecs.ComponentID[components.PulseNeeded](w.Mappers.World)},
				)
			})

			name := string([]byte(*w.Mappers.Name.Get(e)))
			log.Printf("sent %s job\n", name)

		default:
			log.Printf("Job channel full, skipping dispatch for entity %v", item.Entity)
		}
	}
	return deferred
}

func (s *PulseDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	return s.applyWork(w, s.collectWork(w))
}

/* -----------------------------  RESULT  ----------------------------- */

type resultEntry struct {
	entity ecs.Entity
	result jobs.Result
}

type PulseResultSystem struct {
	PendingPulseFilter *generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
}

func (s *PulseResultSystem) Initialize(w *controller.CPRaWorld) {}

func (s *PulseResultSystem) collectResults() map[ecs.Entity]resultEntry {
	out := make(map[ecs.Entity]resultEntry)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			out[ent] = resultEntry{entity: ent, result: res}
		default:
			break loop
		}
	}
	return out
}

func (s *PulseResultSystem) processResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results map[ecs.Entity]resultEntry) []func() {
	deferred := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.PulsePending](w.Mappers.World)) {
			continue
		}

		name := string([]byte(*w.Mappers.Name.Get(entity)))
		fmt.Printf("entity is %v for %s pulse result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			config := *(*w.Mappers.PulseConfig.Get(entity)).Copy()
			statusCopy := *(*w.Mappers.PulseStatus.Get(entity)).Copy()
			monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			monitorCopy.Status = "failed"

			// deferred data writes
			deferred = append(deferred, func(e ecs.Entity, ps components.PulseStatus, ms components.MonitorStatus) func() {
				return func() {
					pulseMapper := generic.NewMap[components.PulseStatus](w.Mappers.World)
					pulseMapper.Set(e, &ps)
					monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					monitorMapper.Set(e, &ms)
				}
			}(entity, statusCopy, monitorCopy))

			// yellow code on first failure
			if statusCopy.ConsecutiveFailures == 1 &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				e := entity
				deferred = append(deferred, func() { w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "yellow"}) })
			}

			// interventions
			if config.MaxFailures <= statusCopy.ConsecutiveFailures &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) {
				log.Printf("Monitor %s failed %d times and needs intervention\n", name, statusCopy.ConsecutiveFailures)
				e := entity
				deferred = append(deferred, func() { w.Mappers.InterventionNeeded.Assign(e, &components.InterventionNeeded{}) })
			}

		} else {
			// ---- SUCCESS ----
			statusCopy := *(*w.Mappers.PulseStatus.Get(entity)).Copy()
			monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			monitorCopy.Status = "success"

			deferred = append(deferred, func(e ecs.Entity, ps components.PulseStatus, ms components.MonitorStatus) func() {
				return func() {
					pulseMapper := generic.NewMap[components.PulseStatus](w.Mappers.World)
					pulseMapper.Set(e, &ps)
					monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					monitorMapper.Set(e, &ms)
				}
			}(entity, statusCopy, monitorCopy))
		}

		// always remove PulsePending
		e := entity
		deferred = append(deferred, func() { w.Mappers.PulsePending.Remove(e) })
	}
	return deferred
}

func (s *PulseResultSystem) Update(w *controller.CPRaWorld) []func() {
	return s.processResultsAndQueueStructuralChanges(w, s.collectResults())
}
