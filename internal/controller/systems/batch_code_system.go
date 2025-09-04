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

// dispatchableCodeJob holds job and color info exactly like sys_code.go
type dispatchableCodeJob struct {
	job   jobs.Job
	color string
}

// BatchCodeSystem processes code dispatch exactly like sys_code.go but in batches
type BatchCodeSystem struct {
	world            *ecs.World
	CodeNeededFilter *ecs.Filter1[components.CodeNeeded]
	Mapper           *entities.EntityManager
	queue            *queue.BoundedQueue
	logger           Logger

	// Batching optimization
	batchSize         int
	entitiesProcessed int64
	batchesCreated    int64
}

// NewBatchCodeSystem creates a new batch code system using the original queue approach
func NewBatchCodeSystem(world *ecs.World, mapper *entities.EntityManager, boundedQueue *queue.BoundedQueue, batchSize int, logger Logger) *BatchCodeSystem {
	system := &BatchCodeSystem{
		world:     world,
		Mapper:    mapper,
		queue:     boundedQueue,
		batchSize: batchSize,
		logger:    logger,
	}

	system.initializeComponents()
	return system
}

// Initialize initializes ECS filters exactly like sys_code.go
func (bcs *BatchCodeSystem) Initialize(w *ecs.World) {
	bcs.initializeComponents()
}

// initializeComponents initializes ECS filters exactly like the original system
func (bcs *BatchCodeSystem) initializeComponents() {
	// Exactly like sys_code.go - entities with CodeNeeded but not CodePending
	bcs.CodeNeededFilter = ecs.NewFilter1[components.CodeNeeded](bcs.world).
		Without(ecs.C[components.CodePending]())
}

// collectWork collects entities and jobs exactly like sys_code.go
func (bcs *BatchCodeSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchableCodeJob {
	start := time.Now()
	out := make(map[ecs.Entity]dispatchableCodeJob)
	query := bcs.CodeNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		color := query.Get().Color
		var job jobs.Job

		// Exact same color logic as sys_code.go with nil checks
		switch color {
		case "red":
			if bcs.Mapper.RedCode.HasAll(ent) {
				if jobComp := bcs.Mapper.RedCodeJob.Get(ent); jobComp != nil {
					job = jobComp.Job
				}
			}
		case "green":
			if bcs.Mapper.GreenCode.HasAll(ent) {
				if jobComp := bcs.Mapper.GreenCodeJob.Get(ent); jobComp != nil {
					job = jobComp.Job
				}
			}
		case "yellow":
			if bcs.Mapper.YellowCode.HasAll(ent) {
				if jobComp := bcs.Mapper.YellowCodeJob.Get(ent); jobComp != nil {
					job = jobComp.Job
				}
			}
		case "cyan":
			if bcs.Mapper.CyanCode.HasAll(ent) {
				if jobComp := bcs.Mapper.CyanCodeJob.Get(ent); jobComp != nil {
					job = jobComp.Job
				}
			}
		case "gray":
			if bcs.Mapper.GrayCode.HasAll(ent) {
				if jobComp := bcs.Mapper.GrayCodeJob.Get(ent); jobComp != nil {
					job = jobComp.Job
				}
			}
		default:
			bcs.logger.Warn("Unknown color %q for entity %v", color, ent)
		}

		if job != nil {
			out[ent] = dispatchableCodeJob{job: job, color: color}
		}
	}

	bcs.logger.LogSystemPerformance("BatchCodeDispatch", time.Since(start), len(out))
	return out
}

// applyWork applies work exactly like sys_code.go but in batches
func (bcs *BatchCodeSystem) applyWork(w *ecs.World, entities []ecs.Entity, jobs []dispatchableCodeJob) error {
	for i, ent := range entities {
		item := jobs[i]

		if w.Alive(ent) {
			// Prevent component duplication exactly like original
			if bcs.Mapper.CodePending.HasAll(ent) {
				namePtr := bcs.Mapper.Name.Get(ent)
				if namePtr != nil {
					bcs.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %v", *namePtr, ent)
				}
				continue
			}

			// Job will be submitted with batch

			// Safe component transition exactly like original
			if bcs.Mapper.CodeNeeded.HasAll(ent) {
				bcs.Mapper.CodeNeeded.Remove(ent)
				bcs.Mapper.CodePending.Add(ent, &components.CodePending{Color: item.color})

				namePtr := bcs.Mapper.Name.Get(ent)
				if namePtr != nil {
					bcs.logger.Debug("Dispatched %s code job for entity: %d", item.color, ent.ID())
				}
				bcs.logger.LogComponentState(ent.ID(), "CodeNeeded->CodePending", "transitioned")
			}
		}
	}
	return nil
}

// Update processes entities using the exact same flow as sys_code.go but in batches
func (bcs *BatchCodeSystem) Update(ctx context.Context) error {
	// Collect work exactly like original system
	toDispatch := bcs.collectWork(bcs.world)

	if len(toDispatch) == 0 {
		return nil
	}

	// Convert map to slices for batch processing
	entities := make([]ecs.Entity, 0, len(toDispatch))
	dispatchableJobs := make([]dispatchableCodeJob, 0, len(toDispatch))

	for ent, job := range toDispatch {
		entities = append(entities, ent)
		dispatchableJobs = append(dispatchableJobs, job)
	}

	// Process in batches - collect jobs and submit as batch
	batchCount := 0
	for i := 0; i < len(entities); i += bcs.batchSize {
		end := i + bcs.batchSize
		if end > len(entities) {
			end = len(entities)
		}

		batchEntities := entities[i:end]
		batchJobs := dispatchableJobs[i:end]

		// Submit batch of jobs to queue
		jobsToSubmit := make([]jobs.Job, 0, len(batchJobs))
		for _, item := range batchJobs {
			jobsToSubmit = append(jobsToSubmit, item.job)
		}
		if err := bcs.queue.EnqueueBatch(jobsToSubmit); err != nil {
			bcs.logger.Warn("Failed to enqueue batch %d, queue full: %v", batchCount, err)
		}

		// Apply component transitions
		if err := bcs.applyWork(bcs.world, batchEntities, batchJobs); err != nil {
			return err
		}

		batchCount++
	}

	bcs.entitiesProcessed += int64(len(toDispatch))
	bcs.batchesCreated += int64(batchCount)

	return nil
}

// Finalize cleans up like the original system
func (bcs *BatchCodeSystem) Finalize(w *ecs.World) {
	// Nothing to clean up - queue manager handles its own cleanup
}

// GetMetrics returns current system metrics
func (bcs *BatchCodeSystem) GetMetrics() (int64, int64) {
	return bcs.entitiesProcessed, bcs.batchesCreated
}
