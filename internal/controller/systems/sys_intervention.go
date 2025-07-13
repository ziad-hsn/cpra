package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"log"
	"time" // Added for time.Now() used in original code
)

type dispatchableIntervention struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter2[components.InterventionJob, components.InterventionNeeded]
	// lock                     sync.Locker // REMOVED: External lock is not needed for arche when used in a single goroutine.
}

func (s *InterventionDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.InterventionNeededFilter = *generic.NewFilter2[components.InterventionJob, components.InterventionNeeded]().Without(generic.T[components.InterventionPending]())
	// s.lock = lock // REMOVED
	// w.Mappers.World.IsLocked() // REMOVED: Polling IsLocked() is problematic and unnecessary.
}

// collectWork: Phase 1 - Reads from the world to find interventions to dispatch.
func (s *InterventionDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableIntervention {
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

// applyWork: Phase 2 - Dispatches jobs and applies structural changes to the world.
func (s *InterventionDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchableIntervention) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.Job.Copy():
			// Structural change: Exchange components.
			// Ensure entity is still alive before making structural changes
			if w.Mappers.World.Alive(entry.Entity) {
				w.Mappers.World.Exchange(entry.Entity,
					[]ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)},
					[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
			}
		default:
			log.Printf("Job channel full for entity %v\n", entry.Entity)
		}
	}
}

func (s *InterventionDispatchSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	dispatchList := s.collectWork(w)
	s.applyWork(w, dispatchList)
}

// InterventionResultSystem --- RESULT PROCESS SYSTEM ---
type InterventionResultSystem struct {
	PendingInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionNeeded] // This field is not used in Update, consider removing if truly unused.
	ResultChan                <-chan jobs.Result
	// lock                      sync.Locker // REMOVED
}

func (s *InterventionResultSystem) Initialize(w *controller.CPRaWorld) {
	// s.lock = lock // REMOVED
	// w.Mappers.World.IsLocked() // REMOVED
}

// collectInterventionResults: Phase 1.1 - Drains the result channel into a slice.
func (s *InterventionResultSystem) collectInterventionResults() []resultEntry {
	toProcess := make([]resultEntry, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			toProcess = append(toProcess, resultEntry{entity: res.Entity(), result: res})
		default:
			break loop
		}
	}
	return toProcess
}

// processInterventionResultsAndQueueStructuralChanges: Phase 1.2 - Processes results, makes data changes,
// and returns a slice of functions that will perform structural changes.
func (s *InterventionResultSystem) processInterventionResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results []resultEntry) []func() {
	deferredOps := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result

		if entity.IsZero() || !w.Mappers.World.Alive(entity) { // Check if entity is valid/alive
			continue
		}

		// Get components once for this entity
		config := (*components.InterventionConfig)(w.Mappers.World.Get(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)))
		status := (*components.InterventionStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.InterventionStatus](w.Mappers.World)))
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))

		if res.Error() != nil {
			// Data-only changes are safe to do immediately.
			status.LastStatus = "failed"
			status.LastError = res.Error()
			status.ConsecutiveFailures++

			if config.MaxFailures <= status.ConsecutiveFailures {
				log.Printf("Monitor %s intervention failed\n", *name)
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					log.Println("scheduling red code")
					if !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodeNeeded](w.Mappers.World)) {
						// This is a structural change. Defer it.
						deferredOps = append(deferredOps, func(e ecs.Entity) func() {
							return func() {
								if w.Mappers.World.Alive(e) {
									w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "red"})
								}
							}
						}(entity))
					}
				}
			} else {
				// This is an Exchange. Defer it.
				deferredOps = append(deferredOps, func(e ecs.Entity) func() {
					return func() {
						if w.Mappers.World.Alive(e) {
							add := []ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)}
							remove := []ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)}
							w.Mappers.World.Exchange(e, add, remove)
						}
					}
				}(entity))
			}
		} else {
			// Data-only changes are safe.
			lastStatus := status.LastStatus // Capture before modification
			status.LastStatus = "success"
			status.LastError = nil
			status.ConsecutiveFailures = 0
			status.LastSuccessTime = time.Now()

			if lastStatus == "failed" {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
					log.Printf("Monitor %s intervention succeeded and needs cyan code\n", *name)
					// This is a structural change. Defer it.
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if w.Mappers.World.Alive(e) {
								w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "cyan"})
							}
						}
					}(entity))
				}
			}
			// This is a Remove. Defer it.
			deferredOps = append(deferredOps, func(e ecs.Entity) func() {
				return func() {
					if w.Mappers.World.Alive(e) {
						w.Mappers.InterventionPending.Remove(e)
					}
				}
			}(entity))
		}
	}
	return deferredOps
}

// applyInterventionQueuedStructuralChanges: Phase 2 - Executes all the queued structural changes at once.
func (s *InterventionResultSystem) applyInterventionQueuedStructuralChanges(deferredOps []func()) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, op := range deferredOps {
		op()
	}
}

func (s *InterventionResultSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	results := s.collectInterventionResults()
	queuedChanges := s.processInterventionResultsAndQueueStructuralChanges(w, results)
	s.applyInterventionQueuedStructuralChanges(queuedChanges)
}

// The commented-out GetEntityComponents function from your original code.
//func GetEntityComponents(w *ecs.World, entity ecs.Entity) []string {
//	// 1. Retrieve Component IDs for the entity.
//	ids := w.Ids(entity)
//
//	var componentNames []string
//
//	// 2. Iterate and access components.
//	for _, id := range ids {
//		// Get the reflect.Type for the component ID.
//		info, _ := ecs.ComponentInfo(w, id)
//		compType := info.Type
//
//		// Get a pointer to the component data.
//		// Note: world.Get() returns an unsafe.Pointer that we don't need to fully cast
//		// just to get the name of the type.
//		componentNames = append(componentNames, compType.Name())
//	}
//	return componentNames
//}
