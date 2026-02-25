// Package systems provide ECS systems for processing monitor state transitions
// and job execution coordination.
//
// Systems in this package implement the ark System interface and are registered
// with the controller to process entities in batches. Each system handles a
// specific aspect of monitor lifecycle management.
//
// # System Types
//
// Schedule Systems:
//   - BatchPulseScheduleSystem: Determines when pulse checks are needed based on intervals
//
// Dispatch Systems:
//   - BatchPulseSystem: Enqueues pulse jobs for execution
//   - BatchInterventionSystem: Enqueues intervention jobs when thresholds exceeded
//   - BatchCodeSystem: Enqueues code alert jobs
//
// Result Systems:
//   - BatchPulseResultSystem: Processes pulse job results and updates monitor state
//   - BatchInterventionResultSystem: Processes intervention job results
//   - BatchCodeResultSystem: Processes code alert job results
//
// # Batch Processing
//
// All systems use batch processing to maximize throughput:
//   - Systems query entities in batches using ECS filters
//   - Jobs are enqueued in batches to reduce queue contention
//   - Entity state updates are batched for cache efficiency
//
// # Performance
//
// Systems use object pooling (sync.Pool) to reduce allocations:
//   - Job slices are pooled for batch enqueue operations
//   - Entity slices are pooled for batch state updates
package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/runtime/queue"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
)

type scheduledPulse struct {
	ent      ecs.Entity
	state    *components.MonitorState
	interval time.Duration
	oldState components.MonitorState
}

// BatchPulseSystem processes entities that need a pulse check.
//
// BatchPulseSystem identifies entities with the StatePulseNeeded flag set,
// enqueues the corresponding pulse job to the pulse queue, and transitions
// the entity state to StatePulsePending.
//
// The system processes entities in batches to maximize throughput and uses
// object pooling to reduce allocations. It respects queue capacity limits
// and can be configured with a maximum dispatch rate.
type BatchPulseSystem struct {
	queue              queue.Queue
	logger             Logger
	stateLogger        *StateLogger
	world              *ecs.World
	filter             *ecs.Filter4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard]
	monitorStateMapper *ecs.Map[components.MonitorState]
	jobPool            *sync.Pool
	batchSize          int
	maxDispatch        int
	shardSlots         int
	currentShard       int
}

// NewBatchPulseSystem creates a new BatchPulseSystem.
func NewBatchPulseSystem(world *ecs.World, q queue.Queue, batchSize int, logger Logger, stateLogger *StateLogger, shardSlots int) *BatchPulseSystem {
	if shardSlots <= 0 {
		shardSlots = components.DefaultShardSlots
	}
	return &BatchPulseSystem{
		world:       world,
		queue:       q,
		logger:      logger,
		stateLogger: stateLogger,
		batchSize:   batchSize,
		shardSlots:  shardSlots,
		filter: ecs.NewFilter4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world).
			Without(ecs.C[components.Disabled]()),
		monitorStateMapper: ecs.NewMap[components.MonitorState](world),
		jobPool: &sync.Pool{
			New: func() interface{} {
				s := make([]interface{}, 0, batchSize)
				return &s
			},
		},
	}
}

func (s *BatchPulseSystem) Initialize(_ *ecs.World) {
	if s.filter != nil {
		s.filter.Register()
	}
}

func (s *BatchPulseSystem) SetMaxDispatch(n int) {
	s.maxDispatch = n
}

