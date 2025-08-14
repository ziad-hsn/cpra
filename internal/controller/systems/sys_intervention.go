package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
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
		out[ent] = w.Mappers.InterventionJob.Get(ent).Job
	}
	return out
}

func (s *InterventionDispatchSystem) applyWork(w *controller.CPRaWorld, jobs map[ecs.Entity]jobs.Job, commandBuffer *CommandBufferSystem) {

	for ent, item := range jobs {
		select {
		case s.JobChan <- item:

			if w.IsAlive(ent) {
				commandBuffer.markInterventionPending(ent)
			}
		default:
			log.Printf("Intervention Job channel full for entity %v\n", ent)
		}
	}
}

func (s *InterventionDispatchSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch, cb)
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
	w *controller.CPRaWorld, results map[ecs.Entity]jobs.Result, commandBuffer *CommandBufferSystem,
) {

	for _, res := range results {
		entity := res.Entity()

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionPending](w.Mappers.World)) {
			continue
		}

		//name := strings.Clone(string(*w.Mappers.Name.Get(entity)))
		//fmt.Printf("entity is %v for %s intervention result.\n", entity, name)

		if res.Error() != nil {
			//fmt.Println("booooooooooooooooooooooooooooooooooooo")
			// ---- FAILURE ----
			maxFailures := w.Mappers.InterventionConfig.Get(entity).MaxFailures
			statusCopy := *w.Mappers.InterventionStatus.Get(entity)
			//monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			//monitorCopy.Status = "failed"

			commandBuffer.setInterventionStatus(entity, statusCopy)
			//fmt.Println(statusCopy.LastStatus, maxFailures, statusCopy.ConsecutiveFailures, statusCopy.LastError)
			if maxFailures <= statusCopy.ConsecutiveFailures {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					//log.Printf("Monitor %s intervention failed\n", name)

					commandBuffer.scheduleCode(entity, "red")
				}
				// No retry when max failures reached.
			} else {
				// Schedule retry.
				commandBuffer.scheduleIntervention(entity)
			}

		} else {
			//fmt.Println("horaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaay")
			// ---- SUCCESS ----
			statusCopy := *w.Mappers.InterventionStatus.Get(entity)
			//monitorCopy := *(*w.Mappers.MonitorStatus.Get(entity)).Copy()
			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			//monitorCopy.Status = "success"

			commandBuffer.setInterventionStatus(entity, statusCopy)

			if lastStatus == "failed" &&
				w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {

				//log.Printf("Monitor %s intervention succeeded and needs cyan code\n", name)
				commandBuffer.scheduleCode(entity, "cyan")
			}
		}

		// Always remove pending after processing.
		commandBuffer.RemoveInterventionPending(entity)
	}
}

func (s *InterventionResultSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	results := s.collectInterventionResults()
	s.processInterventionResultsAndQueueStructuralChanges(w, results, cb)
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
