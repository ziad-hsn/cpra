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

/* ---------------------------  DISPATCH  --------------------------- */

type dispatchableIntervention struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter2[components.InterventionJob, components.InterventionNeeded]
}

func (s *InterventionDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.InterventionNeededFilter = *generic.
		NewFilter2[components.InterventionJob, components.InterventionNeeded]().
		Without(generic.T[components.InterventionPending]())
}

func (s *InterventionDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableIntervention {
	out := make([]dispatchableIntervention, 0)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		job := w.Mappers.InterventionJob.GetUnchecked(ent).Job.Copy()
		out = append(out, dispatchableIntervention{Entity: ent, Job: job})
	}
	return out
}

func (s *InterventionDispatchSystem) applyWork(w *controller.CPRaWorld, list []dispatchableIntervention) []func() {
	deferred := make([]func(), 0, len(list))

	for _, item := range list {
		select {
		case s.JobChan <- item.Job:
			e := item.Entity
			deferred = append(deferred, func() {
				if !e.IsZero() {
					w.Mappers.World.Exchange(
						e,
						[]ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)},
						[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)},
					)
				}
			})
		default:
			log.Printf("Job channel full for entity %v\n", item.Entity)
		}
	}
	return deferred
}

func (s *InterventionDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	return s.applyWork(w, s.collectWork(w))
}

/* ---------------------------  RESULT  --------------------------- */

type InterventionResultSystem struct {
	ResultChan <-chan jobs.Result
}

func (s *InterventionResultSystem) Initialize(w *controller.CPRaWorld) {}

func (s *InterventionResultSystem) collectInterventionResults() map[ecs.Entity]resultEntry {
	out := make(map[ecs.Entity]resultEntry)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			out[res.Entity()] = resultEntry{entity: res.Entity(), result: res}
		default:
			break loop
		}
	}
	return out
}

func (s *InterventionResultSystem) processInterventionResultsAndQueueStructuralChanges(
	w *controller.CPRaWorld, results map[ecs.Entity]resultEntry,
) []func() {

	deferred := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result

		if !w.IsAlive(entity) || !w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.InterventionPending](w.Mappers.World)) {
			continue
		}

		name := string([]byte(*w.Mappers.Name.GetUnchecked(entity)))
		fmt.Printf("entity is %v for %s intervention result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			config := *w.Mappers.InterventionConfig.GetUnchecked(entity).Copy()
			statusCopy := *w.Mappers.InterventionStatus.GetUnchecked(entity).Copy()
			monitorCopy := *w.Mappers.MonitorStatus.Get(entity).Copy()

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			monitorCopy.Status = "failed"

			deferred = append(deferred, func(e ecs.Entity, s components.InterventionStatus, m components.MonitorStatus) func() {
				return func() {
					interventionMapper := generic.NewMap[components.InterventionStatus](w.Mappers.World)
					interventionMapper.Set(e, &s)
					monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					monitorMapper.Set(e, &m)
				}
			}(entity, statusCopy, monitorCopy))

			if config.MaxFailures <= statusCopy.ConsecutiveFailures {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					log.Printf("Monitor %s intervention failed\n", name)

					e := entity
					deferred = append(deferred, func() { w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "red"}) })
				}
				// No retry when max failures reached.
			} else {
				// Schedule retry.
				e := entity
				deferred = append(deferred, func() { w.Mappers.InterventionNeeded.Assign(e, &components.InterventionNeeded{}) })
			}

		} else {
			// ---- SUCCESS ----
			statusCopy := *w.Mappers.InterventionStatus.Get(entity).Copy()
			monitorCopy := *w.Mappers.MonitorStatus.Get(entity).Copy()
			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			monitorCopy.Status = "success"

			deferred = append(deferred, func(e ecs.Entity, s components.InterventionStatus, m components.MonitorStatus) func() {
				return func() {
					interventionMapper := generic.NewMap[components.InterventionStatus](w.Mappers.World)
					interventionMapper.Set(e, &s)
					monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					monitorMapper.Set(e, &m)
				}
			}(entity, statusCopy, monitorCopy))

			if lastStatus == "failed" &&
				w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {

				log.Printf("Monitor %s intervention succeeded and needs cyan code\n", name)
				e := entity
				deferred = append(deferred, func() { w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "cyan"}) })
			}
		}

		// Always remove pending after processing.
		e := entity
		deferred = append(deferred, func() { w.Mappers.InterventionPending.Remove(e) })
	}
	return deferred
}

func (s *InterventionResultSystem) Update(w *controller.CPRaWorld) []func() {
	return s.processInterventionResultsAndQueueStructuralChanges(w, s.collectInterventionResults())
}

/* ------------------  Utility: dump component names  ------------------ */

func GetEntityComponents(w *ecs.World, entity ecs.Entity) []string {
	ids := w.Ids(entity)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		info, _ := ecs.ComponentInfo(w, id)
		out = append(out, info.Type.Name())
	}
	return out
}
