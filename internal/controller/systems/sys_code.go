package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"log"
	"strings"
	"time"
)

/* ---------------------------  DISPATCH  --------------------------- */

type dispatchableCodeJob struct {
	job   jobs.Job
	color string
}

type CodeDispatchSystem struct {
	JobChan          chan<- jobs.Job
	CodeNeededFilter *generic.Filter1[components.CodeNeeded]
}

func (s *CodeDispatchSystem) Initialize(w *controller.CPRaWorld) {
	s.CodeNeededFilter = generic.NewFilter1[components.CodeNeeded]().
		Without(generic.T[components.CodePending]())
}

func (s *CodeDispatchSystem) collectWork(w *controller.CPRaWorld) map[ecs.Entity]dispatchableCodeJob {
	out := make(map[ecs.Entity]dispatchableCodeJob)
	query := s.CodeNeededFilter.Query(w.Mappers.World)

	for query.Next() {
		ent := query.Entity()
		color := string([]byte(query.Get().Color))
		var job jobs.Job

		switch color {
		case "red":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.RedCode](w.Mappers.World)) {
				job = w.Mappers.RedCodeJob.Get(ent).Job
			}
		case "green":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
				job = w.Mappers.GreenCodeJob.Get(ent).Job
			}
		case "yellow":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
				job = w.Mappers.YellowCodeJob.Get(ent).Job
			}
		case "cyan":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.CyanCode](w.Mappers.World)) {
				job = w.Mappers.CyanCodeJob.Get(ent).Job
			}
		case "gray":
			if w.Mappers.World.Has(ent, ecs.ComponentID[components.GrayCode](w.Mappers.World)) {
				job = w.Mappers.GrayCodeJob.Get(ent).Job
			}
		default:
			log.Printf("Unknown color %q for entity %v", color, ent)
		}

		if job != nil {
			out[ent] = dispatchableCodeJob{job: job, color: color}
		}
	}
	return out
}

func (s *CodeDispatchSystem) applyWork(w *controller.CPRaWorld, list map[ecs.Entity]dispatchableCodeJob, commandBuffer *CommandBufferSystem) {

	for e, item := range list {
		select {
		case s.JobChan <- item.job:

			if w.IsAlive(e) {
				commandBuffer.MarkCodePending(e, item.color)
			}

			log.Printf("Sent %s code job for entity %v", item.color, e)

		default:
			log.Printf("Job channel full for entity %v", e)
		}
	}
}

func (s *CodeDispatchSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch, cb)
}

/* ---------------------------  RESULT  --------------------------- */

type CodeResultSystem struct {
	ResultChan <-chan jobs.Result
}

func (s *CodeResultSystem) Initialize(w *controller.CPRaWorld) {}

func (s *CodeResultSystem) collectCodeResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			out[res.Entity()] = res
		default:
			break loop
		}
	}
	return out
}

func (s *CodeResultSystem) processCodeResultsAndQueueStructuralChanges(
	w *controller.CPRaWorld, results map[ecs.Entity]jobs.Result, commandBuffer *CommandBufferSystem,
) {
	for entity, res := range results {

		if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.CodePending](w.Mappers.World)) {
			continue
		}

		name := strings.Clone(string(*w.Mappers.Name.Get(entity)))
		codeColor := strings.Clone(w.Mappers.CodePending.Get(entity).Color)

		switch codeColor {
		case "red":
			cur := w.Mappers.RedCodeStatus.Get(entity)
			var st components.RedCodeStatus
			if cur != nil {
				st = *cur // copy by value
			}
			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				log.Printf("Monitor %s Code failed: %v\n", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}
			commandBuffer.setRedCodeStatus(entity, st)
			commandBuffer.RemoveCodePending(entity)

		case "green":
			cur := w.Mappers.GreenCodeStatus.Get(entity)
			var st components.GreenCodeStatus
			if cur != nil {
				st = *cur
			}
			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				log.Printf("Monitor %s Code failed: %v\n", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}
			commandBuffer.setGreenCodeStatus(entity, st)
			commandBuffer.RemoveCodePending(entity)

		case "yellow":
			cur := w.Mappers.YellowCodeStatus.Get(entity)
			var st components.YellowCodeStatus
			if cur != nil {
				st = *cur
			}
			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				log.Printf("Monitor %s Code failed: %v\n", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}
			commandBuffer.setYellowCodeStatus(entity, st)
			commandBuffer.RemoveCodePending(entity)

		case "cyan":
			cur := w.Mappers.CyanCodeStatus.Get(entity)
			var st components.CyanCodeStatus
			if cur != nil {
				st = *cur
			}
			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				log.Printf("Monitor %s Code failed: %v\n", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}
			commandBuffer.setCyanCodeStatus(entity, st)
			commandBuffer.RemoveCodePending(entity)

		case "gray":
			cur := w.Mappers.GrayCodeStatus.Get(entity)
			var st components.GrayCodeStatus
			if cur != nil {
				st = *cur
			}
			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				log.Printf("Monitor %s Code failed: %v\n", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				log.Printf("Monitor %s %q code sent successfully\n", name, codeColor)
			}
			commandBuffer.setGrayCodeStatus(entity, st)
			commandBuffer.RemoveCodePending(entity)

		default:
			log.Printf("Unknown codeColor %q for entity %v", codeColor, entity)
		}
	}
}

func (s *CodeResultSystem) Update(w *controller.CPRaWorld, cb *CommandBufferSystem) {
	results := s.collectCodeResults()
	s.processCodeResultsAndQueueStructuralChanges(w, results, cb)
}
