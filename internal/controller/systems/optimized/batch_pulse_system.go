package optimized

import (
	"context"
	"fmt"
	"time"
	
	"github.com/mlange-42/ark/ecs"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	optimizedQueue "cpra/internal/queue/optimized"
)

// BatchPulseSystem processes pulse monitoring exactly like sys_pulse.go but in batches
type BatchPulseSystem struct {
	world              *ecs.World
	PulseNeededFilter  *ecs.Filter1[components.PulseNeeded]
	Mapper             *entities.EntityManager
	queue              *optimizedQueue.BoundedQueue
	logger             Logger
	
	// Batching optimization
	batchSize          int
	entitiesProcessed  int64
	batchesCreated     int64
	lastProcessTime    time.Time
}

// NewBatchPulseSystem creates a new batch pulse system using the original queue approach
func NewBatchPulseSystem(world *ecs.World, mapper *entities.EntityManager, queue *optimizedQueue.BoundedQueue, batchSize int, logger Logger) *BatchPulseSystem {
	system := &BatchPulseSystem{
		world:          world,
		Mapper:         mapper,
		queue:          queue,
		batchSize:      batchSize,
		logger:         logger,
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

// applyWork applies work exactly like sys_pulse.go but in batches
func (bps *BatchPulseSystem) applyWork(w *ecs.World, entities []ecs.Entity, jobs []jobs.Job) error {
	for i, ent := range entities {
		_ = jobs[i] // Job already submitted to queue
		
		if w.Alive(ent) {
			// Prevent component duplication exactly like original
			if bps.Mapper.PulsePending.HasAll(ent) {
				namePtr := bps.Mapper.Name.Get(ent)
				if namePtr != nil {
					bps.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, ent.ID())
				}
				continue
			}

			// Job will be submitted with batch - just continue with component transition

			// Safe component transition exactly like original
			if bps.Mapper.PulseNeeded.HasAll(ent) {
				bps.Mapper.PulseNeeded.Remove(ent)
				bps.Mapper.PulsePending.Add(ent, &components.PulsePending{})

				namePtr := bps.Mapper.Name.Get(ent)
				if namePtr != nil {
					bps.logger.Debug("Dispatched %s job for entity: %d", *namePtr, ent.ID())
				}
				bps.logger.LogComponentState(ent.ID(), "PulseNeeded->PulsePending", "transitioned")
			}
		}
	}
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
	entities := make([]ecs.Entity, 0, len(toDispatch))
	jobs := make([]jobs.Job, 0, len(toDispatch))
	
	for ent, job := range toDispatch {
		entities = append(entities, ent)
		jobs = append(jobs, job)
	}
	
	// Process in batches - collect jobs and submit as batch
	batchCount := 0
	for i := 0; i < len(entities); i += bps.batchSize {
		end := i + bps.batchSize
		if end > len(entities) {
			end = len(entities)
		}
		
		batchEntities := entities[i:end]
		batchJobs := jobs[i:end]
		
		// Submit batch of jobs to queue
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
		fmt.Printf("Batch Pulse System: Processed %d entities in %d batches (took %v)\n", 
			len(toDispatch), batchCount, processingTime.Truncate(time.Millisecond))
	}
	
	return nil
}

// Finalize cleans up like the original system
func (bps *BatchPulseSystem) Finalize(w *ecs.World) {
	// Nothing to clean up - queue manager handles its own cleanup
}