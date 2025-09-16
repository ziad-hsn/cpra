package systems

import (
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"
)

// BatchInterventionSystem processes intervention dispatch using proper Ark batch operations
type BatchInterventionSystem struct {
	world  *ecs.World
	Mapper *entities.EntityManager
	queue  *queue.BoundedQueue
	logger Logger

	// Cached filter for optimal Ark performance
	interventionNeededFilter *generic.Filter1[components.InterventionNeeded]

	// Performance tracking
	entitiesProcessed int64
	batchesCreated    int64
	totalDropped      uint64
}

// NewBatchInterventionSystem creates a new batch intervention system using Ark best practices
func NewBatchInterventionSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchInterventionSystem {
	system := &BatchInterventionSystem{
		world:  world,
		Mapper: mapper,
		queue:  boundedQueue,
		logger: logger,
	}

	system.initializeComponents()
	return system
}

// Initialize initializes cached ECS filters for optimal performance
func (bis *BatchInterventionSystem) Initialize(w *ecs.World) {
	bis.initializeComponents()
}

// initializeComponents creates and registers cached filters
func (bis *BatchInterventionSystem) initializeComponents() {
	// Create cached filter and register it (Ark best practice)
	bis.interventionNeededFilter = generic.NewFilter1[components.InterventionNeeded](bis.world).
		Without(generic.T[components.InterventionPending]()).
		Register()
}

// Update processes intervention dispatch using Ark's efficient batch operations
func (bis *BatchInterventionSystem) Update(w *ecs.World) {
	start := time.Now()

	// Collect entities and jobs using cached filter
	entityJobMap := bis.collectWork(w)
	if len(entityJobMap) == 0 {
		return
	}

	// Convert to slices for batch processing
	entities := make([]ecs.Entity, 0, len(entityJobMap))
	jobs := make([]jobs.Job, 0, len(entityJobMap))

	for entity, job := range entityJobMap {
		entities = append(entities, entity)
		jobs = append(jobs, job)
	}

	// Process using proper Ark batch operations
	bis.processBatch(jobs, entities)

	// Update performance metrics
	atomic.AddInt64(&bis.entitiesProcessed, int64(len(entities)))
	atomic.AddInt64(&bis.batchesCreated, 1)

	bis.logger.LogSystemPerformance("BatchInterventionSystem", time.Since(start), len(entities))
}

// collectWork collects entities that need intervention using cached filter
func (bis *BatchInterventionSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)

	// Use cached filter for optimal performance
	query := bis.interventionNeededFilter.Query()
	defer query.Close()

	for query.Next() {
		entity := query.Entity()
		interventionJobComp := bis.Mapper.InterventionJob.Get(entity)
		if interventionJobComp == nil {
			bis.logger.Warn("Entity[%d] has no intervention job component", entity.ID())
			continue
		}

		job := interventionJobComp.Job
		if job != nil {
			out[entity] = job

			namePtr := bis.Mapper.Name.Get(entity)
			if namePtr != nil {
				bis.logger.Debug("Entity[%d] (%s) intervention job collected", entity.ID(), *namePtr)
			}
		} else {
			bis.logger.Warn("Entity[%d] has no intervention job", entity.ID())
		}
	}

	bis.logger.LogSystemPerformance("BatchInterventionCollect", time.Since(start), len(out))
	return out
}

// processBatch processes a batch of intervention jobs using proper Ark batch operations
func (bis *BatchInterventionSystem) processBatch(batchJobs []jobs.Job, batchEntities []ecs.Entity) {
	if len(batchJobs) == 0 {
		return
	}

	// Try to enqueue batch first
	err := bis.queue.EnqueueBatch(batchJobs)
	if err != nil {
		// CRITICAL FIX: Don't transition state if enqueue fails
		bis.logger.Warn("Queue full, retrying intervention batch later: %v", err)
		atomic.AddUint64(&bis.totalDropped, uint64(len(batchJobs)))
		// Keep entities in InterventionNeeded state for retry
		return
	}

	// Only transition state after successful enqueue
	// Use Ark's efficient batch operations instead of individual component changes
	bis.transitionEntityStates(batchEntities)
}

// transitionEntityStates uses Ark's efficient batch operations for state transitions
func (bis *BatchInterventionSystem) transitionEntityStates(entities []ecs.Entity) {
	if len(entities) == 0 {
		return
	}

	// Filter out entities that are no longer valid or already have InterventionPending
	validEntities := make([]ecs.Entity, 0, len(entities))
	for _, entity := range entities {
		if bis.world.Alive(entity) &&
			bis.Mapper.InterventionNeeded.HasAll(entity) &&
			!bis.Mapper.InterventionPending.HasAll(entity) {
			validEntities = append(validEntities, entity)
		}
	}

	if len(validEntities) == 0 {
		return
	}

	// Use Ark's efficient batch operations
	// Remove InterventionNeeded components in batch
	bis.Mapper.InterventionNeeded.RemoveBatch(validEntities, nil)

	// Add InterventionPending components in batch
	pendingComponent := &components.InterventionPending{
		StartTime: time.Now(),
	}
	bis.Mapper.InterventionPending.AddBatch(validEntities, pendingComponent)

	// Log transitions for monitoring
	for _, entity := range validEntities {
		namePtr := bis.Mapper.Name.Get(entity)
		if namePtr != nil {
			bis.logger.Info("INTERVENTION DISPATCHED: %s", *namePtr)
		}
	}
}

// GetStats returns performance statistics
func (bis *BatchInterventionSystem) GetStats() BatchInterventionStats {
	return BatchInterventionStats{
		EntitiesProcessed: atomic.LoadInt64(&bis.entitiesProcessed),
		BatchesCreated:    atomic.LoadInt64(&bis.batchesCreated),
		TotalDropped:      atomic.LoadUint64(&bis.totalDropped),
	}
}

// GetLastUpdateStats returns stats for the controller (compatibility)
func (bis *BatchInterventionSystem) GetLastUpdateStats() (int, int) {
	stats := bis.GetStats()
	return int(stats.EntitiesProcessed), int(stats.BatchesCreated)
}

// BatchInterventionStats provides performance metrics
type BatchInterventionStats struct {
	EntitiesProcessed int64  `json:"entities_processed"`
	BatchesCreated    int64  `json:"batches_created"`
	TotalDropped      uint64 `json:"total_dropped"`
}

// Reset resets performance counters
func (bis *BatchInterventionSystem) Reset() {
	atomic.StoreInt64(&bis.entitiesProcessed, 0)
	atomic.StoreInt64(&bis.batchesCreated, 0)
	atomic.StoreUint64(&bis.totalDropped, 0)
}

