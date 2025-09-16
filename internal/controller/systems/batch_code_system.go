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

// dispatchableCodeJob holds job and color info for code dispatch
type dispatchableCodeJob struct {
	job   jobs.Job
	color string
}

// BatchCodeSystem processes code dispatch using proper Ark batch operations
type BatchCodeSystem struct {
	world  *ecs.World
	Mapper *entities.EntityManager
	queue  *queue.BoundedQueue
	logger Logger

	// Cached filter for optimal Ark performance
	codeNeededFilter *generic.Filter1[components.CodeNeeded]

	// Performance tracking
	entitiesProcessed int64
	batchesCreated    int64
	totalDropped      uint64
}

// NewBatchCodeSystem creates a new batch code system using Ark best practices
func NewBatchCodeSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchCodeSystem {
	system := &BatchCodeSystem{
		world:  world,
		Mapper: mapper,
		queue:  boundedQueue,
		logger: logger,
	}

	system.initializeComponents()
	return system
}

// Initialize initializes cached ECS filters for optimal performance
func (bcs *BatchCodeSystem) Initialize(w *ecs.World) {
	bcs.initializeComponents()
}

// initializeComponents creates and registers cached filters
func (bcs *BatchCodeSystem) initializeComponents() {
	// Create cached filter and register it (Ark best practice)
	bcs.codeNeededFilter = generic.NewFilter1[components.CodeNeeded](bcs.world).
		Without(generic.T[components.CodePending]()).
		Register()
}

// Update processes code dispatch using Ark's efficient batch operations
func (bcs *BatchCodeSystem) Update(w *ecs.World) {
	start := time.Now()

	// Collect entities and jobs using cached filter
	entityJobMap := bcs.collectWork(w)
	if len(entityJobMap) == 0 {
		return
	}

	// Convert to slices for batch processing
	entities := make([]ecs.Entity, 0, len(entityJobMap))
	jobs := make([]jobs.Job, 0, len(entityJobMap))

	for entity, dispatchableJob := range entityJobMap {
		entities = append(entities, entity)
		jobs = append(jobs, dispatchableJob.job)
	}

	// Process using proper Ark batch operations
	bcs.processBatch(jobs, entities)

	// Update performance metrics
	atomic.AddInt64(&bcs.entitiesProcessed, int64(len(entities)))
	atomic.AddInt64(&bcs.batchesCreated, 1)

	bcs.logger.LogSystemPerformance("BatchCodeSystem", time.Since(start), len(entities))
}

// collectWork collects entities that need code dispatch using cached filter
func (bcs *BatchCodeSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchableCodeJob {
	start := time.Now()
	out := make(map[ecs.Entity]dispatchableCodeJob)

	// Use cached filter for optimal performance
	query := bcs.codeNeededFilter.Query()
	defer query.Close()

	for query.Next() {
		entity := query.Entity()
		codeJobComp := bcs.Mapper.CodeJob.Get(entity)
		if codeJobComp == nil {
			bcs.logger.Warn("Entity[%d] has no code job component", entity.ID())
			continue
		}

		job := codeJobComp.Job
		if job != nil {
			// Get color information for the code job
			color := "default"
			if codeConfig := bcs.Mapper.CodeConfig.Get(entity); codeConfig != nil {
				color = codeConfig.Color
			}

			out[entity] = dispatchableCodeJob{
				job:   job,
				color: color,
			}

			namePtr := bcs.Mapper.Name.Get(entity)
			if namePtr != nil {
				bcs.logger.Debug("Entity[%d] (%s) code job collected (color: %s)", entity.ID(), *namePtr, color)
			}
		} else {
			bcs.logger.Warn("Entity[%d] has no code job", entity.ID())
		}
	}

	bcs.logger.LogSystemPerformance("BatchCodeCollect", time.Since(start), len(out))
	return out
}

// processBatch processes a batch of code jobs using proper Ark batch operations
func (bcs *BatchCodeSystem) processBatch(batchJobs []jobs.Job, batchEntities []ecs.Entity) {
	if len(batchJobs) == 0 {
		return
	}

	// Try to enqueue batch first
	err := bcs.queue.EnqueueBatch(batchJobs)
	if err != nil {
		// CRITICAL FIX: Don't transition state if enqueue fails
		bcs.logger.Warn("Queue full, retrying code batch later: %v", err)
		atomic.AddUint64(&bcs.totalDropped, uint64(len(batchJobs)))
		// Keep entities in CodeNeeded state for retry
		return
	}

	// Only transition state after successful enqueue
	// Use Ark's efficient batch operations instead of individual component changes
	bcs.transitionEntityStates(batchEntities)
}

// transitionEntityStates uses Ark's efficient batch operations for state transitions
func (bcs *BatchCodeSystem) transitionEntityStates(entities []ecs.Entity) {
	if len(entities) == 0 {
		return
	}

	// Filter out entities that are no longer valid or already have CodePending
	validEntities := make([]ecs.Entity, 0, len(entities))
	for _, entity := range entities {
		if bcs.world.Alive(entity) &&
			bcs.Mapper.CodeNeeded.HasAll(entity) &&
			!bcs.Mapper.CodePending.HasAll(entity) {
			validEntities = append(validEntities, entity)
		}
	}

	if len(validEntities) == 0 {
		return
	}

	// Use Ark's efficient batch operations
	// Remove CodeNeeded components in batch
	bcs.Mapper.CodeNeeded.RemoveBatch(validEntities, nil)

	// Add CodePending components in batch
	pendingComponent := &components.CodePending{
		StartTime: time.Now(),
	}
	bcs.Mapper.CodePending.AddBatch(validEntities, pendingComponent)

	// Log transitions for monitoring
	for _, entity := range validEntities {
		namePtr := bcs.Mapper.Name.Get(entity)
		if namePtr != nil {
			// Get color information for logging
			color := "default"
			if codeConfig := bcs.Mapper.CodeConfig.Get(entity); codeConfig != nil {
				color = codeConfig.Color
			}
			bcs.logger.Info("CODE DISPATCHED: %s (color: %s)", *namePtr, color)
		}
	}
}

// GetStats returns performance statistics
func (bcs *BatchCodeSystem) GetStats() BatchCodeStats {
	return BatchCodeStats{
		EntitiesProcessed: atomic.LoadInt64(&bcs.entitiesProcessed),
		BatchesCreated:    atomic.LoadInt64(&bcs.batchesCreated),
		TotalDropped:      atomic.LoadUint64(&bcs.totalDropped),
	}
}

// GetLastUpdateStats returns stats for the controller (compatibility)
func (bcs *BatchCodeSystem) GetLastUpdateStats() (int, int) {
	stats := bcs.GetStats()
	return int(stats.EntitiesProcessed), int(stats.BatchesCreated)
}

// BatchCodeStats provides performance metrics
type BatchCodeStats struct {
	EntitiesProcessed int64  `json:"entities_processed"`
	BatchesCreated    int64  `json:"batches_created"`
	TotalDropped      uint64 `json:"total_dropped"`
}

// Reset resets performance counters
func (bcs *BatchCodeSystem) Reset() {
	atomic.StoreInt64(&bcs.entitiesProcessed, 0)
	atomic.StoreInt64(&bcs.batchesCreated, 0)
	atomic.StoreUint64(&bcs.totalDropped, 0)
}

