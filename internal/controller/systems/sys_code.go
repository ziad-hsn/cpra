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

/* ---------------------------  DISPATCH  --------------------------- */

type dispatchableCodeJob struct {
	entity ecs.Entity
	job    jobs.Job
	color  string
}

type CodeDispatchSystem struct {
	JobChan          chan<- jobs.Job
	CodeNeededFilter *generic.Filter1[components.CodeNeeded]
}

func (s *CodeDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.CodeNeededFilter = generic.NewFilter1[components.CodeNeeded]().
		Without(generic.T[components.CodePending]())
}

func (s *CodeDispatchSystem) collectWork(w *controller.CPRaWorld) []dispatchableCodeJob {
	out := make([]dispatchableCodeJob, 0)
	query := s.CodeNeededFilter.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		color := string([]byte(query.Get().Color))
		var job jobs.Job

		switch color {
		case "red":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
				job = (*w.Mappers.RedCodeJob.Get(ent)).Job.Copy()
			}
		case "green":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
				job = (*w.Mappers.GreenCodeJob.Get(ent)).Job.Copy()
			}
		case "yellow":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				job = (*w.Mappers.YellowCodeJob.Get(ent)).Job.Copy()
			}
		case "cyan":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
				job = (*w.Mappers.CyanCodeJob.Get(ent)).Job.Copy()
			}
		case "gray":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.GrayCode](w.Mappers.World)) {
				job = (*w.Mappers.GrayCodeJob.Get(ent)).Job.Copy()
			}
		default:
			log.Printf("Unknown color %q for entity %v", color, ent)
		}

		if job != nil {
			out = append(out, dispatchableCodeJob{entity: ent, job: job, color: color})
		}
	}
	return out
}

func (s *CodeDispatchSystem) applyWork(w *controller.CPRaWorld, list []dispatchableCodeJob) []func() {
	deferred := make([]func(), 0, len(list))

	for _, item := range list {
		select {
		case s.JobChan <- item.job.Copy():
			e := item.entity
			c := item.color

			deferred = append(deferred, func() {
				if w.IsAlive(e) {
					w.Mappers.CodeNeeded.Remove(e)
					cp := new(components.CodePending)
					cp.Color = c
					w.Mappers.CodePending.Assign(e, cp)
				}
			})

			log.Printf("Sent %s code job for entity %v", c, e)

		default:
			log.Printf("Job channel full for entity %v", item.entity)
		}
	}
	return deferred
}

func (s *CodeDispatchSystem) Update(w *controller.CPRaWorld) []func() {
	return s.applyWork(w, s.collectWork(w))
}

/* ---------------------------  RESULT  --------------------------- */

type CodeResultSystem struct {
	ResultChan <-chan jobs.Result
}

func (s *CodeResultSystem) Initialize(w *controller.CPRaWorld) {}

func (s *CodeResultSystem) collectCodeResults() map[ecs.Entity]resultEntry {
	out := make(map[ecs.Entity]resultEntry)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			out[res.Entity()] = resultEntry{entity: res.Entity(), result: res}
		default:
			break loop
		}
	}
	return out
}

func (s *CodeResultSystem) processCodeResultsAndQueueStructuralChanges(
	w *controller.CPRaWorld, results map[ecs.Entity]resultEntry,
) []func() {

	deferred := make([]func(), 0, len(results))

	for _, entry := range results {
		entity := entry.entity
		res := entry.result

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodePending](w.Mappers.World)) {
			continue
		}

		name := string([]byte(*w.Mappers.Name.Get(entity)))
		codeColor := string([]byte(w.Mappers.CodePending.Get(entity).Color))

		fmt.Printf("entity is %v for %s code result.\n", entity, name)

		switch codeColor {
		case "red", "green", "yellow", "cyan", "gray":
			var statusCopy components.CodeStatusAccessor
			switch codeColor {
			case "red":
				statusCopy = (*w.Mappers.RedCodeStatus.Get(entity)).Copy()
			case "green":
				statusCopy = (*w.Mappers.GreenCodeStatus.Get(entity)).Copy()
			case "yellow":
				statusCopy = (*w.Mappers.YellowCodeStatus.Get(entity)).Copy()
			case "cyan":
				statusCopy = (*w.Mappers.CyanCodeStatus.Get(entity)).Copy()
			case "gray":
				statusCopy = (*w.Mappers.GrayCodeStatus.Get(entity)).Copy()
			}

			if res.Error() != nil {
				statusCopy.SetFailure(res.Error())
				log.Printf("Monitor %s Code failed\n", name)
			} else {
				statusCopy.SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}

			// capture copies for deferred Set
			e := entity
			switch codeColor {
			case "red":
				st := *(statusCopy.(*components.RedCodeStatus))
				statusToSet := st
				deferred = append(deferred, func() {
					mapper := generic.NewMap[components.RedCodeStatus](w.Mappers.World)
					mapper.Set(e, &statusToSet)
					w.Mappers.CodePending.Remove(e)
				})
			case "green":
				st := *(statusCopy.(*components.GreenCodeStatus))
				statusToSet := st
				deferred = append(deferred, func() {
					mapper := generic.NewMap[components.GreenCodeStatus](w.Mappers.World)
					mapper.Set(e, &statusToSet)
					w.Mappers.CodePending.Remove(e)
				})
			case "yellow":
				st := *(statusCopy.(*components.YellowCodeStatus))
				statusToSet := st
				deferred = append(deferred, func() {
					mapper := generic.NewMap[components.YellowCodeStatus](w.Mappers.World)
					mapper.Set(e, &statusToSet)
					w.Mappers.CodePending.Remove(e)
				})
			case "cyan":
				st := *(statusCopy.(*components.CyanCodeStatus))
				statusToSet := st
				deferred = append(deferred, func() {
					mapper := generic.NewMap[components.CyanCodeStatus](w.Mappers.World)
					mapper.Set(e, &statusToSet)
					w.Mappers.CodePending.Remove(e)
				})
			case "gray":
				st := *(statusCopy.(*components.GrayCodeStatus))
				statusToSet := st
				deferred = append(deferred, func() {
					mapper := generic.NewMap[components.GrayCodeStatus](w.Mappers.World)
					mapper.Set(e, &statusToSet)
					w.Mappers.CodePending.Remove(e)
				})
			}

		default:
			log.Printf("Unknown codeColor %q for entity %v", codeColor, entity)
		}
	}
	return deferred
}

func (s *CodeResultSystem) Update(w *controller.CPRaWorld) []func() {
	return s.processCodeResultsAndQueueStructuralChanges(w, s.collectCodeResults())
}
