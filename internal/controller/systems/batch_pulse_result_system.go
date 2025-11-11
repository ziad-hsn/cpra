package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseResultSystem processes completed pulse checks.
// It queries for entities with a PulseResult component and updates their state accordingly.
type BatchPulseResultSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger

	// Mappers are used for efficient component access
	stateMapper              *ecs.Map1[components.MonitorState]
	configMapper             *ecs.Map1[components.PulseConfig]
	codeConfigMapper         *ecs.Map1[components.CodeConfig]
	interventionConfigMapper *ecs.Map1[components.InterventionConfig]
	ResultChan               <-chan []jobs.Result
}

// NewBatchPulseResultSystem creates a new BatchPulseResultSystem.
func NewBatchPulseResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger, stateLogger *StateLogger) *BatchPulseResultSystem {
	return &BatchPulseResultSystem{
		world:                    world,
		logger:                   logger,
		stateLogger:              stateLogger,
		stateMapper:              ecs.NewMap1[components.MonitorState](world),
		configMapper:             ecs.NewMap1[components.PulseConfig](world),
		codeConfigMapper:         ecs.NewMap1[components.CodeConfig](world),
		interventionConfigMapper: ecs.NewMap1[components.InterventionConfig](world),
		ResultChan:               results,
	}
}
func (s *BatchPulseResultSystem) Initialize(_ *ecs.World) {
}

func (s *BatchPulseResultSystem) Update(_ *ecs.World) {
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

	// Thresholds now come from PulseConfig; fall back to defaults if unset
	const defaultK = 2
	const defaultM = 3

	for _, result := range results {
		ent := result.Entity()
		if !s.world.Alive(ent) {
			continue
		}

		state := s.stateMapper.Get(ent)
		config := s.configMapper.Get(ent)

		flags := state.Flags
		if (flags & components.StatePulsePending) == 0 {
			s.logger.Warn("Entity[%d] received a PulseResult but was not in a PulsePending state.", ent.ID())
			continue
		}

		processedCount++
		oldState := *state
		state.LastCheckTime = time.Now()

		if result.Error() != nil {
			// --- FAILURE ---
			state.LastError = result.Error()
			// If we are in verification window, escalate to RED and close verification
			if flags&components.StateVerifying != 0 {
				s.logger.Warn("Monitor '%s' verification failed during post-intervention window: %v", state.Name, state.LastError)
				// Only trigger red if incident not already open (defensive)
				if (flags & components.StateIncidentOpen) == 0 {
					s.triggerCode(ent, state, "red")
					state.Flags |= components.StateIncidentOpen
					s.logger.Info("Monitor '%s' - RED ALERT: verification failed, incident opened", state.Name)
				}
				state.Flags &^= components.StateVerifying
				state.VerifyRemaining = 0
				state.RecoveryStreak = 0
			} else {
				state.PulseFailures++
				s.logger.Warn("Monitor '%s' pulse failed (%d/%d): %v", state.Name, state.PulseFailures, config.UnhealthyThreshold, state.LastError)
				// First failure: only send yellow if no incident is open
				if state.PulseFailures == 1 && (flags&components.StateIncidentOpen) == 0 {
					s.triggerCode(ent, state, "yellow")
				}
				unhealthy := config.UnhealthyThreshold
				if unhealthy <= 0 {
					unhealthy = 1
				}
				if state.PulseFailures >= unhealthy {
					if s.interventionConfigMapper.Get(ent) != nil {
						s.logger.Warn("Monitor '%s' reached max failures, triggering intervention.", state.Name)
						state.Flags |= components.StateInterventionNeeded
						state.PulseFailures = 0
						state.RecoveryStreak = 0
					} else {
						// No intervention configured - trigger RED alert once
						if (flags & components.StateIncidentOpen) == 0 {
							s.logger.Warn("Monitor '%s' reached max failures; no intervention configured, triggering RED alert.", state.Name)
							s.triggerCode(ent, state, "red")
							state.Flags |= components.StateIncidentOpen
							s.logger.Info("Monitor '%s' - RED ALERT: incident opened (no intervention)", state.Name)
						} else {
							s.logger.Debug("Monitor '%s' - max failures reached but incident already open, no duplicate red alert", state.Name)
						}
						state.PulseFailures = 0
						state.RecoveryStreak = 0
					}
				}
			}
		} else {
			// --- SUCCESS ---
			state.LastError = nil
			state.LastSuccessTime = state.LastCheckTime
			if flags&components.StateVerifying != 0 {
				if state.VerifyRemaining <= 0 {
					// safety: conclude verification immediately
					state.Flags &^= components.StateVerifying
					s.triggerCode(ent, state, "green")
					state.Flags &^= components.StateIncidentOpen
					state.RecoveryStreak = 0
				} else {
					state.VerifyRemaining--
					if state.VerifyRemaining <= 0 {
						state.Flags &^= components.StateVerifying
						s.triggerCode(ent, state, "green")
						state.Flags &^= components.StateIncidentOpen
						state.RecoveryStreak = 0
					}
				}
			} else {
				// Normal recovery path
				if state.PulseFailures > 0 || (flags&components.StateIncidentOpen) != 0 {
					state.RecoveryStreak++
					k := config.HealthyThreshold
					if k <= 0 {
						k = defaultK
					}
					if state.RecoveryStreak >= k {
						s.logger.Info("Monitor '%s' pulse recovered (K=%d).", state.Name, k)
						s.triggerCode(ent, state, "green")
						state.Flags &^= components.StateIncidentOpen
						state.RecoveryStreak = 0
					}
				} else {
					// steady state success, nothing to do
				}
			}
			state.PulseFailures = 0
		}

		// Unset the pending flag, regardless of outcome.
		state.Flags &^= components.StatePulsePending
		s.stateLogger.LogTransition(ent, oldState, *state)
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
	cfg, ok := codeConfig.Configs[color]
	if !ok {
		s.logger.Warn("Monitor '%s' has no '%s' code config; skipping alert trigger", state.Name, color)
		return
	}
	if !cfg.Dispatch {
		s.logger.Info("Monitor '%s' '%s' code dispatch disabled; not triggering", state.Name, color)
		return
	}
	// TODO: This is a placeholder for a more robust CodeNeeded implementation
	// For now, we directly set the flag.
	state.PendingCode = color
	state.Flags |= components.StateCodeNeeded
	s.logger.Info("Monitor '%s' - triggering %s alert code", state.Name, color)
}

// Finalize is a no-op for this system.
func (s *BatchPulseResultSystem) Finalize(_ *ecs.World) {}
