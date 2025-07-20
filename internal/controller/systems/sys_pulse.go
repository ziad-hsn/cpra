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
	PulseFilter *generic.Filter2[components.PulseConfig, components.PulseStatus]
}

func (s *PulseScheduleSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
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
		entity := query.Entity()
		config := w.Mappers.PulseConfig.GetUnchecked(entity)
		status := w.Mappers.PulseStatus.GetUnchecked(entity)

		// Check for first-time pulse
		if w.Mappers.World.HasUnchecked(query.Entity(), ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
			//status.LastCheckTime = time.Now()
			toCheck = append(toCheck, query.Entity())
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			continue
		}

		// Check for scheduled interval
		if time.Since(status.LastCheckTime) >= config.Interval {
			log.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			//status.LastCheckTime = time.Now()
			toCheck = append(toCheck, query.Entity())
		}
	}
	return toCheck
}

// applyWork: Phase 2 - Prepares deferred structural changes.
func (s *PulseScheduleSystem) applyWork(w *controller.CPRaWorld, entities []ecs.Entity) []func() {
	var deferredOps []func()
	for _, entity := range entities {
		e := entity
		deferredOps = append(deferredOps, func() {
			if !e.IsZero() && !w.Mappers.World.HasUnchecked(e, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
				w.Mappers.PulseNeeded.Assign(e, &components.PulseNeeded{})
			}
		})
	}
	return deferredOps
}

func (s *PulseScheduleSystem) Update(w *controller.CPRaWorld) []func() {
	entitiesToSchedule := s.collectWork(w)
	return s.applyWork(w, entitiesToSchedule)
}

// PulseDispatchSystem refactored
type dispatchablePulse struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded *generic.Filter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]
}

func (s *PulseDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.PulseNeeded = generic.NewFilter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]()
}

// collectWork: Phase 1 - Reads from the world and returns entities ready for dispatch.
func (s *PulseDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchablePulse {
	toDispatch := make([]dispatchablePulse, 0)
	query := s.PulseNeeded.Query(w.Mappers.World)
	for query.Next() {
		entity := query.Entity()
		job := w.Mappers.PulseJob.GetUnchecked(entity)
		status := w.Mappers.PulseStatus.GetUnchecked(entity)

		status.LastCheckTime = time.Now() // Data-only update, safe.

		toDispatch = append(toDispatch, dispatchablePulse{
			Entity: entity,
			Job:    job.Job,
		})
	}
	return toDispatch
}

// applyWork: Phase 2 - Dispatches jobs and prepares deferred structural changes.
func (s *PulseDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchablePulse) []func() {
	var deferredOps []func()
	for _, item := range dispatchList {
		select {
		case s.JobChan <- item.Job.Copy():
			name := *w.Mappers.Name.GetUnchecked(item.Entity)
			log.Printf("sent %s job\n", name)
			e := item.Entity
			if w.Mappers.World.HasUnchecked(e, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				deferredOps = append(deferredOps, func() {
					w.Mappers.PulseFirstCheck.Remove(e)
				})
			}
			deferredOps = append(deferredOps, func() {
				w.Mappers.World.Exchange(e,
					[]ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)},
					[]ecs.ID{ecs.ComponentID[components.PulseNeeded](w.Mappers.World)})
			})
		default:
			log.Printf("Job channel full, skipping dispatch for entity %v", item.Entity)
		}
	}
	return deferredOps
}

func (s *PulseDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	dispatchList := s.collectWork(w)
	return s.applyWork(w, dispatchList)
}

// PulseResultSystem refactored
type resultEntry struct {
	entity ecs.Entity
	result jobs.Result
}

type PulseResultSystem struct {
	PendingPulseFilter *generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
}

func (s *PulseResultSystem) Initialize(w *controller.CPRaWorld) {
}

// collectResults: Phase 1.1 - Drains the result channel into a slice.
func (s *PulseResultSystem) collectResults() []resultEntry {
	toProcess := make([]resultEntry, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			toProcess = append(toProcess, resultEntry{entity: ent, result: res})
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
		monitor := controller.NewMonitorAdapter(w, entry.entity)

		fmt.Printf("entity is %v for %s pulse result.\n", entity, monitor.Name())
		if !monitor.IsAlive() || !w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.PulsePending](w.Mappers.World)) {
			continue
		}

		fmt.Printf("recived %s results\n", monitor.Name())
		if res.Error() != nil {
			monitor.SetPulseStatusAsFailed(res.Error())
			status, _ := monitor.Status()
			config := w.Mappers.PulseConfig.Get(entry.entity)

			if status.ConsecutiveFailures == 1 {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if !e.IsZero() {
								w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "yellow"})
							}
						}
					}(entity))
				}
			}
			if config.MaxFailures <= status.ConsecutiveFailures {
				//monitorStatus.Status = "failed"

				if monitor.HasIntervention() {
					fmt.Printf("Monitor %s failed %d times and needs intervention\n", monitor.Name(), status.ConsecutiveFailures)
					deferredOps = append(deferredOps, func() {
						monitor.ScheduleIntervention()
					})

				}
			}
		} else {
			monitor.SetPulseStatusAsSuccess()

			//if mStatus == "failed" {
			//	if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
			//		deferredOps = append(deferredOps, func(e ecs.Entity) func() {
			//			return func() {
			//				if !e.IsZero() {
			//					w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "green"})
			//				}
			//			}
			//		}(entity))
			//	}
			//}
		}

		deferredOps = append(deferredOps, func() {
			monitor.RemovePendingPulse()
		})
	}
	return deferredOps
}

func (s *PulseResultSystem) Update(w *controller.CPRaWorld) []func() {
	results := s.collectResults()
	return s.processResultsAndQueueStructuralChanges(w, results)
}
