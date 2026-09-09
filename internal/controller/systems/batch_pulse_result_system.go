package systems

import (
	"cpra/internal/alerts"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseResultSystem processes completed pulse checks.
// It queries for entities with a PulseResult component and updates their state accordingly.
type BatchPulseResultSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger
	alertMgr    *alerts.Manager
	sched       *PulseScheduler

	// interventionReady receives entities that need an intervention (set by
	// this system on pulse failure) for the intervention dispatch system.
	interventionReady *ReadyQueue
	// codeSched receives code alerts (immediate -> ready queue, deferred -> heap).
	codeSched *CodeScheduler

	// Mappers are used for efficient component access
	stateMapper              *ecs.Map1[components.MonitorState]
	configMapper             *ecs.Map1[components.PulseConfig]
	codeConfigMapper         *ecs.Map1[components.CodeConfig]
	interventionConfigMapper *ecs.Map1[components.InterventionConfig]
	ResultChan               <-chan []jobs.Result
}

// NewBatchPulseResultSystem creates a new BatchPulseResultSystem.
func NewBatchPulseResultSystem(world *ecs.World, results <-chan []jobs.Result, sched *PulseScheduler, interventionReady *ReadyQueue, codeSched *CodeScheduler, logger Logger, stateLogger *StateLogger, alertMgr *alerts.Manager) *BatchPulseResultSystem {
	return &BatchPulseResultSystem{
		world:                    world,
		logger:                   logger,
		stateLogger:              stateLogger,
		alertMgr:                 alertMgr,
		sched:                    sched,
		interventionReady:        interventionReady,
		codeSched:                codeSched,
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
	now := startTime
	processedCount := 0

	// Thresholds now come from PulseConfig; fall back to defaults if unset
	const defaultK = 2

	for _, result := range results {
		ent := result.Entity()
		if !s.world.Alive(ent) {
			continue
		}

		state := s.stateMapper.Get(ent)
		config := s.configMapper.Get(ent)
		if state == nil || config == nil {
			s.logger.Warn("Entity[%d] received a PulseResult but is missing MonitorState/PulseConfig.", ent.ID())
			continue
		}

		flags := state.Flags
		if (flags&components.StatePulsePending) == 0 || result.Generation != state.PulseGeneration {
			s.logger.Warn("Entity[%d] received a PulseResult but was not in a PulsePending state.", ent.ID())
			continue
		}

		if flags&components.StateVerifying != 0 && result.Generation <= state.VerificationAfter {
			state.SetPulsePending(false)
			state.NextCheckTime = now.Add(config.Interval)
			s.sched.Schedule(ent, state.NextCheckTime)
			continue
		}
		processedCount++
		oldFlags := state.Flags
		state.LastCheckTime = now
		previousWarning := state.PulseWarning
		state.PulseWarning = result.Warning

		if result.Error() != nil {
			// --- FAILURE ---
			state.ConsecutiveFailures++
			state.PulseWarning = ""
			state.RecoveryStreak = 0
			state.Recovering = true
			state.LastError = result.Error()
			// If we are in verification window, escalate to RED and close verification
			if flags&components.StateVerifying != 0 && result.Generation > state.VerificationAfter {
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
				if state.PulseFailures >= unhealthy && !schema.InMaintenance(state.Maintenance, now) {
					if s.interventionConfigMapper.Get(ent) != nil && !state.InterventionAttempted && !state.IsInterventionNeeded() && !state.IsInterventionPending() {
						state.InterventionAttempted = true
						s.logger.Warn("Monitor '%s' reached max failures, triggering intervention.", state.Name)
						state.Flags |= components.StateInterventionNeeded
						if s.interventionReady != nil {
							s.interventionReady.Enqueue([]ecs.Entity{ent})
						}
						state.PulseFailures = 0
						state.RecoveryStreak = 0
					} else if !state.IsInterventionPending() && !state.IsInterventionNeeded() {
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
			state.ConsecutiveFailures = 0
			state.LastError = nil
			state.LastSuccessTime = state.LastCheckTime
			if state.PulseWarning != "" && previousWarning == "" {
				s.triggerCode(ent, state, "yellow")
			} else if previousWarning != "" && state.PulseWarning == "" && !state.Recovering && !state.IsInterventionPending() && flags&(components.StateIncidentOpen|components.StateVerifying) == 0 {
				s.triggerCode(ent, state, "green")
			}
			if flags&components.StateVerifying != 0 && result.Generation > state.VerificationAfter {
				if state.VerifyRemaining <= 0 {
					// safety: conclude verification immediately
					state.Flags &^= components.StateVerifying
					s.triggerCode(ent, state, "green")
					state.Flags &^= components.StateIncidentOpen
					state.Recovering = false
					state.InterventionAttempted = false
					state.RecoveryStreak = 0
				} else {
					state.VerifyRemaining--
					if state.VerifyRemaining <= 0 {
						state.Flags &^= components.StateVerifying
						s.triggerCode(ent, state, "green")
						state.Flags &^= components.StateIncidentOpen
						state.Recovering = false
						state.InterventionAttempted = false
						state.RecoveryStreak = 0
					}
				}
			} else {
				// Normal recovery path
				if flags&components.StateVerifying == 0 && !state.IsInterventionPending() && (state.Recovering || state.PulseFailures > 0 || (flags&components.StateIncidentOpen) != 0) {
					state.RecoveryStreak++
					k := config.HealthyThreshold
					if k <= 0 {
						k = defaultK
					}
					if state.RecoveryStreak >= k {
						s.logger.Info("Monitor '%s' pulse recovered (K=%d).", state.Name, k)
						s.triggerCode(ent, state, "green")
						state.Flags &^= components.StateIncidentOpen
						state.Recovering = false
						state.SetInterventionNeeded(false)
						state.InterventionAttempted = false
						state.RecoveryStreak = 0
					}
				}
			}
			state.PulseFailures = 0
		}

		// Unset the pending flag, regardless of outcome.
		state.Flags &^= components.StatePulsePending

		// Re-schedule the next check with a fixed-rate recurrence anchored
		// on the due time that triggered this check, not on when the result was
		// applied. Anchoring on LastCheckTime folds pipeline latency (ready-
		// queue wait + worker + result drain) into every cycle, stretching each
		// monitor's cadence to interval+latency and capping fleet throughput at
		// N/(interval+latency). Anchoring on NextCheckTime keeps the cadence
		// exact; the fell-behind guard re-anchors from now instead of firing a
		// catch-up burst of stale checks after an overload or stall.
		next := state.NextCheckTime.Add(config.Interval)
		if next.Before(now) {
			next = now.Add(config.Interval)
		}
		state.NextCheckTime = next
		s.sched.Schedule(ent, next)

		s.stateLogger.LogTransition(ent, oldFlags, state)
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchPulseResultSystem", time.Since(startTime), processedCount)
	}
}

func (s *BatchPulseResultSystem) triggerCode(entity ecs.Entity, state *components.MonitorState, color string) {
	if color == "green" && state.PulseWarning != "" {
		return
	}
	requestCode(entity, state, color, s.codeConfigMapper.Get(entity), s.codeSched, s.alertMgr)
}

// Finalize is a no-op for this system.
func (s *BatchPulseResultSystem) Finalize(_ *ecs.World) {}
