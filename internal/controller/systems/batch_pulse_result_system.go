package systems

import (
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// BatchPulseResultSystem processes pulse results exactly like sys_pulse.go
type BatchPulseResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
	logger     Logger
}

// NewBatchPulseResultSystem creates a new batch pulse result system using the original approach
func NewBatchPulseResultSystem(resultChan <-chan jobs.Result, mapper *entities.EntityManager, logger Logger) *BatchPulseResultSystem {
	return &BatchPulseResultSystem{
		ResultChan: resultChan,
		Mapper:     mapper,
		logger:     logger,
	}
}

// Initialize initializes the system exactly like sys_pulse.go
func (bprs *BatchPulseResultSystem) Initialize(w *ecs.World) {
	// Nothing to initialize - original system doesn't either
}

// collectPulseResults collects results exactly like sys_pulse.go
func (bprs *BatchPulseResultSystem) collectPulseResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res, ok := <-bprs.ResultChan:
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

// processPulseResultsAndQueueStructuralChanges processes results exactly like sys_pulse.go
func (bprs *BatchPulseResultSystem) processPulseResultsAndQueueStructuralChanges(
	w *ecs.World, results map[ecs.Entity]jobs.Result,
) {
	for _, res := range results {
		entity := res.Entity()

		if !w.Alive(entity) || !bprs.Mapper.PulsePending.HasAll(entity) {
			continue
		}

		namePtr := bprs.Mapper.Name.Get(entity)
		if namePtr == nil {
			bprs.logger.Warn("Entity %d has nil name component", entity.ID())
			continue
		}
		name := *namePtr

		if res.Error() != nil {
			// ---- FAILURE - exact same logic as sys_pulse.go ----
			maxFailures := bprs.Mapper.PulseConfig.Get(entity).MaxFailures
			statusCopy := bprs.Mapper.PulseStatus.Get(entity)

			// DEBUG: Check maxFailures value
			bprs.logger.Debug("DEBUG: maxFailures=%d, current ConsecutiveFailures=%d", maxFailures, statusCopy.ConsecutiveFailures)
			monitorCopy := bprs.Mapper.MonitorStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++
			statusCopy.LastCheckTime = time.Now()

			// Store the updated value for correct logging
			currentFailures := statusCopy.ConsecutiveFailures

			bprs.logger.Debug("Monitor %s failed (attempt %d/%d): %v",
				name, currentFailures, maxFailures, res.Error())

			// yellow code on first failure - EXACT same logic as sys_pulse.go
			if statusCopy.ConsecutiveFailures == 1 &&
				bprs.Mapper.YellowCode.HasAll(entity) {
				bprs.logger.Info("Monitor %s pulse failed - triggering yellow alert code", name)
				if !bprs.Mapper.CodeNeeded.HasAll(entity) {
					bprs.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "yellow"})
					bprs.logger.LogComponentState(entity.ID(), "CodeNeeded", "yellow added")
				}
			}

			// interventions - EXACT same logic as sys_pulse.go but fixed
			if currentFailures > 0 && currentFailures%maxFailures == 0 &&
				bprs.Mapper.InterventionConfig.HasAll(entity) {
				bprs.logger.Warn("Monitor %s failed %d times - triggering intervention", name, currentFailures)
				bprs.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
				monitorCopy.Status = "failed"
				bprs.logger.LogComponentState(entity.ID(), "InterventionNeeded", "added")
			}

			// Always retry on failure - original logic
			if currentFailures < maxFailures {
				bprs.logger.Info("Monitor %s pulse failed - will retry (%d/%d)", name, currentFailures, maxFailures)
				bprs.Mapper.PulseNeeded.Add(entity, &components.PulseNeeded{})
				bprs.logger.LogComponentState(entity.ID(), "PulseNeeded", "added")
			}

		} else {
			// ---- SUCCESS - exact same logic as sys_pulse.go ----
			statusCopy := bprs.Mapper.PulseStatus.Get(entity)
			monitorCopy := bprs.Mapper.MonitorStatus.Get(entity)
			lastStatus := statusCopy.LastStatus
			wasFailure := statusCopy.ConsecutiveFailures > 0

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			statusCopy.LastCheckTime = time.Now()
			monitorCopy.Status = "success"

			if wasFailure {
				bprs.logger.Info("Monitor %s pulse succeeded - recovered after failure", name)
			} else {
				bprs.logger.Info("Monitor %s pulse succeeded", name)
			}

			if lastStatus == "failed" && bprs.Mapper.GreenCode.HasAll(entity) {
				bprs.logger.Info("Monitor %s pulse recovered - triggering green recovery code", name)
				if !bprs.Mapper.CodeNeeded.HasAll(entity) {
					bprs.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "green"})
					bprs.logger.LogComponentState(entity.ID(), "CodeNeeded", "green added")
				}
			}
		}

		// Always remove pending after processing - exact same as original
		bprs.Mapper.PulsePending.Remove(entity)
		bprs.logger.LogComponentState(entity.ID(), "PulsePending", "removed")
	}
}

// Update processes results exactly like sys_pulse.go
func (bprs *BatchPulseResultSystem) Update(w *ecs.World) {
	results := bprs.collectPulseResults()
	bprs.processPulseResultsAndQueueStructuralChanges(w, results)
}

// Finalize cleans up like the original system
func (bprs *BatchPulseResultSystem) Finalize(w *ecs.World) {
	// Nothing to clean up like original
}
