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
	toDispatch := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)

	for query.Next() {

		job, _ := query.Get()
		// if an interval elapsed and not pending, append to toDispatch
		// (add your logic)
		toDispatch[query.Entity()] = job.Job
	}
	//s.lock.Lock()
	for entity, job := range toDispatch {
		select {
		case s.JobChan <- job:

			w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
			// mark entity as pending (using Exchange after a query)
		default:
			// handle worker pool full, maybe log or retry
		}
	}
	//s.lock.Unlock()
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
	for {
		select {
		case res := <-s.ResultChan:
			//s.lock.Lock()
			entity := res.Entity()

			// 1. Define components to be removed structurally
			//removes := []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)} // Assumes PulsePulse always exists to be removed

			name := w.Mappers.Name.Get(entity)
			if res.Error() != nil {
				config, status := w.Mappers.Intervention.Get(entity)
				status.ConsecutiveFailures++
				if config.MaxFailures <= status.ConsecutiveFailures {
					// Re-acquire Name mapper if it's dynamic or might be affected by prior changes, though unlikely for Name
					status.LastStatus = "failed"
					status.LastError = res.Error()
					fmt.Printf("Monitor %s intervention failed\n", *name)
					if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
						componentsList := GetEntityComponents(w.Mappers.World, entity)
						fmt.Printf("DEBUG: Entity %v has components: %v\n", entity, componentsList)
						fmt.Println("scheduling red code")
						if !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodeNeeded](w.Mappers.World)) {
							w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{
								Color: "red",
							})
						}
					}

				} else {
					w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)})
				}
			} else {
				_, status := w.Mappers.Intervention.Get(entity)
				lastStatus := status.LastStatus

				status.LastStatus = "success"
				status.LastError = nil
				status.ConsecutiveFailures = 0
				status.LastSuccessTime = time.Now()

				if lastStatus == "failed" {
					if w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
						w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "cyan"})
						fmt.Printf("Monitor %s intervention succeded and needs cyan code\n", *name)
					}
				}
				w.Mappers.InterventionPending.Remove(entity)
			}
			//s.lock.Unlock()
			// The line w.Mappers.Pulse.Assign(entity, config, status) is not needed here.
			// Direct modification of status fields is the correct way to update values.
		default:
			return
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
