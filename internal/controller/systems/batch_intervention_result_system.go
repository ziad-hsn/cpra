package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionResultSystem processes completed intervention jobs.
// It processes batches of results passed directly from the result router.
type BatchInterventionResultSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger

	// Mappers for efficient component access
	stateMapper       *ecs.Map[components.MonitorState]
	codeConfigMapper  *ecs.Map1[components.CodeConfig]
	pulseConfigMapper *ecs.Map1[components.PulseConfig]
	registry          *components.ConfigRegistry
	ResultChan        <-chan []jobs.Result
}

// NewBatchInterventionResultSystem creates a new BatchInterventionResultSystem.
func NewBatchInterventionResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger, stateLogger *StateLogger) *BatchInterventionResultSystem {
	return &BatchInterventionResultSystem{
		world:             world,
		logger:            logger,
		stateLogger:       stateLogger,
		stateMapper:       ecs.NewMap[components.MonitorState](world),
		codeConfigMapper:  ecs.NewMap1[components.CodeConfig](world),
		pulseConfigMapper: ecs.NewMap1[components.PulseConfig](world),
		registry:          components.DefaultConfigRegistry(),
		ResultChan:        results,
	}
}

func (s *BatchInterventionResultSystem) Initialize(_ *ecs.World) {
}

func (s *BatchInterventionResultSystem) Update(_ *ecs.World) {
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

		flags := state.Flags
		// Ensure we are processing a pending intervention
		if (flags & components.StateInterventionPending) == 0 {
			s.logger.Warnw("Entity received InterventionResult but was not in InterventionPending state", "entity_id", ent.ID())
			continue
		}

		processedCount++
		oldState := *state
		eventTime := time.Now()
		state.LastEventTime = eventTime

		if result.Error() != nil {
			// --- FAILURE ---
			state.InterventionFailures++
			state.LastError = result.Error()
			s.logger.Errorw("Monitor intervention failed", "monitor_name", state.Name, "error", state.LastError)

			// Only trigger red alert if incident is NOT already open
			if (flags & components.StateIncidentOpen) == 0 {
				s.triggerCode(ent, state, components.ColorRed)
				state.Flags |= components.StateIncidentOpen
				s.logger.Infow("RED ALERT: incident opened", "monitor_name", state.Name)
			} else {
				s.logger.Debugw("Intervention failed but incident already open, no duplicate red alert", "monitor_name", state.Name)
			}
		} else {
			// --- SUCCESS ---
			s.logger.Infow("Monitor intervention succeeded", "monitor_name", state.Name)
			state.ConsecutiveFailures = 0
			state.LastError = nil
			state.LastSuccessTime = eventTime
			// Begin verification window (Phase 2)
			// Use pulse HealthyThreshold as verification count if available, else default
			pulseCfg := s.pulseConfigMapper.Get(ent)
			m := 3
			if pulseCfg != nil && pulseCfg.HealthyThreshold > 0 {
				m = pulseCfg.HealthyThreshold
			}
			state.VerifyRemaining = m
			state.Flags |= components.StateVerifying
			s.triggerCode(ent, state, components.ColorCyan)
		}

		// Unset the pending flag, regardless of outcome.
		state.Flags &^= components.StateInterventionPending
		s.stateLogger.LogTransition(ent, oldState, *state)
	}

	if processedCount > 0 {
		dur := time.Since(startTime)
		s.logger.Debugf("Performance: BatchInterventionResultSystem processed %d entities in %v (%.1f/sec)",
			processedCount, dur, float64(processedCount)/dur.Seconds())
	}
}

func (s *BatchInterventionResultSystem) triggerCode(entity ecs.Entity, state *components.MonitorState, color components.ColorCode) {
	codeConfig := s.codeConfigMapper.Get(entity)
	if codeConfig == nil {
		return
	}
	if color >= components.MaxColors {
		return
	}
	cfg, ok := s.registry.Lookup(codeConfig.Configs[color])
	if !ok || cfg.Notify == "" {
		s.logger.Warnw("Monitor has no code config; skipping alert flag", "monitor_name", state.Name, "color", color)
		return
	}
	if !cfg.Dispatch {
		s.logger.Infow("Code dispatch disabled; not flagging", "monitor_name", state.Name, "color", color)
		return
	}

	// FSM guard: If a code job is already in-flight (Pending), don't overwrite.
	if (state.Flags & components.StateCodePending) != 0 {
		s.logger.Debugf("Monitor '%s' already has code in-flight; deferring %s trigger", state.Name, color)
		return
	}

	// If CodeNeeded is already set, use priority to decide.
	if (state.Flags&components.StateCodeNeeded) != 0 && state.PendingColor != components.ColorNone {
		if !color.HigherPriorityThan(state.PendingColor) {
			s.logger.Debugf("Monitor '%s' already has %s pending; %s has lower priority, skipping", state.Name, state.PendingColor, color)
			return
		}
		s.logger.Debugf("Monitor '%s' upgrading pending code from %s to %s", state.Name, state.PendingColor, color)
	}

	state.PendingColor = color
	state.Flags |= components.StateCodeNeeded
	s.logger.Infow("Flagging for alert code", "monitor_name", state.Name, "color", color)
}

// Finalize is a no-op for this system.
func (s *BatchInterventionResultSystem) Finalize(_ *ecs.World) {}
