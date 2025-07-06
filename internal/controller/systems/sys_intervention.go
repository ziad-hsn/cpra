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

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter2[components.InterventionJob, components.InterventionNeeded]
	FailedInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]
}

func (s *InterventionDispatchSystem) Initialize(w controller.CPRaWorld) {
	s.InterventionNeededFilter = *generic.NewFilter2[components.InterventionJob, components.InterventionNeeded]().Without(generic.T[components.InterventionPending]())
	s.FailedInterventionFilter = *generic.NewFilter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]()

	w.Mappers.World.IsLocked()
}

func (s *InterventionDispatchSystem) Update(w controller.CPRaWorld) {
	toDispatch := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)

	for query.Next() {

		job, _ := query.Get()
		// if an interval elapsed and not pending, append to toDispatch
		// (add your logic)
		toDispatch[query.Entity()] = job.Job
	}
	for entity, job := range toDispatch {
		select {
		case s.JobChan <- job:

			w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
			// mark entity as pending (using Exchange after a query)
		default:
			// handle worker pool full, maybe log or retry
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type InterventionResultSystem struct {
	PendingPulseFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
}

func (s *InterventionResultSystem) Initialize(w controller.CPRaWorld) {
	w.Mappers.World.IsLocked()
}

func (s *InterventionResultSystem) Update(w controller.CPRaWorld) {
	for {
		select {
		case res := <-s.ResultChan:
			entity := res.Entity()

			// 1. Define components to be removed structurally
			//removes := []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)} // Assumes PulsePulse always exists to be removed

			config, status := w.Mappers.Intervention.Get(entity)

			name := w.Mappers.Name.Get(entity)
			if res.Error() != nil {
				status.LastStatus = "failed"
				status.LastError = res.Error()
				status.ConsecutiveFailures++
				if config.MaxFailures <= status.ConsecutiveFailures {
					// Re-acquire Name mapper if it's dynamic or might be affected by prior changes, though unlikely for Name

					fmt.Printf("Monitor %s intervention failed\n", *name)
					if w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
						fmt.Println("scheduling yellow code")
						w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{
							Color: "yellow",
						})
					}

				} else {
					w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)})
				}
			} else {
				status.LastStatus = "success"
				status.LastError = nil
				status.ConsecutiveFailures = 0
				status.LastSuccessTime = time.Now()
				fmt.Printf("Monitor %s intervention failed and needs Red code\n", *name)
				w.Mappers.InterventionPending.Remove(entity)
			}
			// The line w.Mappers.Pulse.Assign(entity, config, status) is not needed here.
			// Direct modification of status fields is the correct way to update values.
		default:
			return
		}
	}
}
