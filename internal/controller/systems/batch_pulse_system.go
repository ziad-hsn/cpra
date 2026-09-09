package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/queue"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BatchPulseSystem processes entities that need a pulse check.
//
// It consumes entities from the PulseScheduler ready queue (populated by the
// schedule system) instead of scanning every entity each tick, so the per-tick
// cost is O(M) where M is the number of dispatched monitors.
type BatchPulseSystem struct {
	queue              queue.Queue
	logger             Logger
	stateLogger        *StateLogger
	world              *ecs.World
	sched              *PulseScheduler
	monitorStateMapper *ecs.Map1[components.MonitorState]
	jobStorageMapper   *ecs.Map1[components.JobStorage]
	batchSize          int
	maxDispatch        int
}

// NewBatchPulseSystem creates a new BatchPulseSystem.
func NewBatchPulseSystem(world *ecs.World, q queue.Queue, sched *PulseScheduler, batchSize int, logger Logger, stateLogger *StateLogger) *BatchPulseSystem {
	return &BatchPulseSystem{
		world:              world,
		queue:              q,
		logger:             logger,
		stateLogger:        stateLogger,
		sched:              sched,
		batchSize:          batchSize,
		monitorStateMapper: ecs.NewMap1[components.MonitorState](world),
		jobStorageMapper:   ecs.NewMap1[components.JobStorage](world),
	}
}

func (s *BatchPulseSystem) Initialize(_ *ecs.World) {
}

func (s *BatchPulseSystem) SetMaxDispatch(n int) {
	s.maxDispatch = n
}

// Update consumes up to tokens entities from the ready queue and enqueues
// their pulse jobs.
func (s *BatchPulseSystem) Update(_ *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Pulse queue saturated", "depth", stats.QueueDepth, "capacity", stats.Capacity)
	}

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
	}
	if s.maxDispatch > 0 && tokens > s.maxDispatch {
		tokens = s.maxDispatch
	}

	batch := s.sched.ConsumeReady(tokens)
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
		if state == nil || !state.IsPulseNeeded() || state.IsPulsePending() {
			continue
		}
		jobStorage := s.jobStorageMapper.Get(ent)
		if jobStorage == nil || jobStorage.PulseJob == nil || jobStorage.PulseJob.IsNil() {
			s.logger.Warn("Entity has PulseNeeded state but no valid PulseJob", "entity_id", ent.ID())
			continue
		}

		// Copy the job so each enqueued instance is independent: the same
		// stored pointer is re-enqueued every tick and may be executed
		// concurrently by the worker pool.
		jobsToQueue = append(jobsToQueue, jobs.NewDispatch(jobStorage.PulseJob, ent, "pulse", "", state.PulseGeneration+1, 0))
		entitiesToUpdate = append(entitiesToUpdate, ent)
	}

	if len(jobsToQueue) == 0 {
		return
	}

	if !s.processBatch(&jobsToQueue, &entitiesToUpdate) {
		// Enqueue failed; re-queue the entities so they are retried next tick.
		s.sched.Requeue(entitiesToUpdate)
		return
	}

	s.logger.LogSystemPerformance("BatchPulseSystem", time.Since(startTime), len(entitiesToUpdate))
}

// processBatch attempts to enqueue a batch of jobs and updates entity states on
// success. It returns true if the batch was enqueued and states were transitioned.
func (s *BatchPulseSystem) processBatch(items *[]interface{}, entities *[]ecs.Entity) bool {
	for n, ent := range *entities {
		if !s.world.Alive(ent) {
			continue
		}
		state := s.monitorStateMapper.Get(ent)
		if state == nil || !state.IsPulseNeeded() || state.IsPulsePending() {
			continue
		}
		if err := s.queue.Enqueue((*items)[n].(jobs.Job)); err != nil {
			s.sched.Requeue([]ecs.Entity{ent})
			continue
		}
		state.PulseGeneration++
		state.SetPulseNeeded(false)
		state.SetPulsePending(true)
	}
	return true
}

// Finalize is a no-op for this system.
func (s *BatchPulseSystem) Finalize(_ *ecs.World) {}
