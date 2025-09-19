package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseResultSystem processes completed pulse checks.
// It queries for entities with a PulseResult component and updates their state accordingly.
type BatchPulseResultSystem struct {
	world  *ecs.World
	logger Logger

	// Mappers are used for efficient component access
	stateMapper      *ecs.Map1[components.MonitorState]
	configMapper     *ecs.Map1[components.PulseConfig]
	codeConfigMapper *ecs.Map1[components.CodeConfig]
	ResultChan       <-chan []jobs.Result
}

// NewBatchPulseResultSystem creates a new BatchPulseResultSystem.
func NewBatchPulseResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger) *BatchPulseResultSystem {
	return &BatchPulseResultSystem{
		world:            world,
		logger:           logger,
		stateMapper:      ecs.NewMap1[components.MonitorState](world),
		configMapper:     ecs.NewMap1[components.PulseConfig](world),
		codeConfigMapper: ecs.NewMap1[components.CodeConfig](world),
		ResultChan:       results,
	}
}
func (s *BatchPulseResultSystem) Initialize(w *ecs.World) {
}

func (s *BatchPulseResultSystem) Update(w *ecs.World) {
	resultsBatches := make([][]jobs.Result, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			resultsBatches = append(resultsBatches, res)
		default:
			break loop
		}
	}

	for _, res := range resultsBatches {
		s.ProcessBatch(res)
	}
}

// ProcessBatch processes a batch of pulse results.
func (s *BatchPulseResultSystem) ProcessBatch(results []jobs.Result) {
	startTime := time.Now()
	processedCount := 0

	for _, result := range results {
		ent := result.Entity()
		if !s.world.Alive(ent) {
			continue
		}

		state := s.stateMapper.Get(ent)
		config := s.configMapper.Get(ent)

		if (atomic.LoadUint32(&state.Flags) & components.StatePulsePending) == 0 {
			s.logger.Warn("Entity[%d] received a PulseResult but was not in a PulsePending state.", ent.ID())
			continue
		}

		processedCount++
		state.LastCheckTime = time.Now()

		if result.Error() != nil {
			// --- FAILURE ---
			state.ConsecutiveFailures++
			state.LastError = result.Error()
			s.logger.Warn("Monitor '%s' pulse failed (%d/%d): %v", state.Name, state.ConsecutiveFailures, config.MaxFailures, state.LastError)

			if state.ConsecutiveFailures >= config.MaxFailures {
				s.logger.Warn("Monitor '%s' reached max failures, triggering intervention.", state.Name)
				atomic.OrUint32(&state.Flags, components.StateInterventionNeeded)
				state.ConsecutiveFailures = 0 // Reset after triggering
			} else if state.ConsecutiveFailures == 1 {
				s.triggerCode(ent, state, "yellow")
			}
		} else {
			// --- SUCCESS ---
			wasFailure := state.ConsecutiveFailures > 0
			if wasFailure {
				s.logger.Info("Monitor '%s' pulse recovered.", state.Name)
				s.triggerCode(ent, state, "green")
			}
			state.ConsecutiveFailures = 0
			state.LastError = nil
			state.LastSuccessTime = state.LastCheckTime
		}

		// Unset the pending flag, regardless of outcome.
		atomic.AndUint32(&state.Flags, ^uint32(components.StatePulsePending))
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchPulseResultSystem", time.Since(startTime), processedCount)
	}
}

func (s *BatchPulseResultSystem) triggerCode(entity ecs.Entity, state *components.MonitorState, color string) {
	codeConfig := s.codeConfigMapper.Get(entity)
	if codeConfig == nil {
		return
	}
	if _, ok := codeConfig.Configs[color]; ok {
		// TODO: This is a placeholder for a more robust CodeNeeded implementation
		// For now, we directly set the flag.
		atomic.OrUint32(&state.Flags, components.StateCodeNeeded)
		s.logger.Info("Monitor '%s' - triggering %s alert code", state.Name, color)
	}
}

// Finalize is a no-op for this system.
func (s *BatchPulseResultSystem) Finalize(w *ecs.World) {}
