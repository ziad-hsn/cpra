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
    codeConfigMapper *ecs.Map1[components.CodeConfig]
    pulseConfigMapper *ecs.Map1[components.PulseConfig]
    ResultChan  <-chan []jobs.Result
}

// NewBatchInterventionResultSystem creates a new BatchInterventionResultSystem.
func NewBatchInterventionResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger) *BatchInterventionResultSystem {
    return &BatchInterventionResultSystem{
        world:       world,
        logger:      logger,
        stateMapper: ecs.NewMap[components.MonitorState](world),
        codeConfigMapper: ecs.NewMap1[components.CodeConfig](world),
        pulseConfigMapper: ecs.NewMap1[components.PulseConfig](world),
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

		flags := atomic.LoadUint32(&state.Flags)
		// Ensure we are processing a pending intervention
		if (flags & components.StateInterventionPending) == 0 {
			s.logger.Warn("Entity[%d] received InterventionResult but was not in InterventionPending state", ent.ID())
			continue
		}

		processedCount++
		state.LastCheckTime = time.Now()

        if result.Error() != nil {
            // --- FAILURE ---
            state.InterventionFailures++
            state.LastError = result.Error()
            s.logger.Error("Monitor '%s' intervention failed: %v", state.Name, state.LastError)

            // Only trigger red alert if incident is NOT already open
            if (flags & components.StateIncidentOpen) == 0 {
                s.triggerCode(ent, state, "red")
                atomic.OrUint32(&state.Flags, components.StateIncidentOpen)
                s.logger.Info("Monitor '%s' - RED ALERT: incident opened", state.Name)
            } else {
                s.logger.Debug("Monitor '%s' - intervention failed but incident already open, no duplicate red alert", state.Name)
            }
        } else {
            // --- SUCCESS ---
            s.logger.Info("Monitor '%s' intervention succeeded.", state.Name)
            state.ConsecutiveFailures = 0
            state.LastError = nil
            state.LastSuccessTime = state.LastCheckTime
            // Begin verification window (Phase 2)
            // Use pulse HealthyThreshold as verification count if available, else default
            pulseCfg := s.pulseConfigMapper.Get(ent)
            m := 3
            if pulseCfg != nil && pulseCfg.HealthyThreshold > 0 {
                m = pulseCfg.HealthyThreshold
            }
            state.VerifyRemaining = m
            atomic.OrUint32(&state.Flags, components.StateVerifying)
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
    codeConfig := s.codeConfigMapper.Get(entity)
    if codeConfig == nil {
        return
    }
    cfg, ok := codeConfig.Configs[color]
    if !ok {
        s.logger.Warn("Monitor '%s' has no '%s' code config; skipping alert flag", state.Name, color)
        return
    }
    if !cfg.Dispatch {
        s.logger.Info("Monitor '%s' '%s' code dispatch disabled; not flagging", state.Name, color)
        return
    }
    state.PendingCode = color
    atomic.OrUint32(&state.Flags, components.StateCodeNeeded)
    s.logger.Info("Monitor '%s' - flagging for %s alert code", state.Name, color)
}

// Finalize is a no-op for this system.
func (s *BatchInterventionResultSystem) Finalize(w *ecs.World) {}
