package systems

import (
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// DefaultMaxSchedulePerTick limits how many pulse checks can be scheduled per tick.
// This prevents CPU spikes from scheduling millions of checks simultaneously.
// At 100 TPS, 10,000/tick = 1M checks/second theoretical max throughput.
const DefaultMaxSchedulePerTick = 10000

// BatchPulseScheduleSystem schedules pulse checks for entities that are due.
// It queries for monitors that are not disabled, not already pending a pulse check,
// and whose interval has passed since the last check.
// This system is a critical part of the monitoring pipeline, ensuring that checks
// are scheduled in a timely and efficient manner.
type BatchPulseScheduleSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger

	// Filter for entities that are candidates for a pulse check.
	filter *ecs.Filter2[components.MonitorState, components.PulseConfig]

	// maxSchedulePerTick limits how many monitors can be scheduled per tick
	// to prevent CPU spikes and spread a load more evenly.
	maxSchedulePerTick int
}

// NewBatchPulseScheduleSystem creates a new BatchPulseScheduleSystem.
func NewBatchPulseScheduleSystem(world *ecs.World, logger Logger, stateLogger *StateLogger) *BatchPulseScheduleSystem {
	return &BatchPulseScheduleSystem{
		world:       world,
		logger:      logger,
		stateLogger: stateLogger,
		filter: ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
			Without(ecs.C[components.Disabled]()),
		maxSchedulePerTick: DefaultMaxSchedulePerTick,
	}
}

// SetMaxSchedulePerTick sets the maximum number of monitors that can be scheduled per tick.
func (s *BatchPulseScheduleSystem) SetMaxSchedulePerTick(max int) {
	if max > 0 {
		s.maxSchedulePerTick = max
	}
}

func (s *BatchPulseScheduleSystem) Initialize(_ *ecs.World) {
	if s.filter != nil {
		s.filter.Register()
	}
}

// Update finds and schedules all monitors that are due for a pulse check.
// It limits scheduling to maxSchedulePerTick to spread a load across ticks.
func (s *BatchPulseScheduleSystem) Update(_ *ecs.World) {
	start := time.Now()
	query := s.filter.Query()
	var scheduledCount int

	now := time.Now()
	maxSchedule := s.maxSchedulePerTick
	if maxSchedule <= 0 {
		maxSchedule = DefaultMaxSchedulePerTick
	}

	// Track if we break early (need to close a query manually)
	brokeEarly := false

	for query.Next() {
		// Rate limit: stop if we've scheduled enough this tick
		if scheduledCount >= maxSchedule {
			brokeEarly = true
			break
		}

		entity := query.Entity()
		state, config := query.Get()

		flags := state.Flags

		// Disabled entities are excluded at the filter level via Without(Disabled)
		if (flags&components.StatePulseNeeded != 0) || (flags&components.StatePulsePending != 0) {
			continue
		}

		due := (flags&components.StatePulseFirstCheck != 0) || (now.Sub(state.LastPulseCheckTime) >= config.Interval)
		if !due {
			continue
		}

		oldState := *state
		state.Flags |= components.StatePulseNeeded
		state.Flags &^= components.StatePulseFirstCheck
		s.stateLogger.LogTransition(entity, oldState, *state)
		scheduledCount++
	}

	// Only close a query if we broke out early; natural completion auto-closes
	if brokeEarly {
		query.Close()
	}

	if scheduledCount > 0 {
		dur := time.Since(start)
		s.logger.Debugf("Performance: BatchPulseScheduleSystem processed %d entities in %v (%.1f/sec)",
			scheduledCount, dur, float64(scheduledCount)/dur.Seconds())
	}
}

// Finalize is a no-op for this system.
func (s *BatchPulseScheduleSystem) Finalize(_ *ecs.World) {
	// Nothing to clean up
}
