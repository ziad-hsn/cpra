package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"time"
)

type CodeDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	CodeNeededFilter         generic.Filter1[components.CodeNeeded]
	FailedInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]
}

func (s *CodeDispatchSystem) Initialize(w controller.CPRaWorld) {
	s.CodeNeededFilter = *generic.NewFilter1[components.CodeNeeded]().Without(generic.T[components.CodePending]())
	s.FailedInterventionFilter = *generic.NewFilter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]()

	w.Mappers.World.IsLocked()
}

func (s *CodeDispatchSystem) Update(w controller.CPRaWorld) {
	toDispatch := make(map[ecs.Entity]jobs.Job)
	query := s.CodeNeededFilter.Query(w.Mappers.World)

	for query.Next() {

		entity := query.Entity()
		color := query.Get().Color
		switch color {
		case "red":
			job := w.Mappers.RedCodeJob.Get(entity)
			toDispatch[query.Entity()] = job.Job
		case "green":
			job := w.Mappers.GreenCodeJob.Get(entity)
			toDispatch[query.Entity()] = job.Job
		case "yellow":
			job := w.Mappers.YellowCodeJob.Get(entity)
			toDispatch[query.Entity()] = job.Job
		case "cyan":
			job := w.Mappers.CyanCodeJob.Get(entity)
			toDispatch[query.Entity()] = job.Job
		case "gray":
			job := w.Mappers.GrayCodeJob.Get(entity)
			toDispatch[query.Entity()] = job.Job
		}
		// if an interval elapsed and not pending, append to toDispatch
		// (add your logic)

	}
	for entity, job := range toDispatch {
		select {
		case s.JobChan <- job:

			w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.CodePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.CodeNeeded](w.Mappers.World)})
			// mark entity as pending (using Exchange after a query)
		default:
			// handle worker pool full, maybe log or retry
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type CodeResultSystem struct {
	PendingPulseFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
}

func (s *CodeResultSystem) Initialize(w controller.CPRaWorld) {
	w.Mappers.World.IsLocked()
}

func (s *CodeResultSystem) Update(w controller.CPRaWorld) {

	for {
		select {
		case res := <-s.ResultChan:
			entity := res.Entity()

			_, status := w.Mappers.Intervention.Get(entity)

			name := w.Mappers.Name.Get(entity)
			if res.Error() != nil {
				status.LastStatus = "failed"
				status.LastError = res.Error()
				status.ConsecutiveFailures++
				// Re-acquire Name mapper if it's dynamic or might be affected by prior changes, though unlikely for Name

				fmt.Printf("Monitor %s Code failed\n", *name)

			} else {
				status.LastStatus = "success"
				status.LastError = nil
				status.ConsecutiveFailures = 0
				status.LastSuccessTime = time.Now()
				fmt.Printf("Monitor %s intervention succeded and needs Yellow code\n", *name)
				w.Mappers.CodePending.Remove(entity)
			}
		default:
			return
		}
	}
}
