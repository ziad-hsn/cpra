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

type dispatchInfo struct {
	Job   jobs.Job
	Color string
}

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
	// The map now holds the new struct
	toDispatch := make(map[ecs.Entity]dispatchInfo)
	query := s.CodeNeededFilter.Query(w.Mappers.World)

	for query.Next() {
		entity := query.Entity()
		needed := query.Get()
		color := needed.Color

		var job jobs.Job
		switch color {
		case "red":
			job = w.Mappers.RedCodeJob.Get(entity).Job
		case "green":
			job = w.Mappers.GreenCodeJob.Get(entity).Job
		case "yellow":
			job = w.Mappers.YellowCodeJob.Get(entity).Job
		case "cyan":
			job = w.Mappers.CyanCodeJob.Get(entity).Job
		case "gray":
			job = w.Mappers.GrayCodeJob.Get(entity).Job
		default:
			// Skip unknown colors
			continue
		}

		// Store both the job and the color string
		toDispatch[entity] = dispatchInfo{Job: job, Color: color}
	}
	for entity, job := range toDispatch {
		select {
		case s.JobChan <- job.Job:

			w.Mappers.World.ExchangeFn(entity, []ecs.ID{ecs.ComponentID[components.CodePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.CodeNeeded](w.Mappers.World)}, func(entity ecs.Entity) {
				code := w.Mappers.CodePending.Get(entity)
				code.Color = job.Color
			})
			// mark entity as pending (using Exchange after a query)
		default:
			// handle worker pool full, maybe log or retry
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type CodeResultSystem struct {
	PendingCodeFilter generic.Filter1[components.CodePending]
	ResultChan        <-chan jobs.Result
}

func (s *CodeResultSystem) Initialize(w controller.CPRaWorld) {
	w.Mappers.World.IsLocked()
}

func (s *CodeResultSystem) Update(w controller.CPRaWorld) {

	for {
		select {
		case res := <-s.ResultChan:
			entity := res.Entity()

			c := w.Mappers.CodePending.Get(entity)
			var status components.CodeStatusAccessor
			if c.Color == "" {
				log.Fatal(GetEntityComponents(w.Mappers.World, entity))
			}
			switch c.Color {
			case "red":
				_, status = w.Mappers.RedCodeConfig.Get(entity)
			case "green":
				_, status = w.Mappers.GreenCodeConfig.Get(entity)
			case "yellow":
				_, status = w.Mappers.YellowCodeConfig.Get(entity)
			case "cyan":
				_, status = w.Mappers.CyanCodeConfig.Get(entity)
			case "gray":
				_, status = w.Mappers.GrayCodeConfig.Get(entity)
			}
			name := w.Mappers.Name.Get(entity)
			if res.Error() != nil {
				status.SetFailure(res.Error())
				// Re-acquire Name mapper if it's dynamic or might be affected by prior changes, though unlikely for Name

				fmt.Printf("Monitor %s Code failed\n", *name)

			} else {
				status.SetSuccess(time.Now())
				fmt.Printf("Monitor %s %q code sent successfully\n", *name, c.Color)
			}
			w.Mappers.CodePending.Remove(entity)
		default:
			return
		}
	}
}
