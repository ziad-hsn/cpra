package systems

import (
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionResultSystem processes intervention results exactly like sys_intervention.go
type BatchInterventionResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
	logger     Logger
}

// NewBatchInterventionResultSystem creates a new batch intervention result system using the original approach
func NewBatchInterventionResultSystem(resultChan <-chan jobs.Result, mapper *entities.EntityManager, logger Logger) *BatchInterventionResultSystem {
	return &BatchInterventionResultSystem{
		ResultChan: resultChan,
		Mapper:     mapper,
		logger:     logger,
	}
}

// Initialize initializes the system exactly like sys_intervention.go
func (birs *BatchInterventionResultSystem) Initialize(w *ecs.World) {
	// Nothing to initialize - original system doesn't either
}

// collectInterventionResults collects results exactly like sys_intervention.go
func (birs *BatchInterventionResultSystem) collectInterventionResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res, ok := <-birs.ResultChan:
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

// processInterventionResultsAndQueueStructuralChanges processes results exactly like sys_intervention.go
func (birs *BatchInterventionResultSystem) processInterventionResultsAndQueueStructuralChanges(
	w *ecs.World, results map[ecs.Entity]jobs.Result,
) {
	for _, res := range results {
		entity := res.Entity()

		if !w.Alive(entity) || !birs.Mapper.InterventionPending.HasAll(entity) {
			continue
		}

		namePtr := birs.Mapper.Name.Get(entity)
		if namePtr == nil {
			birs.logger.Warn("Entity %d has nil name component", entity.ID())
			continue
		}
		name := *namePtr

		if res.Error() != nil {
			// ---- FAILURE - exact same logic as sys_intervention.go ----
			maxFailures := birs.Mapper.InterventionConfig.Get(entity).MaxFailures
			statusCopy := birs.Mapper.InterventionStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++

			birs.logger.Debug("Monitor %s failed (attempt %d/%d): %v",
				name, statusCopy.ConsecutiveFailures, maxFailures, res.Error())
			birs.logger.Info("Monitor %s intervention failed (attempt %d/%d)", name, statusCopy.ConsecutiveFailures, maxFailures)

			if maxFailures <= statusCopy.ConsecutiveFailures {
				if birs.Mapper.RedCode.HasAll(entity) {
					birs.logger.Info("Monitor %s intervention failed completely - triggering red critical code", name)
					if !birs.Mapper.CodeNeeded.HasAll(entity) {
						birs.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "red"})
						birs.logger.LogComponentState(entity.ID(), "CodeNeeded", "red added")
					}
				}
			} else {
				birs.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
				birs.logger.LogComponentState(entity.ID(), "InterventionNeeded", "added")
			}

		} else {
			// ---- SUCCESS - exact same logic as sys_intervention.go ----
			statusCopy := birs.Mapper.InterventionStatus.Get(entity)

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()

			birs.logger.Debug("Monitor %s intervention successful", name)
			birs.logger.Info("Monitor %s intervention succeeded", name)

			// Trigger cyan code when intervention succeeds (interventions are only triggered when needed)
			if birs.Mapper.CyanCode.HasAll(entity) {
				birs.logger.Info("Monitor %s intervention succeeded - triggering cyan success code", name)
				if !birs.Mapper.CodeNeeded.HasAll(entity) {
					birs.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "cyan"})
					birs.logger.LogComponentState(entity.ID(), "CodeNeeded", "cyan added")
				}
			}
		}

		// Always remove pending after processing - exact same as original
		birs.Mapper.InterventionPending.Remove(entity)
		birs.logger.LogComponentState(entity.ID(), "InterventionPending", "removed")
	}
}

// Update processes results exactly like sys_intervention.go
func (birs *BatchInterventionResultSystem) Update(w *ecs.World) {
	results := birs.collectInterventionResults()
	birs.processInterventionResultsAndQueueStructuralChanges(w, results)
}

// Finalize cleans up like the original system
func (birs *BatchInterventionResultSystem) Finalize(w *ecs.World) {
	// Nothing to clean up like original
}
