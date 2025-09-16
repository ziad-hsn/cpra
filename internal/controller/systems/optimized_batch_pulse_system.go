// Package systems provides ECS systems for the CPRA monitoring application
// Following Ark ECS best practices and research-based optimizations
package systems

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/workers/workerspool"
)

// OptimizedBatchPulseSystem handles monitor pulse scheduling using Ark batch operations
// Designed for 1M+ monitors with optimal performance
type OptimizedBatchPulseSystem struct {
	world      *ecs.World
	queue      *queue.CircularQueue
	workerPool *workerspool.WorkersPool
	pools      *MemoryPools
	
	// Cached filters (Ark performance tip: reuse filters)
	readyFilter *generic.Filter1[components.MonitorState]
	
	// Configuration
	batchSize     int
	maxBatchSize  int
	
	// Performance metrics
	lastScheduled uint64
	totalDropped  uint64
	
	// Synchronization
	mu sync.RWMutex
}

// MemoryPools manages object pools to reduce GC pressure
type MemoryPools struct {
	jobs     sync.Pool
	entities sync.Pool
}

// NewMemoryPools creates a new memory pool system
func NewMemoryPools() *MemoryPools {
	return &MemoryPools{
		jobs: sync.Pool{
			New: func() interface{} {
				return make([]queue.Job, 0, 10000)
			},
		},
		entities: sync.Pool{
			New: func() interface{} {
				return make([]ecs.Entity, 0, 10000)
			},
		},
	}
}

// GetJobs retrieves a job slice from the pool
func (p *MemoryPools) GetJobs() []queue.Job {
	return p.jobs.Get().([]queue.Job)[:0]
}

// PutJobs returns a job slice to the pool
func (p *MemoryPools) PutJobs(jobs []queue.Job) {
	if cap(jobs) >= 1000 {
		p.jobs.Put(jobs)
	}
}

// GetEntities retrieves an entity slice from the pool
func (p *MemoryPools) GetEntities() []ecs.Entity {
	return p.entities.Get().([]ecs.Entity)[:0]
}

// PutEntities returns an entity slice to the pool
func (p *MemoryPools) PutEntities(entities []ecs.Entity) {
	if cap(entities) >= 1000 {
		p.entities.Put(entities)
	}
}

// NewOptimizedBatchPulseSystem creates a new optimized pulse system
func NewOptimizedBatchPulseSystem(world *ecs.World, queue *queue.CircularQueue, workerPool *workerspool.WorkersPool) *OptimizedBatchPulseSystem {
	return &OptimizedBatchPulseSystem{
		world:        world,
		queue:        queue,
		workerPool:   workerPool,
		pools:        NewMemoryPools(),
		batchSize:    1000,  // Start with 1K batch size
		maxBatchSize: 10000, // Maximum 10K for optimal Ark performance
	}
}

// Update processes ready monitors in large batches
// Following the critical system update order from CLAUDE.md
func (s *OptimizedBatchPulseSystem) Update() {
	now := time.Now()
	
	// Create or reuse cached filter (Ark performance tip)
	if s.readyFilter == nil {
		s.mu.Lock()
		if s.readyFilter == nil {
			// Register filter for caching (Ark performance optimization)
			s.readyFilter = generic.NewFilter1[components.MonitorState](s.world).Register()
		}
		s.mu.Unlock()
	}
	
	// Get reusable slices from pool
	entities := s.pools.GetEntities()
	jobs := s.pools.GetJobs()
	defer func() {
		s.pools.PutEntities(entities)
		s.pools.PutJobs(jobs)
	}()
	
	// Use Ark's optimized query iteration
	query := s.readyFilter.Query()
	defer query.Close() // Critical: always close queries to release world lock
	
	scheduled := 0
	for query.Next() {
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if monitor is ready for pulse check
		if s.isReadyForPulse(monitor, now) {
			entities = append(entities, entity)
			
			job := queue.Job{
				EntityID: entity,
				URL:      monitor.URL,
				Method:   monitor.Method,
				Timeout:  int64(monitor.Timeout.Milliseconds()),
				JobType:  queue.JobTypePulse,
			}
			jobs = append(jobs, job)
			scheduled++
			
			// Process in adaptive batches for optimal performance
			if len(jobs) >= s.getCurrentBatchSize() {
				s.processBatch(entities, jobs)
				entities = entities[:0]
				jobs = jobs[:0]
			}
		}
	}
	
	// Process remaining items
	if len(jobs) > 0 {
		s.processBatch(entities, jobs)
	}
	
	atomic.StoreUint64(&s.lastScheduled, uint64(scheduled))
	
	// Adaptive batch size adjustment based on queue load
	s.adjustBatchSize()
}

