package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
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
		if (state.Flags & components.StateCodePending) == 0 {
			s.logger.Warn("Entity received CodeResult but was not in CodePending state", "entity_id", ent.ID())
			continue
		}

		processedCount++
		oldState := *state

		// Extract color from the result payload.
		colorPayload, ok := result.Payload["color"]
		if !ok {
			s.logger.Warn("Entity has CodeResult with no color in payload", "entity_id", ent.ID())
			continue
		}
		color, ok := colorPayload.(string)
		if !ok {
			s.logger.Warn("Entity has CodeResult with invalid color payload type", "entity_id", ent.ID())
			continue
		}

		if err := result.Error(); err != nil {
			s.logger.Error("Monitor alert failed to send", "monitor_name", state.Name, "color", color, "error", err)
			// On failure, re-flag for retry: clear Pending, set Needed and restore PendingCode.
			state.Flags &^= components.StateCodePending
			state.Flags |= components.StateCodeNeeded
			if state.PendingCode == "" {
				state.PendingCode = color
			}
		} else {
			s.logger.Info("Monitor alert sent successfully", "monitor_name", state.Name, "color", color)
			// On success, clear Pending.
			state.Flags &^= components.StateCodePending
		}
		s.stateLogger.LogTransition(ent, oldState, *state)
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchCodeResultSystem", time.Since(startTime), processedCount)
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeResultSystem) Finalize(_ *ecs.World) {}
