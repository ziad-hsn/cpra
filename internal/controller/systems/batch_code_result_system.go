package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/runtime/jobs"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchCodeResultSystem processes the results of dispatched code alerts.
// It processes batches of results passed directly from the result router.
type BatchCodeResultSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger

	// Mappers for efficient component access
	stateMapper *ecs.Map[components.MonitorState]
	ResultChan  <-chan []jobs.Result
}

// NewBatchCodeResultSystem creates a new BatchCodeResultSystem.
func NewBatchCodeResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger, stateLogger *StateLogger) *BatchCodeResultSystem {
	return &BatchCodeResultSystem{
		world:       world,
		logger:      logger,
		stateLogger: stateLogger,
		stateMapper: ecs.NewMap[components.MonitorState](world),
		ResultChan:  results,
	}
}

func (s *BatchCodeResultSystem) Initialize(_ *ecs.World) {
}

func (s *BatchCodeResultSystem) Update(_ *ecs.World) {
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

// ProcessBatch processes a batch of code alert results.
func (s *BatchCodeResultSystem) ProcessBatch(results []jobs.Result) {
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

		// Ensure we are processing a pending code alert.
		// Note: If entity is not in CodePending state, it means another system already
		// processed or cancelled this job. This is expected behavior in concurrent systems.
		if (state.Flags & components.StateCodePending) == 0 {
			s.logger.Debugw("Entity received stale CodeResult (state already changed)", "entity_id", ent.ID(), "flags", state.Flags)
			continue
		}

		oldState := *state
		if state.CodeRunVersion != state.ConfigVersion {
			state.Flags &^= components.StateCodePending
			state.PendingColor = components.ColorNone
			s.stateLogger.LogTransition(ent, oldState, *state)
			s.logger.Debugw("Dropping stale CodeResult", "entity_id", ent.ID(), "run_version", state.CodeRunVersion, "current_version", state.ConfigVersion)
			continue
		}

		processedCount++

		// Extract color from the result payload.
		colorPayload, ok := result.Payload["color"]
		if !ok {
			s.logger.Warnw("Entity has CodeResult with no color in payload", "entity_id", ent.ID())
			continue
		}
		color, ok := colorPayload.(string)
		if !ok {
			s.logger.Warnw("Entity has CodeResult with invalid color payload type", "entity_id", ent.ID())
			continue
		}

		if err := result.Error(); err != nil {
			s.logger.Errorw("Monitor alert failed to send", "monitor_name", state.Name, "color", color, "error", err)
			// On failure, re-flag for retry: clear Pending and set Needed.
			state.Flags &^= components.StateCodePending
			state.Flags |= components.StateCodeNeeded
		} else {
			s.logger.Infow("Monitor alert sent successfully", "monitor_name", state.Name, "color", color)
			// On success, clear Pending and PendingColor.
			state.Flags &^= components.StateCodePending
			state.PendingColor = components.ColorNone
		}
		s.stateLogger.LogTransition(ent, oldState, *state)
	}

	if processedCount > 0 {
		dur := time.Since(startTime)
		s.logger.Debugf("Performance: BatchCodeResultSystem processed %d entities in %v (%.1f/sec)",
			processedCount, dur, float64(processedCount)/dur.Seconds())
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeResultSystem) Finalize(_ *ecs.World) {}
