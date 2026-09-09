package systems

import (
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// BatchCodeScheduleSystem releases deferred code alerts once their cooldown
// window has elapsed.
//
// It pops due alerts from the CodeScheduler heap (keyed by NotBefore) instead
// of scanning every entity each tick, so the per-tick cost is O(M log N) where
// M is the number of released alerts.
type BatchCodeScheduleSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger
	codeSched   *CodeScheduler

	stateMapper    *ecs.Map1[components.MonitorState]
	disabledMapper *ecs.Map1[components.Disabled]
}

// NewBatchCodeScheduleSystem creates a new BatchCodeScheduleSystem.
func NewBatchCodeScheduleSystem(world *ecs.World, codeSched *CodeScheduler, logger Logger, stateLogger *StateLogger) *BatchCodeScheduleSystem {
	return &BatchCodeScheduleSystem{
		world:          world,
		logger:         logger,
		stateLogger:    stateLogger,
		codeSched:      codeSched,
		stateMapper:    ecs.NewMap1[components.MonitorState](world),
		disabledMapper: ecs.NewMap1[components.Disabled](world),
	}
}

func (s *BatchCodeScheduleSystem) Initialize(_ *ecs.World) {
}

// Update releases deferred alerts whose cooldown has elapsed.
func (s *BatchCodeScheduleSystem) Update(_ *ecs.World) {
	start := time.Now()
	now := time.Now()

	due := s.codeSched.Due(now)

	ready := make([]ecs.Entity, 0, len(due))
	for _, ent := range due {
		if !s.world.Alive(ent) {
			continue
		}
		if s.disabledMapper.Get(ent) != nil {
			continue
		}
		state := s.stateMapper.Get(ent)
		if state == nil {
			continue
		}
		if len(state.PendingAlerts) == 0 {
			continue
		}

		oldFlags := state.Flags
		state.Flags |= components.StateCodeNeeded
		s.stateLogger.LogTransition(ent, oldFlags, state)
		ready = append(ready, ent)
	}

	if len(ready) > 0 {
		s.codeSched.EnqueueReady(ready)
		s.logger.LogSystemPerformance("BatchCodeScheduleSystem", time.Since(start), len(ready))
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeScheduleSystem) Finalize(_ *ecs.World) {}
