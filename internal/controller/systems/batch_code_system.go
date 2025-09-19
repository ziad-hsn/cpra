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
	filter *ecs.Filter3[components.MonitorState, components.CodeConfig, components.JobStorage]
}

// NewBatchCodeSystem creates a new BatchCodeSystem.
func NewBatchCodeSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger) *BatchCodeSystem {
	return &BatchCodeSystem{
		world:     world,
		queue:     q,
		logger:    logger,
		batchSize: batchSize,
		filter:    ecs.NewFilter3[components.MonitorState, components.CodeConfig, components.JobStorage](world),
	}
}
func (s *BatchCodeSystem) Initialize(w *ecs.World) {

}

// Update finds and processes all monitors that need a code alert.
func (s *BatchCodeSystem) Update(w *ecs.World) {
	startTime := time.Now()
	query := s.filter.Query()

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

		// Process in batches
		if len(jobsToProcess) >= s.batchSize {
			s.processBatch(&jobsToProcess)
			processedCount += len(jobsToProcess)
			jobsToProcess = make([]jobInfo, 0, s.batchSize)
		}
	}

	// Process any remaining entities
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
		stateMapper := ecs.NewMap1[components.MonitorState](s.world)
		state := stateMapper.Get(info.Entity)
		if state == nil {
			continue
		}

		state.PendingCode = ""

		// Atomically update flags: remove CodeNeeded, add CodePending.
		flags := atomic.LoadUint32(&state.Flags)
		newFlags := (flags & ^uint32(components.StateCodeNeeded)) | uint32(components.StateCodePending)
		atomic.StoreUint32(&state.Flags, newFlags)

		s.logger.Info("CODE DISPATCHED: %s (%s)", state.Name, info.Color)
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeSystem) Finalize(w *ecs.World) {}
