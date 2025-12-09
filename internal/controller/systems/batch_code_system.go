package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// jobInfo is a helper struct to associate a job with its entity and color for batch processing.
type jobInfo struct {
	Job    jobs.Job
	Color  string
	Entity ecs.Entity
	// OldState captures the state before transitioning to Pending so we can log/revert safely.
	OldState components.MonitorState
}

// BatchCodeSystem processes entities that need a code alert dispatched.
// It determines the correct color based on the entity's state and enqueues the job.
type BatchCodeSystem struct {
	queue       queue.Queue
	logger      Logger
	stateLogger *StateLogger
	world       *ecs.World
	filter      *ecs.Filter2[components.MonitorState, components.CodeConfig]
	stateMapper *ecs.Map1[components.MonitorState]
	jobInfoPool *sync.Pool
	batchSize   int
}

// NewBatchCodeSystem creates a new BatchCodeSystem.
func NewBatchCodeSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger, stateLogger *StateLogger) *BatchCodeSystem {
	return &BatchCodeSystem{
		world:       world,
		queue:       q,
		logger:      logger,
		stateLogger: stateLogger,
		batchSize:   batchSize,
		filter: ecs.NewFilter2[components.MonitorState, components.CodeConfig](world).
			Without(ecs.C[components.Disabled]()),
		stateMapper: ecs.NewMap1[components.MonitorState](world),
		jobInfoPool: &sync.Pool{
			New: func() interface{} {
				s := make([]jobInfo, 0, batchSize)
				return &s
			},
		},
	}
}
func (s *BatchCodeSystem) Initialize(_ *ecs.World) {
	if s.filter != nil {
		s.filter.Register()
	}
}

// Update finds and processes all monitors that need a code alert.
func (s *BatchCodeSystem) Update(_ *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debug("Code queue saturated", "depth", stats.QueueDepth, "capacity", stats.Capacity)
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
	}

	earlyExit := false

	jobInfoPtr := s.jobInfoPool.Get().(*[]jobInfo)
	jobsToProcess := (*jobInfoPtr)[:0]
	processedCount := 0

	defer func() {
		s.jobInfoPool.Put(jobInfoPtr)
	}()

	for query.Next() {
		ent := query.Entity()
		state, codeConfig := query.Get()

		// Process only entities that need a code alert.
		if (state.Flags & components.StateCodeNeeded) == 0 {
			continue
		}

		// Skip if a code job is already in flight to prevent race conditions.
		// Per FSM pattern: only one job of a type should be in-flight per entity.
		// The current job will complete, then the next color (if any) will be processed.
		if (state.Flags & components.StateCodePending) != 0 {
			continue
		}

		color := state.PendingCode
		if color == "" {
			// This should not happen if StateCodeNeeded is set, but as a safeguard:
			state.Flags &^= components.StateCodeNeeded
			continue
		}

		// Honor dispatch flag and presence of color config before enqueuing
		cfg := codeConfig.Configs[color]
		if cfg == nil {
			s.logger.Warn("Entity missing code config; clearing pending code", "entity_id", ent.ID(), "color", color)
			state.Flags &^= components.StateCodeNeeded
			continue
		}
		if !cfg.Dispatch {
			s.logger.Info("Code dispatch disabled; clearing pending code", "entity_id", ent.ID(), "color", color)
			state.Flags &^= components.StateCodeNeeded
			continue
		}

		// Construct schema.CodeConfig from component to create job JIT.
		schemaCfg := schema.CodeConfig{
			Dispatch: cfg.Dispatch,
			Notify:   cfg.Notify,
			Config:   cfg.Config, // This is already the correct schema type (CodeNotification interface)
		}

		job, err := jobs.CreateCodeJob(state.Name, schemaCfg, ent, color)
		if err != nil {
			s.logger.Error("Failed to create code job", "error", err, "entity_id", ent.ID())
			state.Flags &^= components.StateCodeNeeded
			continue
		}
		if job == nil || isNilJob(job) {
			s.logger.Warn("Entity needs code alert, but job creation returned nil", "entity_id", ent.ID(), "color", color)
			// Clear the flag if no job is found to prevent spinning.
			state.Flags &^= components.StateCodeNeeded
			continue
		}

		jobsToProcess = append(jobsToProcess, jobInfo{Entity: ent, Job: job, Color: color})

		if len(jobsToProcess) >= tokens {
			s.processBatch(&jobsToProcess)
			processedCount += len(jobsToProcess)
			jobsToProcess = jobsToProcess[:0]
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
		s.logger.Debug("Code queue near capacity; skipping enqueue", "depth", stats.QueueDepth, "capacity", stats.Capacity)
		return
	}

	items := make([]interface{}, 0, len(*jobsInfo))
	submitted := make([]jobInfo, 0, len(*jobsInfo))
	for _, info := range *jobsInfo {
		if isNilJob(info.Job) {
			s.logger.Warn("Code job became nil before enqueue; skipping", "entity_id", info.Entity.ID())
			continue
		}
		if !s.world.Alive(info.Entity) {
			continue
		}
		state := s.stateMapper.Get(info.Entity)
		if state == nil {
			continue
		}

		// State might have already been updated if another system intervened; double-check guard.
		if state.Flags&components.StateCodeNeeded == 0 {
			continue
		}

		info.OldState = *state
		state.Flags &^= components.StateCodeNeeded
		state.Flags |= components.StateCodePending

		items = append(items, info.Job)
		submitted = append(submitted, info)
	}

	if len(items) == 0 {
		return
	}

	err := s.queue.EnqueueBatch(items)
	if err != nil {
		s.logger.Warn("Failed to enqueue code job batch, queue may be full", "error", err)
		// Revert state transitions since dispatch failed.
		for _, info := range submitted {
			if !s.world.Alive(info.Entity) {
				continue
			}
			state := s.stateMapper.Get(info.Entity)
			if state == nil {
				continue
			}
			state.Flags &^= components.StateCodePending
			state.Flags |= components.StateCodeNeeded
		}
		return
	}

	for _, info := range submitted {
		if !s.world.Alive(info.Entity) {
			continue
		}
		state := s.stateMapper.Get(info.Entity)
		if state == nil {
			continue
		}

		s.stateLogger.LogTransition(info.Entity, info.OldState, *state)
		s.logger.Info("Code dispatched", "monitor_name", state.Name, "color", info.Color)
	}
}

// Finalize is a no-op for this system.
func (s *BatchCodeSystem) Finalize(_ *ecs.World) {}

func isNilJob(job jobs.Job) bool { return job == nil || job.IsNil() }
