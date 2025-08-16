package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
	"log"
	"time"
)

/* ---------------------------  DISPATCH  --------------------------- */

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter ecs.Filter1[components.InterventionNeeded]
	Mapper                   *entities.EntityManager
}

func (s *InterventionDispatchSystem) Initialize(w *ecs.World) {
	s.InterventionNeededFilter = *ecs.
		NewFilter1[components.InterventionNeeded](w).
		Without(ecs.C[components.InterventionPending]())
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *InterventionDispatchSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	out := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		out[ent] = s.Mapper.InterventionJob.Get(ent).Job
	}
	return out
}

func (s *InterventionDispatchSystem) applyWork(w *ecs.World, jobs map[ecs.Entity]jobs.Job) {

	for ent, item := range jobs {
		select {
		case s.JobChan <- item:

			if w.Alive(ent) {
				s.Mapper.InterventionPending.Add(ent, &components.InterventionPending{})
				s.Mapper.InterventionNeeded.Remove(ent)
				//s.Mapper.InterventionPendingExchange.Exchange(ent, &components.InterventionPending{}, &components.InterventionNeeded{})
			}
		default:
			log.Printf("Intervention Job channel full for entity %v\n", ent)
		}
	}
}

func (s *InterventionDispatchSystem) Update(w *ecs.World) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch)
}

func (s *InterventionDispatchSystem) Finalize(w *ecs.World) {
}

/* ---------------------------  RESULT  --------------------------- */

type InterventionResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
}

func (s *InterventionResultSystem) Initialize(w *ecs.World) {
	//s.Mapper = entities.InitializeMappers(w)
}

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
	w *ecs.World, results map[ecs.Entity]jobs.Result,
) {

	for _, res := range results {
		entity := res.Entity()

		if !w.Alive(entity) || !s.Mapper.InterventionPending.HasAll(entity) {
			continue
		}

		name := *s.Mapper.Name.Get(entity)
		//fmt.Printf("entity is %v for %s intervention result.\n", entity, name)

		if res.Error() != nil {
			//fmt.Println("booooooooooooooooooooooooooooooooooooo")
			// ---- FAILURE ----
			maxFailures := s.Mapper.InterventionConfig.Get(entity).MaxFailures
			statusCopy := *s.Mapper.InterventionStatus.Get(entity)
			//monitorCopy := *(*w.Mapper.MonitorStatus.Get(entity)).Copy()

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			//monitorCopy.Status = "failed"

			s.Mapper.InterventionStatus.Set(entity, &statusCopy)

			//fmt.Println(statusCopy.LastStatus, maxFailures, statusCopy.ConsecutiveFailures, statusCopy.LastError)
			if maxFailures <= statusCopy.ConsecutiveFailures {
				if s.Mapper.RedCode.HasAll(entity) {
					log.Printf("Monitor %s intervention failed\n", name)
					s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "red"})
				}
				// No retry when max failures reached.
			} else {
				// Schedule retry.
				s.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
			}

		} else {
			//fmt.Println("horaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaay")
			// ---- SUCCESS ----
			statusCopy := *s.Mapper.InterventionStatus.Get(entity)
			//monitorCopy := *(*w.Mapper.MonitorStatus.Get(entity)).Copy()
			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			//monitorCopy.Status = "success"

			s.Mapper.InterventionStatus.Set(entity, &statusCopy)

			if lastStatus == "failed" &&
				s.Mapper.CyanCode.HasAll(entity) {

				log.Printf("Monitor %s intervention succeeded and needs cyan code\n", name)
				s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "cyan"})
			}
		}

		// Always remove pending after processing.
		s.Mapper.InterventionPending.Remove(entity)
	}
}

func (s *InterventionResultSystem) Update(w *ecs.World) {
	results := s.collectInterventionResults()
	s.processInterventionResultsAndQueueStructuralChanges(w, results)
}

func (s *InterventionResultSystem) Finalize(w *ecs.World) {}

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
