package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionResultSystem processes completed intervention jobs.
// It processes batches of results passed directly from the result router.
type BatchInterventionResultSystem struct {
	world  *ecs.World
	logger Logger

	// Mappers for efficient component access
	stateMapper *ecs.Map[components.MonitorState]
	ResultChan  <-chan []jobs.Result
}

// NewBatchInterventionResultSystem creates a new BatchInterventionResultSystem.
func NewBatchInterventionResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger) *BatchInterventionResultSystem {
	return &BatchInterventionResultSystem{
		world:       world,
		logger:      logger,
		stateMapper: ecs.NewMap[components.MonitorState](world),
		ResultChan:  results,
	}
}

func (s *BatchInterventionResultSystem) Initialize(w *ecs.World) {
}

func (s *BatchInterventionResultSystem) Update(w *ecs.World) {
	if s.ResultChan == nil {
		return
	}

	resultsBatches := make([][]jobs.Result, 0)
loop:
	for {
		select {
		case res, ok := <-s.ResultChan:
			if !ok {
				s.ResultChan = nil
				break loop
			}
			if len(res) == 0 {
				continue
			}
			resultsBatches = append(resultsBatches, res)
		default:
			break loop
		}
	}

	for _, res := range resultsBatches {
		s.ProcessBatch(res)
	}
}

// ProcessBatch processes a batch of intervention results.
func (s *BatchInterventionResultSystem) ProcessBatch(results []jobs.Result) {
	startTime := time.Now()
	processedCount := 0

	for _, result := range results {
		ent := result.Entity()
		if !s.world.Alive(ent) {
			continue
		}

		state := s.stateMapper.Get(ent)
		if state == nil {
			continue
		}

		// Ensure we are processing a pending intervention
		if (atomic.LoadUint32(&state.Flags) & components.StateInterventionPending) == 0 {
			s.logger.Warn("Entity[%d] received InterventionResult but was not in InterventionPending state", ent.ID())
			continue
		}

		processedCount++
		state.LastCheckTime = time.Now()

		if result.Error() != nil {
			// --- FAILURE ---
			state.ConsecutiveFailures++
			state.LastError = result.Error()
			s.logger.Error("Monitor '%s' intervention failed: %v", state.Name, state.LastError)
			s.triggerCode(ent, state, "red")
		} else {
			// --- SUCCESS ---
			s.logger.Info("Monitor '%s' intervention succeeded.", state.Name)
			state.ConsecutiveFailures = 0
			state.LastError = nil
			state.LastSuccessTime = state.LastCheckTime
			s.triggerCode(ent, state, "cyan")
		}

		// Unset the pending flag, regardless of outcome.
		atomic.AndUint32(&state.Flags, ^uint32(components.StateInterventionPending))
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchInterventionResultSystem", time.Since(startTime), processedCount)
	}
}

func (s *BatchInterventionResultSystem) triggerCode(entity ecs.Entity, state *components.MonitorState, color string) {
	codeConfigMapper := ecs.NewMap[components.CodeConfig](s.world)
	if !codeConfigMapper.Has(entity) {
		return
	}
	codeConfig := codeConfigMapper.Get(entity)
	if _, ok := codeConfig.Configs[color]; ok {
		state.PendingCode = color
		atomic.OrUint32(&state.Flags, components.StateCodeNeeded)
		s.logger.Info("Monitor '%s' - flagging for %s alert code", state.Name, color)
	}
}

// Finalize is a no-op for this system.
func (s *BatchInterventionResultSystem) Finalize(w *ecs.World) {}
