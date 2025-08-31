package systems

import (
	"context"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"github.com/mlange-42/ark/ecs"
)

// BatchPulseScheduleSystem schedules pulse checks based on intervals - THE MISSING PIECE!
type BatchPulseScheduleSystem struct {
	world  *ecs.World
	Mapper *entities.EntityManager
	logger Logger

	// Component filter - entities ready for scheduling
	PulseFilter *ecs.Filter2[components.PulseConfig, components.PulseStatus]

	// Batching
	batchSize int
}

// NewBatchPulseScheduleSystem creates the scheduling system with proper signature
func NewBatchPulseScheduleSystem(world *ecs.World, mapper *entities.EntityManager, batchSize int, logger Logger) *BatchPulseScheduleSystem {
	system := &BatchPulseScheduleSystem{
		world:     world,
		Mapper:    mapper,
		batchSize: batchSize,
		logger:    logger,
	}

	system.Initialize(world)
	return system
}

func (s *BatchPulseScheduleSystem) Initialize(w *ecs.World) {
	// Exactly like original - find entities that need scheduling
	s.PulseFilter = ecs.NewFilter2[components.PulseConfig, components.PulseStatus](w).
		Without(ecs.C[components.DisabledMonitor]()).
		Without(ecs.C[components.PulseNeeded]()).
		Without(ecs.C[components.PulsePending]()).
		Without(ecs.C[components.InterventionNeeded]()).
		Without(ecs.C[components.InterventionPending]())
}

// collectWork identifies entities that need to be scheduled for pulse checks
func (s *BatchPulseScheduleSystem) collectWork(w *ecs.World) []ecs.Entity {
	start := time.Now()
	var toCheck []ecs.Entity
	query := s.PulseFilter.Query()

	now := time.Now()

	for query.Next() {
		ent := query.Entity()
		config, status := query.Get()

		// Check if it needs scheduling based on interval (exactly like original logic)
		interval := config.Interval
		timeSinceLast := now.Sub(status.LastCheckTime)

		// If it has FirstCheck or enough time has passed, schedule it
		if s.Mapper.PulseFirstCheck.HasAll(ent) || timeSinceLast >= interval {
			toCheck = append(toCheck, ent)
		}
	}

	s.logger.LogSystemPerformance("BatchPulseScheduler", time.Since(start), len(toCheck))
	return toCheck
}

// applyWork marks entities as needing pulse checks
func (s *BatchPulseScheduleSystem) applyWork(w *ecs.World, entities []ecs.Entity) {
	for _, ent := range entities {
		if w.Alive(ent) {
			// Add PulseNeeded component to schedule the entity
			if !s.Mapper.PulseNeeded.HasAll(ent) {
				s.Mapper.PulseNeeded.Add(ent, &components.PulseNeeded{})
			}

			// Remove FirstCheck if it exists
			if s.Mapper.PulseFirstCheck.HasAll(ent) {
				s.Mapper.PulseFirstCheck.Remove(ent)
			}
		}
	}
}

// Update schedules entities for pulse checking - THE CRITICAL SYSTEM!
func (s *BatchPulseScheduleSystem) Update(ctx context.Context) error {
	// Collect entities that need scheduling
	toSchedule := s.collectWork(s.world)

	if len(toSchedule) == 0 {
		return nil
	}

	// Process in batches
	for i := 0; i < len(toSchedule); i += s.batchSize {
		end := i + s.batchSize
		if end > len(toSchedule) {
			end = len(toSchedule)
		}

		batch := toSchedule[i:end]
		s.applyWork(s.world, batch)
	}

	return nil
}

func (s *BatchPulseScheduleSystem) Finalize(w *ecs.World) {
	// Nothing to clean up
}
