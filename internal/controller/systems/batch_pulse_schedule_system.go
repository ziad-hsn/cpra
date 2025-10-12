package systems

import (
	"sync/atomic"
	"time"

	"cpra/internal/controller/components"
	"github.com/mlange-42/ark/ecs"
)

// BatchPulseScheduleSystem schedules pulse checks for entities that are due.
// It queries for monitors that are not disabled, not already pending a pulse check,
// and whose interval has passed since the last check.
// This system is a critical part of the monitoring pipeline, ensuring that checks
// are scheduled in a timely and efficient manner.
type BatchPulseScheduleSystem struct {
	world  *ecs.World
	logger Logger

	// Filter for entities that are candidates for a pulse check.
	filter *ecs.Filter2[components.MonitorState, components.PulseConfig]
}

// NewBatchPulseScheduleSystem creates a new BatchPulseScheduleSystem.
func NewBatchPulseScheduleSystem(world *ecs.World, logger Logger) *BatchPulseScheduleSystem {
	return &BatchPulseScheduleSystem{
		world:  world,
		logger: logger,
		filter: ecs.NewFilter2[components.MonitorState, components.PulseConfig](world),
	}
}

func (s *BatchPulseScheduleSystem) Initialize(w *ecs.World) {
    if s.filter != nil {
        s.filter.Register()
    }
}

// Update finds and schedules all monitors that are due for a pulse check.
func (s *BatchPulseScheduleSystem) Update(w *ecs.World) {
	start := time.Now()
	query := s.filter.Query()
	var scheduledCount int

	now := time.Now()

	for query.Next() {
		state, config := query.Get()

		for {
			flags := atomic.LoadUint32(&state.Flags)

			if (flags&components.StateDisabled != 0) || (flags&components.StatePulseNeeded != 0) || (flags&components.StatePulsePending != 0) {
				break
			}

			due := (flags&components.StatePulseFirstCheck != 0) || (now.Sub(state.LastCheckTime) >= config.Interval)
			if !due {
				break
			}

			updated := (flags | components.StatePulseNeeded) &^ components.StatePulseFirstCheck
			if atomic.CompareAndSwapUint32(&state.Flags, flags, updated) {
				scheduledCount++
				break
			}
		}
	}

	if scheduledCount > 0 {
		s.logger.LogSystemPerformance("BatchPulseScheduleSystem", time.Since(start), scheduledCount)
	}

}

// Finalize is a no-op for this system.
func (s *BatchPulseScheduleSystem) Finalize(w *ecs.World) {
	// Nothing to clean up
}
