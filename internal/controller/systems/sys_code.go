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

type dispatchableCodeJob struct {
	entity ecs.Entity
	job    jobs.Job
	color  string
}

type CodeDispatchSystem struct {
	JobChan                  chan<- jobs.Job
	CodeNeededFilter         *generic.Filter1[components.CodeNeeded]
	FailedInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]
}

func (s *CodeDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.CodeNeededFilter = generic.NewFilter1[components.CodeNeeded]().Without(generic.T[components.CodePending]())
	s.FailedInterventionFilter = *generic.NewFilter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]()
}

// collectWork: Phase 1 - Reads from the world to find code jobs to dispatch.
func (s *CodeDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableCodeJob {
	toDispatch := make([]dispatchableCodeJob, 0)
	query := s.CodeNeededFilter.Query(w.Mappers.World)
	for query.Next() {
		entity := query.Entity()
		codeNeeded := *query.Get()
		var job jobs.Job
		switch codeNeeded.Color {
		case "red":
			if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
				job = w.Mappers.RedCodeJob.GetUnchecked(entity).Job.Copy()

			}
		case "green":
			if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
				job = w.Mappers.GreenCodeJob.GetUnchecked(entity).Job.Copy()
			}
		case "yellow":
			if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				job = w.Mappers.YellowCodeJob.GetUnchecked(entity).Job.Copy()
			}
		case "cyan":
			if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
				job = w.Mappers.CyanCodeJob.GetUnchecked(entity).Job.Copy()
			}
		case "gray":
			if w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.GrayCode](w.Mappers.World)) {
				job = w.Mappers.GrayCodeJob.GetUnchecked(entity).Job.Copy()

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

// applyWork: Phase 2 - Dispatches jobs and prepares deferred structural changes.
func (s *CodeDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchableCodeJob) []func() {
	var deferredOps []func()
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.job.Copy():
			log.Printf("Sent %s code job for entity %v", entry.color, entry.entity)
			deferredOps = append(deferredOps, func(e ecs.Entity, c string) func() {
				return func() {
					if !e.IsZero() {
						w.Mappers.CodeNeeded.Remove(e)
						w.Mappers.CodePending.Assign(e, &components.CodePending{Color: c})
					}
				}
			}(entry.entity, entry.color))
		default:
			log.Printf("Job channel full for entity %v", entry.entity)
		}
	}
	return deferredOps
}

func (s *CodeDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	dispatchList := s.collectWork(w)
	return s.applyWork(w, dispatchList)
}

// CodeResultSystem refactored
type CodeResultSystem struct {
	PendingCodeFilter generic.Filter1[components.CodePending]
	ResultChan        <-chan jobs.Result
}

func (s *CodeResultSystem) Initialize(w *controller.CPRaWorld) {
}

// collectCodeResults: Phase 1.1 - Drains the result channel into a slice.
func (s *CodeResultSystem) collectCodeResults() map[ecs.Entity]resultEntry {
	toProcess := make(map[ecs.Entity]resultEntry)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			toProcess[ent] = resultEntry{entity: ent, result: res}
		default:
			break loop
		}
	}
	return toProcess
}

// processCodeResultsAndQueueStructuralChanges: Phase 1.2 - Processes results, makes data changes,
// and returns a slice of functions that will perform structural changes.
func (s *CodeResultSystem) processCodeResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results map[ecs.Entity]resultEntry) []func() {
	deferredOps := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result
		fmt.Printf("entity is %v for code result.\n", entity)

		//monitor := controller.NewMonitorAdapter(w, entity)
		if !w.IsAlive(entity) || !w.Mappers.World.HasUnchecked(entity, ecs.ComponentID[components.CodePending](w.Mappers.World)) {
			continue
		}

		name := string(append([]byte(nil), []byte(*w.Mappers.Name.GetUnchecked(entity))...))

		codeColor := string(append([]byte(nil), []byte(w.Mappers.CodePending.GetUnchecked(entity).Color)...))

		var status components.CodeStatusAccessor
		switch codeColor {
		case "red":
			status = w.Mappers.RedCodeStatus.GetUnchecked(entity).Copy()
		case "green":
			status = w.Mappers.GreenCodeStatus.GetUnchecked(entity).Copy()
		case "yellow":
			status = w.Mappers.YellowCodeStatus.GetUnchecked(entity).Copy()
		case "cyan":
			status = w.Mappers.CyanCodeStatus.GetUnchecked(entity).Copy()
		case "gray":
			status = w.Mappers.GrayCodeStatus.GetUnchecked(entity).Copy()
		default:
			log.Printf("Unknown codeColor %q for entity %v", codeColor, entity)
			continue
		}

		if res.Error() != nil {
			status.SetFailure(res.Error())
			log.Printf("Monitor %s Code failed\n", name)
		} else {
			status.SetSuccess(time.Now())
			log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
		}
		switch codeColor {
		case "red":
			statusMapper := generic.NewMap[components.RedCodeStatus](w.Mappers.World)
			statusMapper.Set(entity, status.(*components.RedCodeStatus))
		case "green":
			statusMapper := generic.NewMap[components.GreenCodeStatus](w.Mappers.World)
			statusMapper.Set(entity, status.(*components.GreenCodeStatus))
		case "yellow":
			statusMapper := generic.NewMap[components.YellowCodeStatus](w.Mappers.World)
			statusMapper.Set(entity, status.(*components.YellowCodeStatus))
		case "cyan":
			statusMapper := generic.NewMap[components.CyanCodeStatus](w.Mappers.World)
			statusMapper.Set(entity, status.(*components.CyanCodeStatus))
		case "gray":
			statusMapper := generic.NewMap[components.GrayCodeStatus](w.Mappers.World)
			statusMapper.Set(entity, status.(*components.GrayCodeStatus))
		default:
			log.Printf("Unknown codeColor %q for entity %v", codeColor, entity)
			continue
		}

		deferredOps = append(deferredOps, func() {
			if !w.IsAlive(entity) {
				w.Mappers.CodePending.Remove(entity)
			}
		})

	}
	return deferredOps
}

func (s *CodeResultSystem) Update(w *controller.CPRaWorld) []func() {
	results := s.collectCodeResults()
	return s.processCodeResultsAndQueueStructuralChanges(w, results)
}
