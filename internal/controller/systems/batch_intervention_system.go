package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchInterventionSystem processes entities that need an intervention.
//
// It consumes entities from the intervention ready queue (populated by the
// pulse result system on failure) instead of scanning every entity each tick,
// so the per-tick cost is O(M) where M is the number of dispatched interventions.
type BatchInterventionSystem struct {
	queue              queue.Queue
	logger             Logger
	stateLogger        *StateLogger
	world              *ecs.World
	ready              *ReadyQueue
	monitorStateMapper *ecs.Map1[components.MonitorState]
	jobStorageMapper   *ecs.Map1[components.JobStorage]
	batchSize          int
}

// NewBatchInterventionSystem creates a new BatchInterventionSystem.
func NewBatchInterventionSystem(world *ecs.World, q queue.Queue, ready *ReadyQueue, batchSize int, logger Logger, stateLogger *StateLogger) *BatchInterventionSystem {
	return &BatchInterventionSystem{
		world:              world,
		queue:              q,
		logger:             logger,
		stateLogger:        stateLogger,
		ready:              ready,
		batchSize:          batchSize,
		monitorStateMapper: ecs.NewMap1[components.MonitorState](world),
		jobStorageMapper:   ecs.NewMap1[components.JobStorage](world),
	}
}

func (s *BatchInterventionSystem) Initialize(_ *ecs.World) {
}

// Update consumes up to tokens entities from the ready queue and enqueues
// their intervention jobs.
func (s *BatchInterventionSystem) Update(_ *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Intervention queue saturated", "depth", stats.QueueDepth, "capacity", stats.Capacity)
	}

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
	}

	batch := s.ready.Consume(tokens)
	if len(batch) == 0 {
		return
	}

	jobsToQueue := make([]interface{}, 0, len(batch))
	entitiesToUpdate := make([]ecs.Entity, 0, len(batch))
	for _, ent := range batch {
		if !s.world.Alive(ent) {
			continue
		}
		state := s.monitorStateMapper.Get(ent)
		if state == nil || !state.IsInterventionNeeded() || state.IsInterventionPending() {
			continue
		}
		if schema.InMaintenance(state.Maintenance, startTime) {
			s.ready.Enqueue([]ecs.Entity{ent})
			continue
		}
		jobStorage := s.jobStorageMapper.Get(ent)
		if jobStorage == nil || jobStorage.InterventionJob == nil || jobStorage.InterventionJob.IsNil() {
			s.logger.Warn("Entity has InterventionNeeded state but no valid InterventionJob", "entity_id", ent.ID())
			continue
		}

		// Copy the job before enqueue: the stored pointer is re-enqueued across
		// ticks and may be executed concurrently, so we must not share it.
		jobsToQueue = append(jobsToQueue, jobs.NewDispatch(jobStorage.InterventionJob, ent, "intervention", "", state.InterventionGeneration+1, 0))
		entitiesToUpdate = append(entitiesToUpdate, ent)
	}

	if len(jobsToQueue) == 0 {
		return
	}

	if !s.processBatch(&jobsToQueue, &entitiesToUpdate) {
		// Enqueue failed; re-queue the entities so they are retried next tick.
		s.ready.Enqueue(entitiesToUpdate)
		return
	}

	s.logger.LogSystemPerformance("BatchInterventionSystem", time.Since(startTime), len(entitiesToUpdate))
}

// processBatch attempts to enqueue a batch of jobs and updates entity states on
// success. It returns true if the batch was enqueued and states were transitioned.
func (s *BatchInterventionSystem) processBatch(items *[]interface{}, entities *[]ecs.Entity) bool {
	for n, ent := range *entities {
		if !s.world.Alive(ent) {
			continue
		}
		state := s.monitorStateMapper.Get(ent)
		if state == nil || !state.IsInterventionNeeded() || state.IsInterventionPending() {
			continue
		}
		if err := s.queue.Enqueue((*items)[n].(jobs.Job)); err != nil {
			s.ready.Enqueue([]ecs.Entity{ent})
			continue
		}
		state.InterventionGeneration++
		state.SetInterventionNeeded(false)
		state.SetInterventionPending(true)
	}
	return true
}

// Finalize is a no-op for this system.
func (s *BatchInterventionSystem) Finalize(_ *ecs.World) {}
