package systems

import (
	"context"
	"cpra/internal/queue"
	"fmt"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// BatchPulseSystem processes pulse monitoring exactly like sys_pulse.go but in batches
type BatchPulseSystem struct {
	world             *ecs.World
	PulseNeededFilter *ecs.Filter1[components.PulseNeeded]
	Mapper            *entities.EntityManager
	queue             *queue.BoundedQueue
	logger            Logger

	// Batching optimization
	batchSize         int
	entitiesProcessed int64
	batchesCreated    int64
	lastProcessTime   time.Time
}

// NewBatchPulseSystem creates a new batch pulse system using the original queue approach
func NewBatchPulseSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchPulseSystem {
	system := &BatchPulseSystem{
		world:           world,
		Mapper:          mapper,
		queue:           boundedQueue,
		batchSize:       batchSize,
		logger:          logger,
		lastProcessTime: time.Now(),
	}

	// Initialize ECS filter exactly like the original system
	system.initializeComponents()

	return system
}

// Initialize initializes ECS filters exactly like sys_pulse.go
func (bps *BatchPulseSystem) Initialize(w *ecs.World) {
	bps.initializeComponents()
}

// initializeComponents initializes ECS filters exactly like the original system
func (bps *BatchPulseSystem) initializeComponents() {
	// Exactly like sys_pulse.go - entities with PulseNeeded but not PulsePending
	bps.PulseNeededFilter = ecs.NewFilter1[components.PulseNeeded](bps.world).
		Without(ecs.C[components.PulsePending]())
}

// collectWork collects entities and jobs exactly like sys_pulse.go
func (bps *BatchPulseSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)
	query := bps.PulseNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		pulseJobComp := bps.Mapper.PulseJob.Get(ent)
		if pulseJobComp == nil {
			bps.logger.Warn("Entity[%d] has no pulse job component", ent.ID())
			continue
		}
		job := pulseJobComp.Job
		if job != nil {
			out[ent] = job

			namePtr := bps.Mapper.Name.Get(ent)
			if namePtr != nil {
				bps.logger.Debug("Entity[%d] (%s) pulse job collected", ent.ID(), *namePtr)
			}
		} else {
			bps.logger.Warn("Entity[%d] has no pulse job", ent.ID())
		}
	}

	bps.logger.LogSystemPerformance("BatchPulseDispatch", time.Since(start), len(out))
	return out
}

