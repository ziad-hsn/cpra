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

// PulseScheduleSystem refactored
type PulseScheduleSystem struct {
	PulseFilter generic.Filter2[components.PulseConfig, components.PulseStatus]
}

func (s *PulseScheduleSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseFilter = *generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
		Without(generic.T[components.DisabledMonitor]()).
		Without(generic.T[components.PulsePending]()).
		Without(generic.T[components.InterventionNeeded]()).
		Without(generic.T[components.InterventionPending]()).
		Without(generic.T[components.CodeNeeded]()).
		Without(generic.T[components.CodePending]())
}

// collectWork: Phase 1 - Reads from the world and returns entities needing a pulse check.
func (s *PulseScheduleSystem) collectWork(w *controller.CPRaWorld) []ecs.Entity {
	toCheck := make([]ecs.Entity, 0)
	query := s.PulseFilter.Query(w.Mappers.World)
	for query.Next() {
		config := (*components.PulseConfig)(query.Query.Get(ecs.ComponentID[components.PulseConfig](w.Mappers.World)))
		status := (*components.PulseStatus)(query.Query.Get(ecs.ComponentID[components.PulseStatus](w.Mappers.World)))

		// Check for first-time pulse
		if w.Mappers.World.Has(query.Entity(), ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
			toCheck = append(toCheck, query.Entity())
			status.LastCheckTime = time.Now()
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			continue
		}

		// Check for scheduled interval
		if time.Since(status.LastCheckTime) >= config.Interval {
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			status.LastCheckTime = time.Now()
			toCheck = append(toCheck, query.Entity())
		}
		// No need to nil out component pointers if they are used within the function scope.
		// Go's GC handles this.
	}
	return toCheck
}

// applyWork: Phase 2 - Applies structural changes based on collected entities.
func (s *PulseScheduleSystem) applyWork(w *controller.CPRaWorld, entities []ecs.Entity) {
	for _, entity := range entities {
		// Check if entity still exists and doesn't already have PulseNeeded.
		// World.Has is generally safe to call if the entity itself is valid,
		// but if the entity might have been removed by another system, Alive() check is good.
		if w.Mappers.World.Alive(entity) && !w.Mappers.World.Has(entity, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
			w.Mappers.PulseNeeded.Assign(entity, &components.PulseNeeded{})
		}
	}
}

func (s *PulseScheduleSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	entitiesToSchedule := s.collectWork(w)
	s.applyWork(w, entitiesToSchedule)
}

// PulseDispatchSystem refactored
type dispatchablePulse struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded generic.Filter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]
}

func (s *PulseDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseNeeded = *generic.NewFilter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]()
}

// collectWork: Phase 1 - Reads from the world and returns entities ready for dispatch.
func (s *PulseDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchablePulse {
	toDispatch := make([]dispatchablePulse, 0)
	query := s.PulseNeeded.Query(w.Mappers.World)
	for query.Next() {
		job := (*components.PulseJob)(query.Query.Get(ecs.ComponentID[components.PulseJob](w.Mappers.World)))
		status := (*components.PulseStatus)(query.Query.Get(ecs.ComponentID[components.PulseStatus](w.Mappers.World)))

		status.LastCheckTime = time.Now() // Data-only update, safe.

		toDispatch = append(toDispatch, dispatchablePulse{
			Entity: query.Entity(),
			Job:    job.Job,
		})
		// No need to nil component pointers.
	}
	return toDispatch
}

// applyWork: Phase 2 - Dispatches jobs and applies structural changes.
func (s *PulseDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchablePulse) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, item := range dispatchList {
		select {
		case s.JobChan <- item.Job.Copy():
			name := *w.Mappers.Name.Get(item.Entity)
			log.Printf("sent %s job\n", name)
			if w.Mappers.World.Has(item.Entity, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				// Structural change: Remove component.
				w.Mappers.PulseFirstCheck.Remove(item.Entity)
			}
			// Structural change: Exchange components.
			w.Mappers.World.Exchange(item.Entity,
				[]ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)},
				[]ecs.ID{ecs.ComponentID[components.PulseNeeded](w.Mappers.World)})
		default:
			log.Printf("Job channel full, skipping dispatch for entity %v", item.Entity)
			// handle worker pool full, maybe log or retry
		}
	}
}

