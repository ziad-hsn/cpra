package systems

import (
	"time"

	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// BatchCodeResultSystem processes code results exactly like sys_code.go
type BatchCodeResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
	logger     Logger
}

// NewBatchCodeResultSystem creates a new batch code result system using the original approach
func NewBatchCodeResultSystem(resultChan <-chan jobs.Result, mapper *entities.EntityManager, logger Logger) *BatchCodeResultSystem {
	return &BatchCodeResultSystem{
		ResultChan: resultChan,
		Mapper:     mapper,
		logger:     logger,
	}
}

// Initialize initializes the system exactly like sys_code.go
func (bcrs *BatchCodeResultSystem) Initialize(w *ecs.World) {
	// Nothing to initialize - original system doesn't either
}

// collectCodeResults collects results exactly like sys_code.go
func (bcrs *BatchCodeResultSystem) collectCodeResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res, ok := <-bcrs.ResultChan:
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

// processCodeResultsAndQueueStructuralChanges processes results exactly like sys_code.go
func (bcrs *BatchCodeResultSystem) processCodeResultsAndQueueStructuralChanges(
	w *ecs.World, results map[ecs.Entity]jobs.Result,
) {
	for entity, res := range results {

		if !w.Alive(entity) || !bcrs.Mapper.CodePending.HasAll(entity) {
			continue
		}

		name := *bcrs.Mapper.Name.Get(entity)
		codeColor := bcrs.Mapper.CodePending.Get(entity).Color

		// Exact same color-specific processing as sys_code.go
		switch codeColor {
		case "red":
			st := *bcrs.Mapper.RedCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				bcrs.logger.Error("Monitor %s %q alert code failed: %v", name, codeColor, err)
			} else {
				(&st).SetSuccess(time.Now())
				bcrs.logger.Info("Monitor %s %q alert code sent successfully", name, codeColor)
			}
			bcrs.Mapper.RedCodeStatus.Set(entity, &st)
			bcrs.Mapper.CodePending.Remove(entity)
			bcrs.logger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "green":
			st := *bcrs.Mapper.GreenCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				bcrs.logger.Error("Monitor %s %q alert code failed: %v", name, codeColor, err)
			} else {
				(&st).SetSuccess(time.Now())
				bcrs.logger.Info("Monitor %s %q alert code sent successfully", name, codeColor)
			}
			bcrs.Mapper.GreenCodeStatus.Set(entity, &st)
			bcrs.Mapper.CodePending.Remove(entity)
			bcrs.logger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "yellow":
			st := *bcrs.Mapper.YellowCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				bcrs.logger.Error("Monitor %s %q alert code failed: %v", name, codeColor, err)
			} else {
				(&st).SetSuccess(time.Now())
				bcrs.logger.Info("Monitor %s %q alert code sent successfully", name, codeColor)
			}
			bcrs.Mapper.YellowCodeStatus.Set(entity, &st)
			bcrs.Mapper.CodePending.Remove(entity)
			bcrs.logger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "cyan":
			st := *bcrs.Mapper.CyanCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				bcrs.logger.Error("Monitor %s %q alert code failed: %v", name, codeColor, err)
			} else {
				(&st).SetSuccess(time.Now())
				bcrs.logger.Info("Monitor %s %q alert code sent successfully", name, codeColor)
			}
			bcrs.Mapper.CyanCodeStatus.Set(entity, &st)
			bcrs.Mapper.CodePending.Remove(entity)
			bcrs.logger.LogComponentState(entity.ID(), "CodePending", "removed")

		case "gray":
			st := *bcrs.Mapper.GrayCodeStatus.Get(entity)

			if err := res.Error(); err != nil {
				(&st).SetFailure(err)
				bcrs.logger.Error("Monitor %s %q alert code failed: %v", name, codeColor, err)
			} else {
				(&st).SetSuccess(time.Now())
				bcrs.logger.Info("Monitor %s %q alert code sent successfully", name, codeColor)
			}
			bcrs.Mapper.GrayCodeStatus.Set(entity, &st)
			bcrs.Mapper.CodePending.Remove(entity)
			bcrs.logger.LogComponentState(entity.ID(), "CodePending", "removed")

		default:
			bcrs.logger.Warn("Unknown codeColor %q for entity %v", codeColor, entity)
		}
	}
}

// Update processes results exactly like sys_code.go
func (bcrs *BatchCodeResultSystem) Update(w *ecs.World) {
	results := bcrs.collectCodeResults()
	bcrs.processCodeResultsAndQueueStructuralChanges(w, results)
}

// Finalize cleans up like the original system
func (bcrs *BatchCodeResultSystem) Finalize(w *ecs.World) {
	// Nothing to clean up like original
}
