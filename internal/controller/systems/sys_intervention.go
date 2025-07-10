package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"sync"
	"time"
)

type dispatchableIntervention struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter2[components.InterventionJob, components.InterventionNeeded]
	lock                     sync.Locker
}

func (s *InterventionDispatchSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.InterventionNeededFilter = *generic.NewFilter2[components.InterventionJob, components.InterventionNeeded]().Without(generic.T[components.InterventionPending]())
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *InterventionDispatchSystem) findNeededInterventions(w controller.CPRaWorld) []dispatchableIntervention {
	toDispatch := make([]dispatchableIntervention, 0)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)
	for query.Next() {
		job, _ := query.Get()
		toDispatch = append(toDispatch, dispatchableIntervention{
			Entity: query.Entity(),
			Job:    job.Job,
		})
	}
	return toDispatch
}

func (s *InterventionDispatchSystem) Update(w controller.CPRaWorld) {
	// Phase 1: Read from the world.
	dispatchList := s.findNeededInterventions(w)

	// Phase 2: Write to the world and channels.
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.Job.Copy():
			w.Mappers.World.Exchange(entry.Entity, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
		default:
			fmt.Printf("Job channel full for entity %v\n", entry.Entity)
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type InterventionResultSystem struct {
	PendingInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionNeeded]
	ResultChan                <-chan jobs.Result
	lock                      sync.Locker
}

func (s *InterventionResultSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *InterventionResultSystem) Update(w controller.CPRaWorld) {
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
		config := (*components.InterventionConfig)(w.Mappers.World.Get(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)))
		status := (*components.InterventionStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.InterventionStatus](w.Mappers.World)))
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))

		// Update data
		if res.Error() != nil {
			status.LastStatus = "failed"
			status.LastError = res.Error()
			if config.MaxFailures <= status.ConsecutiveFailures {
				fmt.Printf("Monitor %s intervention failed\n", *name)
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					//componentsList := GetEntityComponents(w.Mappers.World, entity)
					//fmt.Printf("DEBUG: Entity %v has components: %v\n", entity, componentsList)
					fmt.Println("scheduling red code")
					if !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodeNeeded](w.Mappers.World)) {
						w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "red"})
					}
				}
			} else {
				// Re-schedule intervention by adding InterventionNeeded
				w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)})
			}
		} else {
			lastStatus := status.LastStatus
			status.LastStatus = "success"
			status.LastError = nil
			status.ConsecutiveFailures = 0
			status.LastSuccessTime = time.Now()

			if lastStatus == "failed" {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
					w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "cyan"})
					fmt.Printf("Monitor %s intervention succeeded and needs cyan code\n", *name)
				}
			}
			// Remove InterventionPending last
			w.Mappers.InterventionPending.Remove(entity)
		}
	}
}

func GetEntityComponents(w *ecs.World, entity ecs.Entity) []string {
	// 1. Retrieve Component IDs for the entity.
	ids := w.Ids(entity)

	var componentNames []string

	// 2. Iterate and access components.
	for _, id := range ids {
		// Get the reflect.Type for the component ID.
		info, _ := ecs.ComponentInfo(w, id)
		compType := info.Type

		// Get a pointer to the component data.
		// Note: world.Get() returns an unsafe.Pointer that we don't need to fully cast
		// just to get the name of the type.
		componentNames = append(componentNames, compType.Name())
	}
	return componentNames
}