// Update finds and processes all monitors that need a pulse check.
func (s *BatchPulseSystem) Update(_ *ecs.World) {
	startTime := time.Now()
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debugw("Pulse queue saturated", "depth", stats.QueueDepth, "capacity", stats.Capacity)
	}

	query := s.filter.Query()

	// Process a single shard per tick to avoid O(N) scans.
	shardToProcess := s.currentShard
	s.currentShard = (s.currentShard + 1) % s.shardSlots

	var tokens int
	if stats.Capacity <= 0 {
		// Sentinel capacity <= 0 signals an unbounded queue (Workings implementation).
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

	earlyExit := false

	jobsPtr := s.jobPool.Get().(*[]interface{})
	jobsToQueue := (*jobsPtr)[:0]
	scheduled := make([]scheduledPulse, 0, tokens)
	processedCount := 0

	defer func() {
		s.jobPool.Put(jobsPtr)
	}()

	now := time.Now()
	for query.Next() {
		ent := query.Entity()
		state, jobStorage, pulseCfg, shard := query.Get()

		if shard == nil || int(shard.ID)%s.shardSlots != shardToProcess {
			continue
		}

		// Skip if a pulse job is already pending.
		if state.Flags&components.StatePulsePending != 0 {
			continue
		}

		// Guard against missing jobs.
		if jobStorage == nil || jobStorage.PulseJob == nil || jobStorage.PulseJob.IsNil() {
			s.logger.Warnw("Entity has pulse work but no valid PulseJob", "entity_id", ent.ID())
			continue
		}

		interval := pulseCfg.Interval
		if interval <= 0 {
			interval = time.Second
		}

		// Determine if pulse is due: first check or next check time reached.
		due := state.Flags&components.StatePulseFirstCheck != 0
		if !due {
			if state.NextCheckTime.IsZero() || !state.NextCheckTime.After(now) {
				due = true
			}
		}
		if !due {
			continue
		}

		jobsToQueue = append(jobsToQueue, jobStorage.PulseJob)
		scheduled = append(scheduled, scheduledPulse{
			ent:      ent,
			state:    state,
			interval: interval,
			oldState: *state,
		})

		if len(jobsToQueue) >= tokens {
			s.processBatch(&jobsToQueue, &scheduled)
			processedCount += len(jobsToQueue)
			jobsToQueue = jobsToQueue[:0]
			scheduled = scheduled[:0]
			earlyExit = true
			break
		}
	}

	// Process any remaining entities
	if earlyExit {
		query.Close()
	}

	if len(jobsToQueue) > 0 {
		s.processBatch(&jobsToQueue, &scheduled)
		processedCount += len(jobsToQueue)
	}

	if processedCount > 0 {
		dur := time.Since(startTime)
		s.logger.Debugf("Performance: BatchPulseSystem processed %d entities in %v (%.1f/sec)",
			processedCount, dur, float64(processedCount)/dur.Seconds())
	}

}

// processBatch attempts to enqueue a batch of jobs and updates entity states on success.
func (s *BatchPulseSystem) processBatch(jobs *[]interface{}, scheduled *[]scheduledPulse) {
	stats := s.queue.Stats()
	if stats.Capacity > 0 && stats.QueueDepth >= int(float64(stats.Capacity)*0.9) {
		s.logger.Debugw("Pulse queue near capacity; skipping enqueue", "depth", stats.QueueDepth, "capacity", stats.Capacity)
		return
	}
	err := s.queue.EnqueueBatch(*jobs)
	if err != nil {
		s.logger.Warnw("Failed to enqueue pulse job batch, queue may be full", "error", err)
		// Do not transition state if enqueue fails, allowing retry on the next tick.
		return
	}

	// If enqueue is successful, transition the state for all entities in the batch.
	now := time.Now()
	for _, item := range *scheduled {
		ent := item.ent
		if !s.world.Alive(ent) {
			continue
		}
		state := item.state
		if state == nil {
			continue
		}

		// Transition directly to Pending and schedule the next check.
		state.Flags &^= components.StatePulseFirstCheck
		state.Flags &^= components.StatePulseNeeded
		state.Flags |= components.StatePulsePending
		state.PulseRunVersion = state.ConfigVersion
		state.LastPulseCheckTime = now
		state.LastEventTime = now
		state.NextCheckTime = now.Add(item.interval)
		s.stateLogger.LogTransition(ent, item.oldState, *state)
	}
}

// Finalize is a no-op for this system.
func (s *BatchPulseSystem) Finalize(_ *ecs.World) {}
