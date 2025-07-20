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

type dispatchableIntervention struct {
	Entity ecs.Entity
	Job    jobs.Job
}

type InterventionDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	InterventionNeededFilter generic.Filter2[components.InterventionJob, components.InterventionNeeded]
}

func (s *InterventionDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.InterventionNeededFilter = *generic.NewFilter2[components.InterventionJob, components.InterventionNeeded]().Without(generic.T[components.InterventionPending]())
}

// collectWork: Phase 1 - Reads from the world to find interventions to dispatch.
func (s *InterventionDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableIntervention {
	toDispatch := make([]dispatchableIntervention, 0)
	query := s.InterventionNeededFilter.Query(w.Mappers.World)
	for query.Next() {
		job, _ := query.Get()
		j := job.Job.Copy()
		entity := query.Entity()
		toDispatch = append(toDispatch, dispatchableIntervention{
			Entity: entity,
			Job:    j,
		})
	}
	return toDispatch
}

// applyWork: Phase 2 - Dispatches jobs and prepares deferred structural changes.
func (s *InterventionDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchableIntervention) []func() {
	var deferredOps []func()
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.Job:
			e := entry.Entity
			deferredOps = append(deferredOps, func() {
				if !e.IsZero() {
					w.Mappers.World.Exchange(e,
						[]ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)},
						[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)})
				}
			})
		default:
			log.Printf("Job channel full for entity %v\n", entry.Entity)
		}
	}
	return deferredOps
}

func (s *InterventionDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	dispatchList := s.collectWork(w)
	return s.applyWork(w, dispatchList)
}

// InterventionResultSystem --- RESULT PROCESS SYSTEM ---
type InterventionResultSystem struct {
	PendingInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionNeeded]
	ResultChan                <-chan jobs.Result
}

func (s *InterventionResultSystem) Initialize(w *controller.CPRaWorld) {
}

// collectInterventionResults: Phase 1.1 - Drains the result channel into a slice.
func (s *InterventionResultSystem) collectInterventionResults() []resultEntry {
	toProcess := make([]resultEntry, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			toProcess = append(toProcess, resultEntry{entity: ent, result: res})
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

		fmt.Printf("entity is %v for pulse result.\n", entity)

		if entity.IsZero() {
			continue
		}

		if !w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.InterventionPending](w.Mappers.World)) {
			continue
		}

		config, status := w.Mappers.Intervention.GetUnchecked(entity)
		name := *w.Mappers.Name.GetUnchecked(entity)

		if res.Error() != nil {
			status.LastStatus = "failed"
			status.LastError = res.Error()
			status.ConsecutiveFailures++

			if config.MaxFailures <= status.ConsecutiveFailures {
				log.Printf("Monitor %s intervention failed\n", name)
				if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
					log.Println("scheduling red code")
					if !w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.CodeNeeded](w.Mappers.World)) {
						deferredOps = append(deferredOps, func(e ecs.Entity) func() {
							return func() {
								if !e.IsZero() {
									w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "red"})
								}
							}
						}(entity))
					}
				}
			} else {
				deferredOps = append(deferredOps, func(e ecs.Entity) func() {
					return func() {
						if !e.IsZero() {
							w.Mappers.World.Exchange(e,
								[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](w.Mappers.World)},
								[]ecs.ID{ecs.ComponentID[components.InterventionPending](w.Mappers.World)})
						}
					}
				}(entity))
			}
		} else {
			lastStatus := status.LastStatus
			status.LastStatus = "success"
			status.LastError = nil
			status.ConsecutiveFailures = 0
			status.LastSuccessTime = time.Now()

			if lastStatus == "failed" {
				if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
					log.Printf("Monitor %s intervention succeeded and needs cyan code\n", name)
					deferredOps = append(deferredOps, func(e ecs.Entity) func() {
						return func() {
							if !e.IsZero() {
								w.Mappers.CodeNeeded.Assign(e, &components.CodeNeeded{Color: "cyan"})
							}
						}
					}(entity))
				}
			}
			deferredOps = append(deferredOps, func(e ecs.Entity) func() {
				return func() {
					if !e.IsZero() {
						w.Mappers.InterventionPending.Remove(e)
					}
				}
			}(entity))
		}
	}
	return deferredOps
}

func (s *InterventionResultSystem) Update(w *controller.CPRaWorld) []func() {
	results := s.collectInterventionResults()
	return s.processInterventionResultsAndQueueStructuralChanges(w, results)
}

func GetEntityComponents(w *ecs.World, entity ecs.Entity) []string {
	ids := w.Ids(entity)
	var componentNames []string
	for _, id := range ids {
		info, _ := ecs.ComponentInfo(w, id)
		compType := info.Type
		componentNames = append(componentNames, compType.Name())
	}
	return componentNames
}
