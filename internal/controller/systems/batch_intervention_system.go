package systems

import (
	"context"
	"cpra/internal/queue"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionSystem processes intervention dispatch exactly like sys_intervention.go but in batches
type BatchInterventionSystem struct {
	world                    *ecs.World
	InterventionNeededFilter *ecs.Filter1[components.InterventionNeeded]
	Mapper                   *entities.EntityManager
	queue                    *queue.BoundedQueue
	logger                   Logger

	// Batching optimization
	batchSize         int
	entitiesProcessed int64
	batchesCreated    int64
}

// NewBatchInterventionSystem creates a new batch intervention system using the original queue approach
func NewBatchInterventionSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchInterventionSystem {
	system := &BatchInterventionSystem{
		world:     world,
		Mapper:    mapper,
		queue:     boundedQueue,
		batchSize: batchSize,
		logger:    logger,
	}

	system.initializeComponents()
	return system
}

// Initialize initializes ECS filters exactly like sys_intervention.go
func (bis *BatchInterventionSystem) Initialize(w *ecs.World) {
	bis.initializeComponents()
}

// initializeComponents initializes ECS filters exactly like the original system
func (bis *BatchInterventionSystem) initializeComponents() {
	// Exactly like sys_intervention.go - entities with InterventionNeeded but not InterventionPending
	bis.InterventionNeededFilter = ecs.NewFilter1[components.InterventionNeeded](bis.world).
		Without(ecs.C[components.InterventionPending]())
}

// collectWork collects entities and jobs exactly like sys_intervention.go
func (bis *BatchInterventionSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)
	query := bis.InterventionNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		interventionJobComp := bis.Mapper.InterventionJob.Get(ent)
		if interventionJobComp == nil {
			bis.logger.Warn("Entity[%d] has no intervention job component", ent.ID())
			continue
		}
		job := interventionJobComp.Job
		if job != nil {
			out[ent] = job

			namePtr := bis.Mapper.Name.Get(ent)
			if namePtr != nil {
				bis.logger.Debug("Entity[%d] (%s) intervention job collected", ent.ID(), *namePtr)
			}
		} else {
			bis.logger.Warn("Entity[%d] has no intervention job", ent.ID())
		}
	}

	bis.logger.LogSystemPerformance("BatchInterventionDispatch", time.Since(start), len(out))
	return out
}

// applyWork applies work using Ark's batch operations for optimal performance
func (bis *BatchInterventionSystem) applyWork(w *ecs.World, entities []ecs.Entity, jobs []jobs.Job) error {
	if len(entities) == 0 {
		return nil
	}

	// Filter entities to only include those that need transitions and don't already have InterventionPending
	validEntities := make([]ecs.Entity, 0, len(entities))
	for i, ent := range entities {
		_ = jobs[i] // Job already submitted to queue

		if w.Alive(ent) {
			// Skip entities that already have InterventionPending
			if bis.Mapper.InterventionPending.HasAll(ent) {
				namePtr := bis.Mapper.Name.Get(ent)
				if namePtr != nil {
					bis.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, ent.ID())
				}
				continue
			}

			// Only include entities that have InterventionNeeded
			if bis.Mapper.InterventionNeeded.HasAll(ent) {
				validEntities = append(validEntities, ent)
			}
		}
	}

	if len(validEntities) == 0 {
		return nil
	}

	// Use Ark's batch operations for component transitions
	// Create a filter for entities that have InterventionNeeded but not InterventionPending
	interventionNeededFilter := ecs.NewFilter1[components.InterventionNeeded](w).
		Without(ecs.C[components.InterventionPending]())
	
	// Get batch of matching entities
	batch := interventionNeededFilter.Batch()
	
	// Use batch operations: Remove InterventionNeeded and Add InterventionPending in batches
	bis.Mapper.InterventionNeeded.RemoveBatch(batch, nil)
	bis.Mapper.InterventionPending.AddBatch(batch, &components.InterventionPending{})

	// Log component transitions
	for _, ent := range validEntities {
		namePtr := bis.Mapper.Name.Get(ent)
		if namePtr != nil {
			bis.logger.Info("INTERVENTION DISPATCHED: %s", *namePtr)
		}
		bis.logger.LogComponentState(ent.ID(), "InterventionNeeded->InterventionPending", "transitioned")
	}

	bis.logger.Debug("Batch intervention system: Applied component transitions to %d entities using batch operations", len(validEntities))
	return nil
}

// Update processes entities using the exact same flow as sys_intervention.go but in batches
func (bis *BatchInterventionSystem) Update(ctx context.Context) error {
	// Collect work exactly like original system
	toDispatch := bis.collectWork(bis.world)

	bis.logger.Debug("Intervention system found %d e to dispatch", len(toDispatch))

	if len(toDispatch) == 0 {
		return nil
	}

	bis.logger.Info("Batch Intervention System: Processing %d e", len(toDispatch))

	// Convert map to slices for batch processing
	e := make([]ecs.Entity, 0, len(toDispatch))
	j := make([]jobs.Job, 0, len(toDispatch))

	for ent, job := range toDispatch {
		e = append(e, ent)
		j = append(j, job)
	}

	// Process in batches - collect j and submit as batch
	batchCount := 0
	for i := 0; i < len(e); i += bis.batchSize {
		end := i + bis.batchSize
		if end > len(e) {
			end = len(e)
		}

		batchEntities := e[i:end]
		batchJobs := j[i:end]

		// Submit batch of j to queue
		if err := bis.queue.EnqueueBatch(batchJobs); err != nil {
			bis.logger.Warn("Failed to enqueue batch %d, queue full: %v", batchCount, err)
		}

		// Apply component transitions
		if err := bis.applyWork(bis.world, batchEntities, batchJobs); err != nil {
			return err
		}

		batchCount++
	}

	bis.entitiesProcessed += int64(len(toDispatch))
	bis.batchesCreated += int64(batchCount)

	return nil
}

// Finalize cleans up like the original system
func (bis *BatchInterventionSystem) Finalize(w *ecs.World) {
	// Nothing to clean up - queue manager handles its own cleanup
}

// GetMetrics returns current system metrics
func (bis *BatchInterventionSystem) GetMetrics() (int64, int64) {
	return bis.entitiesProcessed, bis.batchesCreated
}
