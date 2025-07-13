package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
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
	CodeNeededFilter         generic.Filter1[components.CodeNeeded]
	FailedInterventionFilter generic.Filter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed] // This field is not used in Update, consider removing if truly unused.
	// lock                     sync.Locker // REMOVED: External lock is not needed for arche when used in a single goroutine.
}

func (s *CodeDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.CodeNeededFilter = *generic.NewFilter1[components.CodeNeeded]().Without(generic.T[components.CodePending]())
	s.FailedInterventionFilter = *generic.NewFilter4[components.InterventionConfig, components.InterventionStatus, components.InterventionJob, components.InterventionFailed]()
	// s.lock = lock // REMOVED
	// w.Mappers.World.IsLocked() // REMOVED: Polling IsLocked() is problematic and unnecessary.
}

// collectWork: Phase 1 - Reads from the world to find code jobs to dispatch.
func (s *CodeDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableCodeJob {
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

// applyWork: Phase 2 - Dispatches jobs and applies structural changes to the world.
func (s *CodeDispatchSystem) applyWork(w *controller.CPRaWorld, dispatchList []dispatchableCodeJob) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, entry := range dispatchList {
		select {
		case s.JobChan <- entry.job.Copy():
			log.Printf("Sent %s code job for entity %v", entry.color, entry.entity)
			// Ensure entity is still alive before making structural changes
			if w.Mappers.World.Alive(entry.entity) {
				w.Mappers.CodeNeeded.Remove(entry.entity)
				w.Mappers.CodePending.Assign(entry.entity, &components.CodePending{Color: entry.color})
			}
		default:
			log.Printf("Job channel full for entity %v", entry.entity)
		}
	}
}

func (s *CodeDispatchSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	dispatchList := s.collectWork(w)
	s.applyWork(w, dispatchList)
}

// CodeResultSystem refactored
type CodeResultSystem struct {
	PendingCodeFilter generic.Filter1[components.CodePending] // This field is not used in Update, consider removing if truly unused.
	ResultChan        <-chan jobs.Result
	// lock              sync.Locker // REMOVED
}

func (s *CodeResultSystem) Initialize(w *controller.CPRaWorld) {
	// s.lock = lock // REMOVED
	// w.Mappers.World.IsLocked() // REMOVED
}

// collectCodeResults: Phase 1.1 - Drains the result channel into a slice.
func (s *CodeResultSystem) collectCodeResults() []resultEntry {
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

// processCodeResultsAndQueueStructuralChanges: Phase 1.2 - Processes results, makes data changes,
// and returns a slice of functions that will perform structural changes.
func (s *CodeResultSystem) processCodeResultsAndQueueStructuralChanges(w *controller.CPRaWorld, results []resultEntry) []func() {
	deferredOps := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result

		if entity.IsZero() || !w.Mappers.World.Alive(entity) { // Check if entity is valid/alive
			continue
		}

		// Get CodePending component
		codePending := w.Mappers.CodePending.Get(entity)
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))

		// Get appropriate CodeStatusAccessor based on color
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

		// Data-only changes are safe to do immediately.
		if res.Error() != nil {
			status.SetFailure(res.Error())
			log.Printf("Monitor %s Code failed\n", *name)
		} else {
			status.SetSuccess(time.Now())
			log.Printf("Monitor %s %q code sent successfully\n", *name, codePending.Color)
		}

		// This is a structural change. Defer it.
		deferredOps = append(deferredOps, func(e ecs.Entity) func() {
			return func() {
				if w.Mappers.World.Alive(e) {
					w.Mappers.CodePending.Remove(e)
				}
			}
		}(entity))
	}
	return deferredOps
}

// applyCodeQueuedStructuralChanges: Phase 2 - Executes all the queued structural changes at once.
func (s *CodeResultSystem) applyCodeQueuedStructuralChanges(deferredOps []func()) {
	// REMOVED: for w.Mappers.World.IsLocked() {}.
	for _, op := range deferredOps {
		op()
	}
}

func (s *CodeResultSystem) Update(w *controller.CPRaWorld) {
	// Main update method calls the two phases.
	results := s.collectCodeResults()
	queuedChanges := s.processCodeResultsAndQueueStructuralChanges(w, results)
	s.applyCodeQueuedStructuralChanges(queuedChanges)
}
