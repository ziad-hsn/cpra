package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/queue"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// jobInfo is a helper struct to associate a job with its entity and color for batch processing.
type jobInfo struct {
	Entity ecs.Entity
	Job    interface{}
	Color  string
}

// BatchCodeSystem processes entities that need a code alert dispatched.
// It determines the correct color based on the entity's state and enqueues the job.
type BatchCodeSystem struct {
	world     *ecs.World
	queue     queue.Queue // Using a generic queue interface
	logger    Logger
	batchSize int

	// Filter for entities that require a code alert.
	filter      *ecs.Filter3[components.MonitorState, components.CodeConfig, components.JobStorage]
	stateMapper *ecs.Map1[components.MonitorState]
}

// NewBatchCodeSystem creates a new BatchCodeSystem.
func NewBatchCodeSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger) *BatchCodeSystem {
	return &BatchCodeSystem{
		world:       world,
		queue:       q,
		logger:      logger,
		batchSize:   batchSize,
		filter:      ecs.NewFilter3[components.MonitorState, components.CodeConfig, components.JobStorage](world),
		stateMapper: ecs.NewMap1[components.MonitorState](world),
	}
}
func (s *BatchCodeSystem) Initialize(w *ecs.World) {

}

// Update finds and processes all monitors that need a code alert.
func (s *BatchCodeSystem) Update(w *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Code queue saturated (%d/%d); deferring dispatch", stats.QueueDepth, stats.Capacity)
	}

	query := s.filter.Query()

	free := stats.Capacity - stats.QueueDepth
	if free <= 0 {
		return
	}
	tokens := int(float64(free) * 0.8)
	if tokens <= 0 {
		tokens = free
	}
	if tokens <= 0 {
		tokens = 1
	}

	earlyExit := false

	jobsToProcess := make([]jobInfo, 0, s.batchSize)
	processedCount := 0

	for query.Next() {
		ent := query.Entity()
		state, _, jobStorage := query.Get()

		// Process only entities that need a code alert.
		if (atomic.LoadUint32(&state.Flags) & components.StateCodeNeeded) == 0 {
			continue
		}

		color := state.PendingCode
		if color == "" {
			// This should not happen if StateCodeNeeded is set, but as a safeguard:
			atomic.AndUint32(&state.Flags, ^uint32(components.StateCodeNeeded))
			continue
		}

		job, ok := jobStorage.CodeJobs[color]
		if !ok || job == nil {
			s.logger.Warn("Entity[%d] needs '%s' code alert, but no job is configured.", ent.ID(), color)
			// Clear the flag if no job is found to prevent spinning.
			atomic.AndUint32(&state.Flags, ^uint32(components.StateCodeNeeded))
			continue
		}

		jobsToProcess = append(jobsToProcess, jobInfo{Entity: ent, Job: job, Color: color})

		if len(jobsToProcess) >= tokens {
			s.processBatch(&jobsToProcess)
			processedCount += len(jobsToProcess)
			jobsToProcess = make([]jobInfo, 0, s.batchSize)
			earlyExit = true
			break
		}
	}

	// Process any remaining entities
	if earlyExit {
		query.Close()
	}

	if len(jobsToProcess) > 0 {
		s.processBatch(&jobsToProcess)
		processedCount += len(jobsToProcess)
	}

	if processedCount > 0 {
		s.logger.LogSystemPerformance("BatchCodeSystem", time.Since(startTime), processedCount)
	}

}

// processBatch attempts to enqueue a batch of jobs and updates entity states on success.
func (s *BatchCodeSystem) processBatch(jobsInfo *[]jobInfo) {
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Code queue near capacity (%d/%d); skipping enqueue", stats.QueueDepth, stats.Capacity)
		return
	}
	jobs := make([]interface{}, len(*jobsInfo))
	for i, info := range *jobsInfo {
		jobs[i] = info.Job
	}

	err := s.queue.EnqueueBatch(jobs)
	if err != nil {
		s.logger.Warn("Failed to enqueue code job batch, queue may be full: %v", err)
		return
	}

	for _, info := range *jobsInfo {
		if !s.world.Alive(info.Entity) {
			continue
		}
		state := s.stateMapper.Get(info.Entity)
		if state == nil {
			continue
		}

		for {
			flags := atomic.LoadUint32(&state.Flags)
			if flags&components.StateCodeNeeded == 0 {
				break
			}

			updated := (flags & ^uint32(components.StateCodeNeeded)) | uint32(components.StateCodePending)
			if atomic.CompareAndSwapUint32(&state.Flags, flags, updated) {
				state.PendingCode = ""
				s.logger.Info("CODE DISPATCHED: %s (%s)", state.Name, info.Color)
				break
			}
		}
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeSystem) Finalize(w *ecs.World) {}
