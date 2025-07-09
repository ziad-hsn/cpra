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

func (s *InterventionDispatchSystem) Update(w controller.CPRaWorld) {
	// Collect entities and jobs to dispatch
	type dispatchEntry struct {
		entity ecs.Entity
		job    jobs.Job
	}
	toDispatch := make([]dispatchEntry, 0)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)
	for query.Next() {
		job, _ := query.Get()
		toDispatch = append(toDispatch, dispatchEntry{
			entity: query.Entity(),
			job:    job.Job,
		})
	}

	// Process collected entities
	for _, entry := range toDispatch {
		select {
		case s.JobChan <- entry.job:
			w.Mappers.World.Exchange(entry.entity, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
		default:
			// Handle worker pool full
			fmt.Printf("Job channel full for entity %v\n", entry.entity)
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
	select {
	case res := <-s.ResultChan:
		entity := res.Entity()
		// Validate entity
		if !w.Mappers.World.Alive(entity) {
			log.Printf("Invalid entity %v received from ResultChan", entity)
			return
		}

		// Get and validate all component pointers
		if !w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) ||
			!w.Mappers.World.Has(entity, ecs.ComponentID[components.PulseStatus](w.Mappers.World)) ||
			!w.Mappers.World.Has(entity, ecs.ComponentID[components.Name](w.Mappers.World)) {
			log.Printf("Entity %v missing required components: %v", entity, GetEntityComponents(w.Mappers.World, entity))
			return
		}

		config := (*components.InterventionConfig)(w.Mappers.World.Get(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)))
		status := (*components.PulseStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.PulseStatus](w.Mappers.World)))
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))

		// Log components for debugging
		log.Printf("Processing entity %v components: %v", entity, GetEntityComponents(w.Mappers.World, entity))

		// Update data
		if res.Error() != nil {
			status.LastStatus = "failed"
			status.LastError = res.Error()
			if config.MaxFailures <= status.ConsecutiveFailures {
				log.Printf("Monitor %s intervention failed", *name)
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					componentsList := GetEntityComponents(w.Mappers.World, entity)
					log.Printf("DEBUG: Entity %v has components: %v", entity, componentsList)
					log.Println("scheduling red code")
					if !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodeNeeded](w.Mappers.World)) {
						w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "red"})
					}
				}
			} else {
				// Re-schedule intervention
				log.Printf("Before Exchange entity %v: %v", entity, GetEntityComponents(w.Mappers.World, entity))
				w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)})
				log.Printf("After Exchange entity %v: %v", entity, GetEntityComponents(w.Mappers.World, entity))
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
					log.Printf("Monitor %s intervention succeeded and needs cyan code", *name)
				}
			}
			// Remove InterventionPending last
			log.Printf("Before Remove entity %v: %v", entity, GetEntityComponents(w.Mappers.World, entity))
			w.Mappers.InterventionPending.Remove(entity)
			log.Printf("After Remove entity %v: %v", entity, GetEntityComponents(w.Mappers.World, entity))
		}
	default:
		return
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
