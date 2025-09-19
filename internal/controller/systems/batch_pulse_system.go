package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/queue"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseSystem processes entities that need a pulse check.
// It identifies entities with the StatePulseNeeded flag, enqueues the corresponding job,
// and transitions the entity state to StatePulsePending.
type BatchPulseSystem struct {
	world     *ecs.World
	queue     queue.Queue // Using a generic queue interface
	logger    Logger
	batchSize int

	// Filter for entities that require a pulse check.
	filter             *ecs.Filter2[components.MonitorState, components.JobStorage]
	monitorStateMapper *ecs.Map[components.MonitorState]
}

// NewBatchPulseSystem creates a new BatchPulseSystem.
func NewBatchPulseSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger) *BatchPulseSystem {
	return &BatchPulseSystem{
		world:              world,
		queue:              q,
		logger:             logger,
		batchSize:          batchSize,
		filter:             ecs.NewFilter2[components.MonitorState, components.JobStorage](world),
		monitorStateMapper: ecs.NewMap[components.MonitorState](world),
	}
}

func (s *BatchPulseSystem) Initialize(w *ecs.World) {

}

// Update finds and processes all monitors that need a pulse check.
func (s *BatchPulseSystem) Update(w *ecs.World) {
	startTime := time.Now()
	query := s.filter.Query()

	jobsToQueue := make([]interface{}, 0, s.batchSize)
	entitiesToUpdate := make([]ecs.Entity, 0, s.batchSize)
	processedCount := 0

	for query.Next() {
		ent := query.Entity()
		state, jobStorage := query.Get()

		// Process only entities that need a pulse check.
		if (atomic.LoadUint32(&state.Flags) & components.StatePulseNeeded) == 0 {
			continue
		}

		if jobStorage.PulseJob == nil {
			s.logger.Warn("Entity[%d] has PulseNeeded state but no PulseJob", ent.ID())
			continue
		}

		jobsToQueue = append(jobsToQueue, jobStorage.PulseJob)
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
		s.logger.LogSystemPerformance("BatchPulseSystem", time.Since(startTime), processedCount)
	}

}

// processBatch attempts to enqueue a batch of jobs and updates entity states on success.
func (s *BatchPulseSystem) processBatch(jobs *[]interface{}, entities *[]ecs.Entity) {
	err := s.queue.EnqueueBatch(*jobs)
	if err != nil {
		s.logger.Warn("Failed to enqueue pulse job batch, queue may be full: %v", err)
		// Do not transition state if enqueue fails, allowing retry on the next tick.
		return
	}

	// If enqueue is successful, transition the state for all entities in the batch.
	now := time.Now()
	for _, ent := range *entities {
		if !s.world.Alive(ent) {
			continue
		}
		state := s.monitorStateMapper.Get(ent)
		if state == nil {
			continue
		}

		// Atomically update flags: remove PulseNeeded, add PulsePending.
		flags := atomic.LoadUint32(&state.Flags)
		newFlags := (flags & ^uint32(components.StatePulseNeeded)) | uint32(components.StatePulsePending)
		atomic.StoreUint32(&state.Flags, newFlags)

		// Update LastCheckTime immediately upon dispatch.
		state.LastCheckTime = now
	}
}

// Finalize is a no-op for this system.
func (s *BatchPulseSystem) Finalize(w *ecs.World) {}
