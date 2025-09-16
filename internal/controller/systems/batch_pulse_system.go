package systems

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"
)

// BatchPulseSystem processes pulse monitoring using proper Ark batch operations
// Replaces individual component operations with efficient MapBatchFn calls
type BatchPulseSystem struct {
	world  *ecs.World
	Mapper *entities.EntityManager
	queue  *queue.BoundedQueue
	logger Logger

	// Cached filters for optimal Ark performance
	pulseNeededFilter *generic.Filter1[components.PulseNeeded]
	
	// Performance tracking
	entitiesProcessed int64
	batchesCreated    int64
	lastProcessTime   time.Time
	totalDropped      uint64
}

// NewBatchPulseSystem creates a new batch pulse system using proper Ark patterns
func NewBatchPulseSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchPulseSystem {
	system := &BatchPulseSystem{
		world:           world,
		Mapper:          mapper,
		queue:           boundedQueue,
		logger:          logger,
		lastProcessTime: time.Now(),
	}

	// Initialize cached filters (Ark best practice)
	system.initializeComponents()
	return system
}

// Initialize initializes cached ECS filters for optimal performance
func (bps *BatchPulseSystem) Initialize(w *ecs.World) {
	bps.initializeComponents()
}

// initializeComponents creates and registers cached filters
func (bps *BatchPulseSystem) initializeComponents() {
	// Create cached filter and register it for optimal performance
	bps.pulseNeededFilter = generic.NewFilter1[components.PulseNeeded](bps.world).
		Without(generic.T[components.PulsePending]()).
		Register()
}

// Update processes pulse checks using Ark's efficient batch operations
func (bps *BatchPulseSystem) Update(w *ecs.World) {
	start := time.Now()
	
	// Collect entities and jobs using cached filter
	entityJobMap := bps.collectWork(w)
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
	bps.processBatch(jobs, entities)
	
	// Update performance metrics
	atomic.AddInt64(&bps.entitiesProcessed, int64(len(entities)))
	atomic.AddInt64(&bps.batchesCreated, 1)
	bps.lastProcessTime = time.Now()
	
	bps.logger.LogSystemPerformance("BatchPulseSystem", time.Since(start), len(entities))
}

// collectWork collects entities that need pulse checks using cached filter
func (bps *BatchPulseSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)
	
	// Use cached filter for optimal performance
	query := bps.pulseNeededFilter.Query()
	defer query.Close()

	for query.Next() {
		entity := query.Entity()
		pulseJobComp := bps.Mapper.PulseJob.Get(entity)
		if pulseJobComp == nil {
			bps.logger.Warn("Entity[%d] has no pulse job component", entity.ID())
			continue
		}
		
		job := pulseJobComp.Job
		if job != nil {
			out[entity] = job

			namePtr := bps.Mapper.Name.Get(entity)
			if namePtr != nil {
				bps.logger.Debug("Entity[%d] (%s) pulse job collected", entity.ID(), *namePtr)
			}
		} else {
			bps.logger.Warn("Entity[%d] has no pulse job", entity.ID())
		}
	}

	bps.logger.LogSystemPerformance("BatchPulseCollect", time.Since(start), len(out))
	return out
}

// processBatch processes a batch of jobs using proper Ark batch operations
func (bps *BatchPulseSystem) processBatch(batchJobs []jobs.Job, batchEntities []ecs.Entity) {
	if len(batchJobs) == 0 {
		return
	}
	
	// Try to enqueue batch first
	err := bps.queue.EnqueueBatch(batchJobs)
	if err != nil {
		// CRITICAL FIX: Don't transition state if enqueue fails
		bps.logger.Warn("Queue full, retrying batch later: %v", err)
		atomic.AddUint64(&bps.totalDropped, uint64(len(batchJobs)))
		// Keep entities in PulseNeeded state for retry
		return
	}

	// Only transition state after successful enqueue
	// Use Ark's efficient batch operations instead of individual component changes
	bps.transitionEntityStates(batchEntities)
}

// transitionEntityStates uses Ark's efficient batch operations for state transitions
func (bps *BatchPulseSystem) transitionEntityStates(entities []ecs.Entity) {
	if len(entities) == 0 {
		return
	}
	
	// Filter out entities that are no longer valid or already have PulsePending
	validEntities := make([]ecs.Entity, 0, len(entities))
	for _, entity := range entities {
		if bps.world.Alive(entity) && 
		   bps.Mapper.PulseNeeded.HasAll(entity) && 
		   !bps.Mapper.PulsePending.HasAll(entity) {
			validEntities = append(validEntities, entity)
		}
	}
	
	if len(validEntities) == 0 {
		return
	}
	
	// Use Ark's efficient batch operations
	// Remove PulseNeeded components in batch
	bps.Mapper.PulseNeeded.RemoveBatch(validEntities, nil)
	
	// Add PulsePending components in batch
	pendingComponent := &components.PulsePending{
		StartTime: time.Now(),
	}
	bps.Mapper.PulsePending.AddBatch(validEntities, pendingComponent)
	
	// Log transitions for monitoring
	for _, entity := range validEntities {
		namePtr := bps.Mapper.Name.Get(entity)
		if namePtr != nil {
			pulseConfig := bps.Mapper.PulseConfig.Get(entity)
			pulseStatus := bps.Mapper.PulseStatus.Get(entity)
			
			if pulseConfig != nil && pulseStatus != nil {
				interval := pulseConfig.Interval
				isFirstCheck := pulseStatus.LastCheckTime.IsZero() || bps.Mapper.PulseFirstCheck.HasAll(entity)
				
				if isFirstCheck {
					bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, FIRST CHECK)", 
						*namePtr, interval)
				} else {
					timeSinceLastCheck := time.Since(pulseStatus.LastCheckTime)
					delay := timeSinceLastCheck - interval
					
					if delay > 0 {
						bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, delay: %v)", 
							*namePtr, interval, delay)
					} else {
						bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, on time)", 
							*namePtr, interval)
					}
				}
			}
		}
	}
}

// GetStats returns performance statistics
func (bps *BatchPulseSystem) GetStats() BatchPulseStats {
	return BatchPulseStats{
		EntitiesProcessed: atomic.LoadInt64(&bps.entitiesProcessed),
		BatchesCreated:    atomic.LoadInt64(&bps.batchesCreated),
		TotalDropped:      atomic.LoadUint64(&bps.totalDropped),
		LastProcessTime:   bps.lastProcessTime,
	}
}

// BatchPulseStats provides performance metrics
type BatchPulseStats struct {
	EntitiesProcessed int64     `json:"entities_processed"`
	BatchesCreated    int64     `json:"batches_created"`
	TotalDropped      uint64    `json:"total_dropped"`
	LastProcessTime   time.Time `json:"last_process_time"`
}

// Reset resets performance counters
func (bps *BatchPulseSystem) Reset() {
	atomic.StoreInt64(&bps.entitiesProcessed, 0)
	atomic.StoreInt64(&bps.batchesCreated, 0)
	atomic.StoreUint64(&bps.totalDropped, 0)
	bps.lastProcessTime = time.Now()
}

