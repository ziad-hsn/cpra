package systems

import (
    "cpra/internal/controller/components"
    "cpra/internal/queue"
    "sync"
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

	jobPool    *sync.Pool
	entityPool *sync.Pool
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
		jobPool: &sync.Pool{
			New: func() interface{} {
				s := make([]interface{}, 0, batchSize)
				return &s
			},
		},
		entityPool: &sync.Pool{
			New: func() interface{} {
				s := make([]ecs.Entity, 0, batchSize)
				return &s
			},
		},
	}
}

func (s *BatchInterventionSystem) Initialize(w *ecs.World) {
    if s.filter != nil {
        s.filter.Register()
    }
}

// Update finds and processes all monitors that need an intervention.
func (s *BatchInterventionSystem) Update(w *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Intervention queue saturated (%d/%d); deferring dispatch", stats.QueueDepth, stats.Capacity)
	}

	query := s.filter.Query()

	var tokens int
	if stats.Capacity <= 0 {
		tokens = s.batchSize
		if tokens <= 0 {
			tokens = 1
		}
	} else {
		free := stats.Capacity - stats.QueueDepth
		if free <= 0 {
			return
		}
		tokens = int(float64(free) * 0.8)
		if tokens <= 0 {
			tokens = free
		}
		if tokens <= 0 {
			tokens = 1
		}
	}

	earlyExit := false

	jobsPtr := s.jobPool.Get().(*[]interface{})
	entitiesPtr := s.entityPool.Get().(*[]ecs.Entity)
	jobsToQueue := (*jobsPtr)[:0]
	entitiesToUpdate := (*entitiesPtr)[:0]
	processedCount := 0

	defer func() {
		s.jobPool.Put(jobsPtr)
		s.entityPool.Put(entitiesPtr)
	}()

	for query.Next() {
		ent := query.Entity()
		state, _, jobStorage := query.Get()

    // Process only entities that need an intervention.
    if (state.Flags & components.StateInterventionNeeded) == 0 {
        continue
    }

		if jobStorage.InterventionJob == nil {
			s.logger.Warn("Entity[%d] has InterventionNeeded state but no InterventionJob", ent.ID())
			continue
		}

		jobsToQueue = append(jobsToQueue, jobStorage.InterventionJob)
		entitiesToUpdate = append(entitiesToUpdate, ent)

		if len(jobsToQueue) >= tokens {
			s.processBatch(&jobsToQueue, &entitiesToUpdate)
			processedCount += len(jobsToQueue)
			jobsToQueue = jobsToQueue[:0]
			entitiesToUpdate = entitiesToUpdate[:0]
			earlyExit = true
			break
		}
	}

	// Process any remaining entities
	if earlyExit {
		query.Close()
	}

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
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Intervention queue near capacity (%d/%d); skipping enqueue", stats.QueueDepth, stats.Capacity)
		return
	}
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

        // Transition from Needed -> Pending
        if state.Flags&components.StateInterventionNeeded != 0 {
            state.Flags &^= components.StateInterventionNeeded
            state.Flags |= components.StateInterventionPending
            s.logger.Info("INTERVENTION DISPATCHED: %s", state.Name)
        }
    }
}

// Finalize is a no-op for this system.
func (s *BatchInterventionSystem) Finalize(w *ecs.World) {}
