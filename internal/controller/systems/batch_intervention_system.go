package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/queue"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionSystem processes entities that need an intervention.
// It identifies entities with the StateInterventionNeeded flag, enqueues the corresponding job,
// and transitions the entity state to StateInterventionPending.
type BatchInterventionSystem struct {
	world     *ecs.World
	queue     queue.Queue // Using a generic queue interface
	logger    Logger
	batchSize int

	// Filter for entities that require an intervention.
	filter             *ecs.Filter3[components.MonitorState, components.InterventionConfig, components.JobStorage]
	monitorStateMapper *ecs.Map[components.MonitorState]
}

// NewBatchInterventionSystem creates a new BatchInterventionSystem.
func NewBatchInterventionSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger) *BatchInterventionSystem {
	return &BatchInterventionSystem{
		world:              world,
		queue:              q,
		logger:             logger,
		batchSize:          batchSize,
		filter:             ecs.NewFilter3[components.MonitorState, components.InterventionConfig, components.JobStorage](world),
		monitorStateMapper: ecs.NewMap[components.MonitorState](world),
	}
}

func (s *BatchInterventionSystem) Initialize(w *ecs.World) {}

// Update finds and processes all monitors that need an intervention.
func (s *BatchInterventionSystem) Update(w *ecs.World) {
	startTime := time.Now()
	query := s.filter.Query()

	jobsToQueue := make([]interface{}, 0, s.batchSize)
	entitiesToUpdate := make([]ecs.Entity, 0, s.batchSize)
	processedCount := 0

	for query.Next() {
		ent := query.Entity()
		state, _, jobStorage := query.Get()

		// Process only entities that need an intervention.
		if (atomic.LoadUint32(&state.Flags) & components.StateInterventionNeeded) == 0 {
			continue
		}

		if jobStorage.InterventionJob == nil {
			s.logger.Warn("Entity[%d] has InterventionNeeded state but no InterventionJob", ent.ID())
			continue
		}

		jobsToQueue = append(jobsToQueue, jobStorage.InterventionJob)
		entitiesToUpdate = append(entitiesToUpdate, ent)

		// Process in batches
		if len(jobsToQueue) >= s.batchSize {
			s.processBatch(&jobsToQueue, &entitiesToUpdate)
			processedCount += len(jobsToQueue)
			jobsToQueue = make([]interface{}, 0, s.batchSize)
			entitiesToUpdate = make([]ecs.Entity, 0, s.batchSize)
		}
	}

	// Process any remaining entities
	if len(jobsToQueue) > 0 {
		s.processBatch(&jobsToQueue, &entitiesToUpdate)
		processedCount += len(jobsToQueue)
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchInterventionSystem", time.Since(startTime), processedCount)
	}

}

// processBatch attempts to enqueue a batch of jobs and updates entity states on success.
func (s *BatchInterventionSystem) processBatch(jobs *[]interface{}, entities *[]ecs.Entity) {
	err := s.queue.EnqueueBatch(*jobs)
	if err != nil {
		s.logger.Warn("Failed to enqueue intervention job batch, queue may be full: %v", err)
		// Do not transition state if enqueue fails, allowing retry on the next tick.
		return
	}

	// If enqueue is successful, transition the state for all entities in the batch.
	for _, ent := range *entities {
		if !s.world.Alive(ent) {
			continue
		}
		state := s.monitorStateMapper.Get(ent)
		if state == nil {
			continue
		}

		// Atomically update flags: remove InterventionNeeded, add InterventionPending.
		flags := atomic.LoadUint32(&state.Flags)
		newFlags := (flags & ^uint32(components.StateInterventionNeeded)) | uint32(components.StateInterventionPending)
		atomic.StoreUint32(&state.Flags, newFlags)

		s.logger.Info("INTERVENTION DISPATCHED: %s", state.Name)
	}
}

// Finalize is a no-op for this system.
func (s *BatchInterventionSystem) Finalize(w *ecs.World) {}