func (s *PulseDispatchSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	dispatchList := s.collectWork(w)
	s.applyWork(w, dispatchList)
}

// PulseResultSystem refactored
type resultEntry struct {
	entity ecs.Entity
	result jobs.Result
}

type PulseResultSystem struct {
	PendingPulseFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
	// lock sync.Locker // REMOVED
}

func (s *PulseResultSystem) Initialize(w *controller.CPRaWorld) {
	// s.lock = lock // REMOVED
	// w.Mappers.World.IsLocked() // REMOVED
}

// collectResults: Phase 1.1 - Drains the result channel into a slice.
func (s *PulseResultSystem) collectResults() []resultEntry {
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
	return toProcess
}

// processResultsAndQueueStructuralChanges: Phase 1.2 - Processes results, makes data changes,
// and returns a slice of functions that will perform structural changes.
func (s *PulseResultSystem) processResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results []resultEntry) []func() {
	deferredOps := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result
		n := *w.Mappers.Name.Get(entity)
		fmt.Printf("recived %s results\n", n)
		// Add safety check before getting components
		if entity.IsZero() {
			continue
		}

		// Ensure entity is still alive before getting components
		if !w.Mappers.World.Alive(entity) {
			continue
		}

		// Get components once for this entity
		config := (*components.PulseConfig)(w.Mappers.World.Get(entity, ecs.ComponentID[components.PulseConfig](w.Mappers.World)))
		status := (*components.PulseStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.PulseStatus](w.Mappers.World)))
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))
		monitorStatus := (*components.MonitorStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.MonitorStatus](w.Mappers.World)))

		if res.Error() != nil {
			// Data-only changes are safe to do immediately.
			status.LastStatus = "failed"
			status.LastError = res.Error()
			status.ConsecutiveFailures++

			if status.ConsecutiveFailures == 1 {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
					// Structural change: Defer it.
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if w.Mappers.World.Alive(e) {
								w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "yellow"})
							}
						}
					}(entity))
				}
			}
			if config.MaxFailures <= status.ConsecutiveFailures {
				// Data-only change is safe.
				monitorStatus.Status = "failed"

				if w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) {
					fmt.Printf("Monitor %s failed %d times and needs intervention\n", *name, status.ConsecutiveFailures)
					// Structural change: Defer it.
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if w.Mappers.World.Alive(e) {
								w.Mappers.InterventionNeeded.Assign(e, &components.InterventionNeeded{})
							}
						}
					}(entity))
				}
			}
		} else {
			// Data-only changes are safe.
			status.LastStatus = "success"
			status.LastError = nil
			status.ConsecutiveFailures = 0
			status.LastSuccessTime = time.Now()
			s := monitorStatus.Status
			monitorStatus.Status = "success"

			if s == "failed" {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
					// Structural change: Defer it.
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if w.Mappers.World.Alive(e) {
								w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "green"})
							}
						}
					}(entity))
				}
			}
		}

		// This is the final structural change for this entity. Defer it.
		// This ensures PulsePending is removed regardless of success/failure
		deferredOps = append(deferredOps, func(e ecs.Entity, name components.Name) func() {
			return func() {
				if w.Mappers.World.Alive(e) {
					if w.Mappers.World.Has(e, ecs.ComponentID[components.PulsePending](w.Mappers.World)) {
						w.Mappers.PulsePending.Remove(e)
					} else {
						log.Fatalf("name --> %s -- entity --> %v, components --> %v ", name, e, GetEntityComponents(w.Mappers.World, e))
					}
				}
			}
		}(entity, *name))
	}
	return deferredOps
}

// applyQueuedStructuralChanges: Phase 2 - Executes all the queued structural changes at once.
func (s *PulseResultSystem) applyQueuedStructuralChanges(deferredOps []func()) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, op := range deferredOps {
		op()
	}
}

func (s *PulseResultSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	results := s.collectResults()
	queuedChanges := s.processResultsAndQueueStructuralChanges(w, results)
	s.applyQueuedStructuralChanges(queuedChanges)
}
