package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"log"
	"sync"
	"time"
)

type dispatchableCodeJob struct {
	entity ecs.Entity
	job    jobs.Job
	color  string
}
type CodeDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	CodeNeededFilter         generic.Filter1[components.CodeNeeded]
	FailedInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]
	lock                     sync.Locker
}

func (s *CodeDispatchSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.CodeNeededFilter = *generic.NewFilter1[components.CodeNeeded]().Without(generic.T[components.CodePending]())
	s.FailedInterventionFilter = *generic.NewFilter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]()
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *CodeDispatchSystem) findNeededCodeJobs(w controller.CPRaWorld) []dispatchableCodeJob {
	toDispatch := make([]dispatchableCodeJob, 0)
	query := s.CodeNeededFilter.Query(w.Mappers.World)
	for query.Next() {
		entity := query.Entity()
		codeNeeded := (*components.CodeNeeded)(query.Query.Get(ecs.ComponentID[components.CodeNeeded](w.Mappers.World)))
		var job jobs.Job
		switch codeNeeded.Color {
		case "red":
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
				job = (*components.RedCodeJob)(w.Mappers.World.Get(entity, ecs.ComponentID[components.RedCodeJob](w.Mappers.World))).Job
			}
		case "green":
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
				job = (*components.GreenCodeJob)(w.Mappers.World.Get(entity, ecs.ComponentID[components.GreenCodeJob](w.Mappers.World))).Job
			}
		case "yellow":
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				job = (*components.YellowCodeJob)(w.Mappers.World.Get(entity, ecs.ComponentID[components.YellowCodeJob](w.Mappers.World))).Job
			}
		case "cyan":
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
				job = (*components.CyanCodeJob)(w.Mappers.World.Get(entity, ecs.ComponentID[components.CyanCodeJob](w.Mappers.World))).Job
			}
		case "gray":
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.GrayCode](w.Mappers.World)) {
				job = (*components.GrayCodeJob)(w.Mappers.World.Get(entity, ecs.ComponentID[components.GrayCodeJob](w.Mappers.World))).Job
			}
		default:
			log.Printf("Unknown color %q for entity %v", codeNeeded.Color, entity)
			continue
		}
		if job != nil {
			toDispatch = append(toDispatch, dispatchableCodeJob{entity: entity, job: job, color: codeNeeded.Color})
		}
	}
	return toDispatch
}

func (s *CodeDispatchSystem) Update(w controller.CPRaWorld) {
	// Phase 1: Read from the world.
	dispatchList := s.findNeededCodeJobs(w)

	// Phase 2: Write to the world and channels.
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.job.Copy():
			log.Printf("Sent %s code job for entity %v", entry.color, entry.entity)
			w.Mappers.World.Exchange(entry.entity, []ecs.ID{ecs.ComponentID[components.CodePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.CodeNeeded](w.Mappers.World)})
		default:
			log.Printf("Job channel full for entity %v", entry.entity)
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type CodeResultSystem struct {
	PendingCodeFilter generic.Filter1[components.CodePending]
	ResultChan        <-chan jobs.Result
	lock              sync.Locker
}

func (s *CodeResultSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *CodeResultSystem) Update(w controller.CPRaWorld) {
	// Collect results to process

	toProcess := make([]resultEntry, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			toProcess = append(toProcess, resultEntry{entity: res.Entity(), result: res})
		default:
			break loop // Exit loop when no more results
		}
	}

	// Process collected results
	for _, entry := range toProcess {
		entity := entry.entity
		res := entry.result

		// Get all component pointers before structural changes
		codePending := w.Mappers.CodePending.Get(entity)
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))
		var status components.CodeStatusAccessor
		switch codePending.Color {
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
		default:
			log.Printf("Unknown color %q for entity %v", codePending.Color, entity)
			continue
		}

		// Update data
		if res.Error() != nil {
			status.SetFailure(res.Error())
			fmt.Printf("Monitor %s Code failed\n", *name)
		} else {
			status.SetSuccess(time.Now())
			fmt.Printf("Monitor %s %q code sent successfully\n", *name, codePending.Color)
		}

		// Perform structural change last
		w.Mappers.CodePending.Remove(entity)
	}
}
