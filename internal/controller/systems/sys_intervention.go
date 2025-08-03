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

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter1[components.InterventionNeeded]
}

func (s *InterventionDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.InterventionNeededFilter = *generic.
		NewFilter1[components.InterventionNeeded]().
		Without(generic.T[components.InterventionPending]())
}

func (s *InterventionDispatchSystem) collectWork(w *controller.CPRaWorld) map[ecs.Entity]jobs.Job {
	out := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		out[ent] = w.Mappers.InterventionJob.Get(ent).Job.Copy()
	}
	return out
}

func (s *InterventionDispatchSystem) applyWork(w *controller.CPRaWorld, jobs map[ecs.Entity]jobs.Job) []func() {
	deferred := make([]func(), 0, len(jobs))

	for ent, item := range jobs {
		select {
		case s.JobChan <- item:
			deferred = append(deferred, func(e ecs.Entity) func() {
				return func() {
					if !e.IsZero() {
						w.Mappers.World.Exchange(
							e,
							[]ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)},
							[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)},
						)
					}
				}
			}(ent))
		default:
			log.Printf("Job channel full for entity %v\n", ent)
		}
	}
	return deferred
}

func (s *InterventionDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	toDispatch := s.collectWork(w)
	return s.applyWork(w, toDispatch)
}

/* ---------------------------  RESULT  --------------------------- */

type InterventionResultSystem struct {
	ResultChan <-chan jobs.Result
}

func (s *InterventionResultSystem) Initialize(w *controller.CPRaWorld) {}

func (s *InterventionResultSystem) collectInterventionResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			out[res.Entity()] = res
		default:
			break loop
		}
	}
	return out
}

func (s *InterventionResultSystem) processInterventionResultsAndQueueStructuralChanges(
	w *controller.CPRaWorld, results map[ecs.Entity]jobs.Result,
) []func() {

	deferred := make([]func(), 0, len(results))

	for _, res := range results {
		entity := res.Entity()

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionPending](w.Mappers.World)) {
			continue
		}

		name := string([]byte(*w.Mappers.Name.Get(entity)))
		fmt.Printf("entity is %v for %s intervention result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			config := *(*w.Mappers.InterventionConfig.Get(entity)).Copy()
			statusCopy := *(*w.Mappers.InterventionStatus.Get(entity)).Copy()
			//monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			//monitorCopy.Status = "failed"

			deferred = append(deferred, func(e ecs.Entity, s components.InterventionStatus) func() {
				return func() {
					interventionMapper := generic.NewMap[components.InterventionStatus](w.Mappers.World)
					interventionMapper.Set(e, &s)
					//monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					//monitorMapper.Set(e, &m)
				}
			}(entity, statusCopy))

			if config.MaxFailures <= statusCopy.ConsecutiveFailures {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					log.Printf("Monitor %s intervention failed\n", name)

					deferred = append(deferred, func(e ecs.Entity) func() {
						return func() { w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "red"}) }
					}(entity))
				}
				// No retry when max failures reached.
			} else {
				// Schedule retry.
				deferred = append(deferred, func(e ecs.Entity) func() {
					return func() { w.Mappers.InterventionNeeded.Assign(e, &components.InterventionNeeded{}) }
				}(entity))
			}

		} else {
			// ---- SUCCESS ----
			statusCopy := *(*w.Mappers.InterventionStatus.Get(entity)).Copy()
			//monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()
			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			//monitorCopy.Status = "success"

			deferred = append(deferred, func(e ecs.Entity, s components.InterventionStatus) func() {
				return func() {
					interventionMapper := generic.NewMap[components.InterventionStatus](w.Mappers.World)
					interventionMapper.Set(e, &s)
					//monitorMapper := generic.NewMap[components.MonitorStatus](w.Mappers.World)
					//monitorMapper.Set(e, &m)
				}
			}(entity, statusCopy))

			if lastStatus == "failed" &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {

				log.Printf("Monitor %s intervention succeeded and needs cyan code\n", name)
				deferred = append(deferred, func(e ecs.Entity) func() {
					return func() { w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "cyan"}) }
				}(entity))
			}
		}

		// Always remove pending after processing.
		deferred = append(deferred, func(e ecs.Entity) func() { return func() { w.Mappers.InterventionPending.Remove(e) } }(entity))
	}
	return deferred
}

func (s *InterventionResultSystem) Update(w *controller.CPRaWorld) []func() {
	results := s.collectInterventionResults()
	return s.processInterventionResultsAndQueueStructuralChanges(w, results)
}

///* ------------------  Utility: dump component names  ------------------ */
//
//func GetEntityComponents(w *ecs.World, entity ecs.Entity) []string {
//	ids := w.Ids(entity)
//	out := make([]string, 0, len(ids))
//	for _, id := range ids {
//		info, _ := ecs.ComponentInfo(w, id)
//		out = append(out, info.Type.Name())
//	}
//	return out
//}
