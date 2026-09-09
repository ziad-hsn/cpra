package systems

import (
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseScheduleSystem takes due checks from PulseScheduler, validates
// their current state, and passes them to the dispatch system.
type BatchPulseScheduleSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger
	sched       *PulseScheduler

	// Filter for entities that are candidates for a pulse check (used only to
	// populate the scheduler in Initialize).
	filter *ecs.Filter2[components.MonitorState, components.PulseConfig]

	// Mappers for revalidating popped entities.
	stateMapper    *ecs.Map1[components.MonitorState]
	disabledMapper *ecs.Map1[components.Disabled]
}

// NewBatchPulseScheduleSystem creates a new BatchPulseScheduleSystem.
func NewBatchPulseScheduleSystem(world *ecs.World, sched *PulseScheduler, logger Logger, stateLogger *StateLogger) *BatchPulseScheduleSystem {
	return &BatchPulseScheduleSystem{
		world:       world,
		logger:      logger,
		stateLogger: stateLogger,
		sched:       sched,
		filter: ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
			Without(ecs.C[components.Disabled]()),
		stateMapper:    ecs.NewMap1[components.MonitorState](world),
		disabledMapper: ecs.NewMap1[components.Disabled](world),
	}
}

// Initialize registers the filter and schedules the loaded monitors.
func (s *BatchPulseScheduleSystem) Initialize(_ *ecs.World) {
	if s.filter != nil {
		s.filter.Register()
	}

	now := time.Now()
	query := s.filter.Query()
	for query.Next() {
		ent := query.Entity()
		state, config := query.Get()

		// First check is due immediately; otherwise due at last check + interval.
		var due time.Time
		if state.Flags&components.StatePulseFirstCheck == 0 {
			due = state.LastCheckTime.Add(config.Interval)
		} else {
			// Spread first checks across one interval to avoid dispatching the
			// entire fleet on the first tick.
			due = now.Add(staggerPhase(ent.ID(), config.Interval))
		}
		state.NextCheckTime = due
		s.sched.Schedule(ent, due)
	}
}

// staggerPhase maps an entity deterministically to an offset in [0, interval).
func staggerPhase(entityID uint32, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(uint64(entityID) * 0x9E3779B97F4A7C15 % uint64(interval))
}

// Update takes due monitors from the scheduler, marks them
// StatePulseNeeded, and pushes them onto the ready queue for the dispatch system.
func (s *BatchPulseScheduleSystem) Update(_ *ecs.World) {
	start := time.Now()
	now := time.Now()

	due := s.sched.Due(now)

	// Revalidate each entity before marking it needed: the scheduler is a
	// secondary index and may hold stale entries for removed or already-pending
	// entities.
	ready := make([]ecs.Entity, 0, len(due))
	for _, ent := range due {
		if !s.world.Alive(ent) {
			continue
		}
		// Skip disabled entities: the heap is a secondary index and may hold
		// stale entries for monitors disabled after being scheduled.
		if s.disabledMapper.Get(ent) != nil {
			continue
		}
		state := s.stateMapper.Get(ent)
		if state == nil {
			continue
		}
		if state.Flags&(components.StatePulseNeeded|components.StatePulsePending) != 0 {
			continue
		}

		oldFlags := state.Flags
		state.Flags |= components.StatePulseNeeded
		state.Flags &^= components.StatePulseFirstCheck
		s.stateLogger.LogTransition(ent, oldFlags, state)
		ready = append(ready, ent)
	}

	if len(ready) > 0 {
		s.sched.EnqueueReady(ready)
		s.logger.LogSystemPerformance("BatchPulseScheduleSystem", time.Since(start), len(ready))
	}
}

// Finalize is a no-op for this system.
func (s *BatchPulseScheduleSystem) Finalize(_ *ecs.World) {
	// Nothing to clean up
}