// isReadyForPulse checks if a monitor is ready for pulse check
func (s *OptimizedBatchPulseSystem) isReadyForPulse(monitor *components.MonitorState, now time.Time) bool {
	// Check if monitor is in ready state and interval has passed
	return monitor.IsReady() && now.After(monitor.NextCheck)
}

// processBatch handles a batch of entities and jobs
// Critical: Only transitions entities to Pending state if jobs are successfully enqueued
func (s *OptimizedBatchPulseSystem) processBatch(entities []ecs.Entity, jobs []queue.Job) {
	if len(entities) == 0 || len(jobs) == 0 {
		return
	}
	
	// Try to enqueue jobs to worker pool
	enqueued := s.queue.EnqueueBatch(jobs)
	
	if enqueued < len(jobs) {
		// Queue full - track dropped jobs but don't block
		dropped := uint64(len(jobs) - enqueued)
		atomic.AddUint64(&s.totalDropped, dropped)
		
		// Log warning for monitoring
		fmt.Printf("Warning: Pulse queue full, dropped %d jobs (total dropped: %d)\n", 
			dropped, atomic.LoadUint64(&s.totalDropped))
	}
	
	// Critical fix: Only update entities whose jobs were successfully enqueued
	// This prevents entities from getting stuck in PulsePending state
	if enqueued > 0 {
		s.updateEntityStates(entities[:enqueued])
	}
}

// updateEntityStates updates entity states using Ark's efficient batch operations
func (s *OptimizedBatchPulseSystem) updateEntityStates(entities []ecs.Entity) {
	mapper := generic.NewMap1[components.MonitorState](s.world)
	
	// Use Ark's MapBatchFn for maximum performance (11x faster than individual operations)
	mapper.MapBatchFn(entities, func(entity ecs.Entity, monitor *components.MonitorState) {
		// Transition to processing state
		monitor.SetProcessing()
		monitor.LastCheck = time.Now()
		
		// Update metrics
		atomic.AddUint64(&monitor.TotalChecks, 1)
	})
}

// getCurrentBatchSize returns the current adaptive batch size
func (s *OptimizedBatchPulseSystem) getCurrentBatchSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.batchSize
}

// adjustBatchSize dynamically adjusts batch size based on queue load
func (s *OptimizedBatchPulseSystem) adjustBatchSize() {
	queueStats := s.queue.Stats()
	
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// Increase batch size when queue is underutilized
	if queueStats.Usage < 0.3 && s.batchSize < s.maxBatchSize {
		s.batchSize = min(s.batchSize*2, s.maxBatchSize)
	}
	
	// Decrease batch size when queue is overloaded
	if queueStats.Usage > 0.8 && s.batchSize > 100 {
		s.batchSize = max(s.batchSize/2, 100)
	}
}

// Stats returns system performance statistics
func (s *OptimizedBatchPulseSystem) Stats() PulseSystemStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	return PulseSystemStats{
		LastScheduled: atomic.LoadUint64(&s.lastScheduled),
		TotalDropped:  atomic.LoadUint64(&s.totalDropped),
		BatchSize:     s.batchSize,
		QueueStats:    s.queue.Stats(),
	}
}

// PulseSystemStats provides system performance metrics
type PulseSystemStats struct {
	LastScheduled uint64           `json:"last_scheduled"`
	TotalDropped  uint64           `json:"total_dropped"`
	BatchSize     int              `json:"batch_size"`
	QueueStats    queue.QueueStats `json:"queue_stats"`
}

// Cleanup performs system cleanup (call during shutdown)
func (s *OptimizedBatchPulseSystem) Cleanup() {
	if s.readyFilter != nil {
		s.readyFilter.Unregister()
	}
}

// Helper functions

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

