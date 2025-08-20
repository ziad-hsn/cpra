package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"
	"github.com/mlange-42/ark/ecs"
	"time"
)

/* ---------------------------  DISPATCH  --------------------------- */

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter ecs.Filter1[components.InterventionNeeded]
	Mapper                   *entities.EntityManager
	QueueManager             *queue.QueueManager
}

func (s *InterventionDispatchSystem) Initialize(w *ecs.World) {
	s.InterventionNeededFilter = *ecs.
		NewFilter1[components.InterventionNeeded](w).
		Without(ecs.C[components.InterventionPending]())
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *InterventionDispatchSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		job := s.Mapper.InterventionJob.Get(ent).Job
		if job != nil {
			out[ent] = job

			namePtr := s.Mapper.Name.Get(ent)
			if namePtr != nil {
				controller.DispatchLogger.Debug("Entity[%d] (%s) intervention job collected", ent.ID(), *namePtr)
			}
		} else {
			controller.DispatchLogger.Warn("Entity[%d] has no intervention job", ent.ID())
		}
	}

	controller.DispatchLogger.LogSystemPerformance("InterventionDispatch", time.Since(start), len(out))
	return out
}

func (s *InterventionDispatchSystem) applyWork(w *ecs.World, jobs map[ecs.Entity]jobs.Job) {
	for ent, job := range jobs {
		// Safely send job to channel

		if w.Alive(ent) {
			// Prevent component duplication
			if s.Mapper.InterventionPending.HasAll(ent) {
				namePtr := s.Mapper.Name.Get(ent)
				if namePtr != nil {
					controller.DispatchLogger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, ent.ID())
				}
				continue
			}

			err := s.QueueManager.EnqueueIntervention(ent, job)
			if err != nil {
				controller.DispatchLogger.Warn("Failed to enqueue intervention for entity %d: %v", ent.ID(), err)
				continue
			}

			// Safe component transition
			if s.Mapper.InterventionNeeded.HasAll(ent) {
				s.Mapper.InterventionNeeded.Remove(ent)
				s.Mapper.InterventionPending.Add(ent, &components.InterventionPending{})

				namePtr := s.Mapper.Name.Get(ent)
				if namePtr != nil {
					controller.DispatchLogger.Debug("Dispatched %s job for entity: %d", *namePtr, ent.ID())
				}
				controller.DispatchLogger.LogComponentState(ent.ID(), "InterventionNeeded->InterventionPending", "transitioned")
			}

		}
	}
}

func (s *InterventionDispatchSystem) Update(w *ecs.World) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch)
}

func (s *InterventionDispatchSystem) Finalize(w *ecs.World) {
	close(s.JobChan)
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
		case res, ok := <-s.ResultChan:
			if !ok {
				break loop
			}
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

		namePtr := s.Mapper.Name.Get(entity)
		if namePtr == nil {
			controller.ResultLogger.Warn("Entity %d has nil name component", entity.ID())
			continue
		}
		name := *namePtr

		if res.Error() != nil {
			// ---- FAILURE ----
			maxFailures := s.Mapper.InterventionConfig.Get(entity).MaxFailures
			statusCopy := s.Mapper.InterventionStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++

			controller.ResultLogger.Debug("Monitor %s failed (attempt %d/%d): %v",
				name, statusCopy.ConsecutiveFailures, maxFailures, res.Error())

			if maxFailures <= statusCopy.ConsecutiveFailures {
				if s.Mapper.RedCode.HasAll(entity) {
					controller.ResultLogger.Info("Monitor %s intervention failed", name)
					if !s.Mapper.CodeNeeded.HasAll(entity) {
						s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "red"})
						controller.ResultLogger.LogComponentState(entity.ID(), "CodeNeeded", "red added")
					}
				}
			} else {
				s.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
				controller.ResultLogger.LogComponentState(entity.ID(), "InterventionNeeded", "added")
			}

		} else {
			// ---- SUCCESS ----
			statusCopy := s.Mapper.InterventionStatus.Get(entity)
			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()

			controller.ResultLogger.Debug("Monitor %s intervention successful", name)

			if lastStatus == "failed" && s.Mapper.CyanCode.HasAll(entity) {
				controller.ResultLogger.Info("Monitor %s intervention succeeded and needs cyan code", name)
				if !s.Mapper.CodeNeeded.HasAll(entity) {
					s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "cyan"})
					controller.ResultLogger.LogComponentState(entity.ID(), "CodeNeeded", "cyan added")
				}
			}
		}

		// Always remove pending after processing.
		s.Mapper.InterventionPending.Remove(entity)
		controller.ResultLogger.LogComponentState(entity.ID(), "InterventionPending", "removed")
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