// applyWork applies work using Ark's batch operations for optimal performance
func (bps *BatchPulseSystem) applyWork(w *ecs.World, entities []ecs.Entity, jobs []jobs.Job) error {
	if len(entities) == 0 {
		return nil
	}

	// Filter entities to only include those that need transitions and don't already have PulsePending
	validEntities := make([]ecs.Entity, 0, len(entities))
	for i, ent := range entities {
		_ = jobs[i] // Job already submitted to queue

		if w.Alive(ent) {
			// Skip entities that already have PulsePending
			if bps.Mapper.PulsePending.HasAll(ent) {
				namePtr := bps.Mapper.Name.Get(ent)
				if namePtr != nil {
					bps.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, ent.ID())
				}
				continue
			}

			// Only include entities that have PulseNeeded
			if bps.Mapper.PulseNeeded.HasAll(ent) {
				validEntities = append(validEntities, ent)
			}
		}
	}

	if len(validEntities) == 0 {
		return nil
	}

	// Use Ark's batch operations for component transitions
	// Create a filter for entities that have PulseNeeded but not PulsePending
	pulseNeededFilter := ecs.NewFilter1[components.PulseNeeded](w).
		Without(ecs.C[components.PulsePending]())
	
	// Get batch of matching entities
	batch := pulseNeededFilter.Batch()
	
	// Use batch operations: Remove PulseNeeded and Add PulsePending in batches
	bps.Mapper.PulseNeeded.RemoveBatch(batch, nil)
	bps.Mapper.PulsePending.AddBatch(batch, &components.PulsePending{})

	// Log component transitions with timing information
	for _, ent := range validEntities {
		namePtr := bps.Mapper.Name.Get(ent)
		if namePtr != nil {
			// Get pulse configuration to show interval and calculate delay
			pulseConfig := bps.Mapper.PulseConfig.Get(ent)
			pulseStatus := bps.Mapper.PulseStatus.Get(ent)
			
			if pulseConfig != nil && pulseStatus != nil {
				interval := pulseConfig.Interval
				
				// Check if this is the first check (LastCheckTime is zero or entity has PulseFirstCheck)
				isFirstCheck := pulseStatus.LastCheckTime.IsZero() || bps.Mapper.PulseFirstCheck.HasAll(ent)
				
				if isFirstCheck {
					bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, FIRST CHECK)", 
						*namePtr, interval)
				} else {
					timeSinceLastCheck := time.Since(pulseStatus.LastCheckTime)
					delay := timeSinceLastCheck - interval
					
					if delay > 0 {
						bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, last check: %v ago, delay: %v)", 
							*namePtr, interval, timeSinceLastCheck.Truncate(time.Second), delay.Truncate(time.Second))
					} else {
						bps.logger.Info("PULSE DISPATCHED: %s (interval: %v, last check: %v ago)", 
							*namePtr, interval, timeSinceLastCheck.Truncate(time.Second))
					}
				}
			} else {
				bps.logger.Info("PULSE DISPATCHED: %s", *namePtr)
			}
		}
		bps.logger.LogComponentState(ent.ID(), "PulseNeeded->PulsePending", "transitioned")
	}

	bps.logger.Debug("Batch pulse system: Applied component transitions to %d entities using batch operations", len(validEntities))
	return nil
}

// Update processes entities using the exact same flow as sys_pulse.go but in batches
func (bps *BatchPulseSystem) Update(ctx context.Context) error {
	startTime := time.Now()

	// Collect work exactly like original system
	toDispatch := bps.collectWork(bps.world)

	if len(toDispatch) == 0 {
		return nil
	}

	// Convert map to slices for batch processing
	e := make([]ecs.Entity, 0, len(toDispatch))
	j := make([]jobs.Job, 0, len(toDispatch))

	for ent, job := range toDispatch {
		e = append(e, ent)
		j = append(j, job)
	}

	// Process in batches - collect j and submit as batch
	batchCount := 0
	for i := 0; i < len(e); i += bps.batchSize {
		end := i + bps.batchSize
		if end > len(e) {
			end = len(e)
		}

		batchEntities := e[i:end]
		batchJobs := j[i:end]

		// Submit batch of j to queue
		if err := bps.queue.EnqueueBatch(batchJobs); err != nil {
			// If queue full, apply component transitions anyway
			bps.logger.Warn("Failed to enqueue batch %d, queue full: %v", batchCount, err)
		}

		// Apply component transitions
		if err := bps.applyWork(bps.world, batchEntities, batchJobs); err != nil {
			return fmt.Errorf("failed to process batch %d: %w", batchCount, err)
		}

		batchCount++
	}

	bps.entitiesProcessed += int64(len(toDispatch))
	bps.batchesCreated += int64(batchCount)

	if len(toDispatch) > 0 {
		processingTime := time.Since(startTime)
		bps.logger.Debug("Batch Pulse System: Processed %d entities in %d batches (took %v)",
			len(toDispatch), batchCount, processingTime.Truncate(time.Millisecond))
	}

	return nil
}

// Finalize cleans up like the original system
func (bps *BatchPulseSystem) Finalize(w *ecs.World) {
	// Nothing to clean up - queue manager handles its own cleanup
}

// GetMetrics returns current system metrics
func (bps *BatchPulseSystem) GetMetrics() (int64, int64) {
	return bps.entitiesProcessed, bps.batchesCreated
}
