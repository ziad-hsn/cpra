package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchCodeResultSystem processes the results of dispatched code alerts.
// It processes batches of results passed directly from the result router.
type BatchCodeResultSystem struct {
	world  *ecs.World
	logger Logger

	// Mappers for efficient component access
	stateMapper *ecs.Map[components.MonitorState]
	ResultChan  <-chan []jobs.Result
}

// NewBatchCodeResultSystem creates a new BatchCodeResultSystem.
func NewBatchCodeResultSystem(world *ecs.World, results <-chan []jobs.Result, logger Logger) *BatchCodeResultSystem {
	return &BatchCodeResultSystem{
		world:       world,
		logger:      logger,
		stateMapper: ecs.NewMap[components.MonitorState](world),
		ResultChan:  results,
	}
}

func (s *BatchCodeResultSystem) Initialize(w *ecs.World) {
}

func (s *BatchCodeResultSystem) Update(w *ecs.World) {
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
		if (atomic.LoadUint32(&state.Flags) & components.StateCodePending) == 0 {
			s.logger.Warn("Entity[%d] received CodeResult but was not in CodePending state", ent.ID())
			continue
		}

		processedCount++

		// Extract color from the result payload.
		colorPayload, ok := result.Payload["color"]
		if !ok {
			s.logger.Warn("Entity[%d] has CodeResult with no color in payload", ent.ID())
			continue
		}
		color, ok := colorPayload.(string)
		if !ok {
			s.logger.Warn("Entity[%d] has CodeResult with invalid color payload type", ent.ID())
			continue
		}

		if err := result.Error(); err != nil {
			s.logger.Error("Monitor '%s' %s alert failed to send: %v", state.Name, color, err)
		} else {
			s.logger.Info("Monitor '%s' %s alert sent successfully.", state.Name, color)
		}

		// Unset the pending flag.
		atomic.AndUint32(&state.Flags, ^uint32(components.StateCodePending))
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchCodeResultSystem", time.Since(startTime), processedCount)
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeResultSystem) Finalize(w *ecs.World) {}
