package systems

import (
    "cpra/internal/controller/components"
    "cpra/internal/queue"
    "sync"
    "time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseSystem processes entities that need a pulse check.
// It identifies entities with the StatePulseNeeded flag, enqueues the corresponding job,
// and transitions the entity state to StatePulsePending.
type BatchPulseSystem struct {
	world       *ecs.World
	queue       queue.Queue // Using a generic queue interface
	logger      Logger
	batchSize   int
	maxDispatch int

	// Filter for entities that require a pulse check.
	filter             *ecs.Filter2[components.MonitorState, components.JobStorage]
	monitorStateMapper *ecs.Map[components.MonitorState]

	jobPool    *sync.Pool
	entityPool *sync.Pool
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

func (s *BatchPulseSystem) Initialize(w *ecs.World) {
    if s.filter != nil {
        s.filter.Register()
    }
}

func (s *BatchPulseSystem) SetMaxDispatch(n int) {
	s.maxDispatch = n
}

// Update finds and processes all monitors that need a pulse check.
func (s *BatchPulseSystem) Update(w *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Pulse queue saturated (%d/%d); deferring dispatch", stats.QueueDepth, stats.Capacity)
	}

	query := s.filter.Query()

	var tokens int
	if stats.Capacity <= 0 {
		// Sentinel capacity <= 0 signals an unbounded queue (Workiva implementation).
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
	if s.maxDispatch > 0 && tokens > s.maxDispatch {
		tokens = s.maxDispatch
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
		state, jobStorage := query.Get()

    // Process only entities that need a pulse check.
    if (state.Flags & components.StatePulseNeeded) == 0 {
        continue
    }

		if jobStorage.PulseJob == nil {
			s.logger.Warn("Entity[%d] has PulseNeeded state but no PulseJob", ent.ID())
			continue
		}

		jobsToQueue = append(jobsToQueue, jobStorage.PulseJob)
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
		s.logger.LogSystemPerformance("BatchPulseSystem", time.Since(startTime), processedCount)
	}

}

// processBatch attempts to enqueue a batch of jobs and updates entity states on success.
func (s *BatchPulseSystem) processBatch(jobs *[]interface{}, entities *[]ecs.Entity) {
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Pulse queue near capacity (%d/%d); skipping enqueue", stats.QueueDepth, stats.Capacity)
		return
	}
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

        // Transition from Needed -> Pending
        if state.Flags&components.StatePulseNeeded != 0 {
            state.Flags &^= components.StatePulseNeeded
            state.Flags |= components.StatePulsePending
            state.LastCheckTime = now
        }
    }
}

// Finalize is a no-op for this system.
func (s *BatchPulseSystem) Finalize(w *ecs.World) {}
