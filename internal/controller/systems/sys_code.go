package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"
	"github.com/mlange-42/ark/ecs"
	"time"
)

/* ---------------------------  DISPATCH  --------------------------- */

type dispatchableCodeJob struct {
	job   jobs.Job
	color string
}

type CodeDispatchSystem struct {
	JobChan          chan<- jobs.Job
	CodeNeededFilter *ecs.Filter1[components.CodeNeeded]
	Mapper           *entities.EntityManager
	QueueManager     *queue.QueueManager
}

func (s *CodeDispatchSystem) Initialize(w *ecs.World) {
	s.CodeNeededFilter = ecs.NewFilter1[components.CodeNeeded](w).
		Without(ecs.C[components.CodePending]())
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *CodeDispatchSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchableCodeJob {
	start := time.Now()
	out := make(map[ecs.Entity]dispatchableCodeJob)
	query := s.CodeNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		color := query.Get().Color
		var job jobs.Job

		switch color {
		case "red":
			if s.Mapper.RedCode.HasAll(ent) {
				job = s.Mapper.RedCodeJob.Get(ent).Job
			}
		case "green":
			if s.Mapper.GreenCode.HasAll(ent) {
				job = s.Mapper.GreenCodeJob.Get(ent).Job
			}
		case "yellow":
			if s.Mapper.YellowCode.HasAll(ent) {
				job = s.Mapper.YellowCodeJob.Get(ent).Job
			}
		case "cyan":
			if s.Mapper.CyanCode.HasAll(ent) {
				job = s.Mapper.CyanCodeJob.Get(ent).Job
			}
		case "gray":
			if s.Mapper.GrayCode.HasAll(ent) {
				job = s.Mapper.GrayCodeJob.Get(ent).Job
			}
		default:
			controller.DispatchLogger.Warn("Unknown color %q for entity %v", color, ent)
		}

		if job != nil {
			out[ent] = dispatchableCodeJob{job: job, color: color}
		}
	}
	controller.DispatchLogger.LogSystemPerformance("CodeDispatch", time.Since(start), len(out))
	return out
}

func (s *CodeDispatchSystem) applyWork(w *ecs.World, list map[ecs.Entity]dispatchableCodeJob) {
	for e, item := range list {
		if w.Alive(e) {
			// Prevent component duplication
			if s.Mapper.CodePending.HasAll(e) {
				namePtr := s.Mapper.Name.Get(e)
				if namePtr != nil {
					controller.DispatchLogger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %v", *namePtr, e)
				}
				continue
			}

			// Enqueue with deduplication
			err := s.QueueManager.EnqueueCode(e, item.job)
			if err != nil {
				controller.DispatchLogger.Warn("Failed to enqueue code for entity %d: %v", e, err)
				continue
			}

			// Safe component transition
			if s.Mapper.CodeNeeded.HasAll(e) {
				s.Mapper.CodeNeeded.Remove(e)
				s.Mapper.CodePending.Add(e, &components.CodePending{Color: item.color})

				namePtr := s.Mapper.Name.Get(e)
				if namePtr != nil {
					controller.DispatchLogger.Debug("Dispatched %s code job for entity: %d", item.color, e.ID())
				}
				controller.DispatchLogger.LogComponentState(e.ID(), "CodeNeeded->CodePending", "transitioned")
			}
		}

	}
}

func (s *CodeDispatchSystem) Update(w *ecs.World) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch)
}

func (s *CodeDispatchSystem) Finalize(w *ecs.World) {
	//close(s.JobChan)
}

/* ---------------------------  RESULT  --------------------------- */

type CodeResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
}

func (s *CodeResultSystem) Initialize(w *ecs.World) {
	//s.Mapper = entities.InitializeMappers(w)
}

func (s *CodeResultSystem) collectCodeResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res, ok := <-s.ResultChan:
			if !ok {
				break loop
			}
			out[res.Entity()] = res
		default:
			break loop
		}
	}
	return out
}

func (s *CodeResultSystem) processCodeResultsAndQueueStructuralChanges(
	w *ecs.World, results map[ecs.Entity]jobs.Result,
) {
	for entity, res := range results {

		if !w.Alive(entity) || !s.Mapper.CodePending.HasAll(entity) {
			continue
		}

		name := *s.Mapper.Name.Get(entity)
		codeColor := s.Mapper.CodePending.Get(entity).Color

		switch codeColor {
		case "red":
			st := *s.Mapper.RedCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				controller.ResultLogger.Error("Monitor %s Code failed: %v", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				controller.ResultLogger.Info("Monitor %s %q code sent successfully", name, codeColor)
			}
			s.Mapper.RedCodeStatus.Set(entity, &st)
			s.Mapper.CodePending.Remove(entity)
			controller.ResultLogger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "green":
			st := *s.Mapper.GreenCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				controller.ResultLogger.Error("Monitor %s Code failed: %v", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				controller.ResultLogger.Info("Monitor %s %q code sent successfully", name, codeColor)
			}
			s.Mapper.GreenCodeStatus.Set(entity, &st)
			s.Mapper.CodePending.Remove(entity)
			controller.ResultLogger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "yellow":
			st := *s.Mapper.YellowCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				controller.ResultLogger.Error("Monitor %s Code failed: %v", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				controller.ResultLogger.Info("Monitor %s %q code sent successfully", name, codeColor)
			}
			s.Mapper.YellowCodeStatus.Set(entity, &st)
			s.Mapper.CodePending.Remove(entity)
			controller.ResultLogger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "cyan":
			st := *s.Mapper.CyanCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				controller.ResultLogger.Error("Monitor %s Code failed: %v", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				controller.ResultLogger.Info("Monitor %s %q code sent successfully", name, codeColor)
			}
			s.Mapper.CyanCodeStatus.Set(entity, &st)
			s.Mapper.CodePending.Remove(entity)
			controller.ResultLogger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "gray":
			st := *s.Mapper.GrayCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				controller.ResultLogger.Error("Monitor %s Code failed: %v", name, err)
			} else {
				(&st).SetSuccess(time.Now())
				controller.ResultLogger.Info("Monitor %s %q code sent successfully", name, codeColor)
			}
			s.Mapper.GrayCodeStatus.Set(entity, &st)
			s.Mapper.CodePending.Remove(entity)
			controller.ResultLogger.LogComponentState(entity.ID(), "CodePending", "removed")

		default:
			controller.ResultLogger.Warn("Unknown codeColor %q for entity %v", codeColor, entity)
		}
	}
}

func (s *CodeResultSystem) Update(w *ecs.World) {
	results := s.collectCodeResults()
	s.processCodeResultsAndQueueStructuralChanges(w, results)
}

func (s *CodeResultSystem) Finalize(w *ecs.World) {}
