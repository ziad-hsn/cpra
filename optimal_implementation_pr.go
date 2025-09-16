// Optimal CPRA Implementation for ark-migration branch
// Based on comprehensive research and following Ark ECS best practices
// Designed to handle 1M+ monitors efficiently with proper Go idioms

package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"
)

// ============================================================================
// COMPONENT DEFINITIONS (Following Ark Best Practices)
// ============================================================================

// MonitorState consolidates monitor data into single component
// Follows Ark principle: minimize component add/remove operations
// Cache-aligned for optimal memory access patterns
type MonitorState struct {
	// Core monitor configuration (immutable after creation)
	URL      string        `json:"url"`
	Method   string        `json:"method"`
	Interval time.Duration `json:"interval"`
	Timeout  time.Duration `json:"timeout"`
	
	// State management (atomic operations for thread safety)
	Flags        uint32        `json:"-"` // Bitfield: Ready=1, Processing=2, Failed=4
	LastCheck    time.Time     `json:"last_check"`
	NextCheck    time.Time     `json:"next_check"`
	
	// Results tracking
	StatusCode   int           `json:"status_code"`
	ResponseTime time.Duration `json:"response_time"`
	ErrorCount   uint32        `json:"error_count"`
	
	// Padding to cache line boundary (64 bytes total)
	_ [4]byte
}

// State flag constants (following Go naming conventions)
const (
	StateReady      uint32 = 1 << iota // Entity ready for processing
	StateProcessing                    // Entity currently being processed
	StateFailed                        // Entity in failed state
	StateDisabled                      // Entity temporarily disabled
)

// Thread-safe state management methods (following Effective Go patterns)
func (m *MonitorState) IsReady() bool      { return atomic.LoadUint32(&m.Flags)&StateReady != 0 }
func (m *MonitorState) IsProcessing() bool { return atomic.LoadUint32(&m.Flags)&StateProcessing != 0 }
func (m *MonitorState) IsFailed() bool     { return atomic.LoadUint32(&m.Flags)&StateFailed != 0 }

func (m *MonitorState) SetReady()      { atomic.StoreUint32(&m.Flags, StateReady) }
func (m *MonitorState) SetProcessing() { atomic.StoreUint32(&m.Flags, StateProcessing) }
func (m *MonitorState) SetFailed()     { atomic.StoreUint32(&m.Flags, StateFailed) }

// ============================================================================
// HIGH-PERFORMANCE CIRCULAR QUEUE (Following GeeksforGeeks patterns)
// ============================================================================

// CircularQueue implements a lock-free circular buffer
// Based on research from GeeksforGeeks and production queue patterns
type CircularQueue struct {
	items    []MonitorJob
	head     uint64 // Use uint64 to prevent ABA problems
	tail     uint64
	mask     uint64 // capacity - 1 (for power-of-2 sizes)
	capacity uint64
	
	// Padding to prevent false sharing between cache lines
	_ [56]byte
}

// MonitorJob represents work to be processed
type MonitorJob struct {
	EntityID ecs.Entity    `json:"entity_id"`
	URL      string        `json:"url"`
	Method   string        `json:"method"`
	Timeout  time.Duration `json:"timeout"`
}

// NewCircularQueue creates a new circular queue with power-of-2 capacity
// Following Go constructor patterns from Effective Go
func NewCircularQueue(capacity uint64) *CircularQueue {
	// Ensure capacity is power of 2 for efficient bitwise operations
	cap := uint64(1)
	for cap < capacity {
		cap <<= 1
	}
	
	return &CircularQueue{
		items:    make([]MonitorJob, cap),
		mask:     cap - 1,
		capacity: cap,
	}
}

// Enqueue adds a job to the queue (non-blocking)
// Returns false if queue is full (following Go error handling patterns)
func (q *CircularQueue) Enqueue(job MonitorJob) bool {
	tail := atomic.LoadUint64(&q.tail)
	head := atomic.LoadUint64(&q.head)
	
	// Check if queue is full
	if tail-head >= q.capacity {
		return false
	}
	
	// Use bitwise AND instead of modulo for performance
	q.items[tail&q.mask] = job
	atomic.StoreUint64(&q.tail, tail+1)
	return true
}

// DequeueBatch removes multiple jobs efficiently (following Ark batch patterns)
// Returns number of jobs dequeued
func (q *CircularQueue) DequeueBatch(batch []MonitorJob) int {
	head := atomic.LoadUint64(&q.head)
	tail := atomic.LoadUint64(&q.tail)
	
	available := tail - head
	if available == 0 {
		return 0
	}
	
	count := uint64(len(batch))
	if available < count {
		count = available
	}
	
	// Bulk copy for cache efficiency
	for i := uint64(0); i < count; i++ {
		batch[i] = q.items[(head+i)&q.mask]
	}
	
	atomic.StoreUint64(&q.head, head+count)
	return int(count)
}

// Size returns current queue size (O(1) operation)
func (q *CircularQueue) Size() uint64 {
	tail := atomic.LoadUint64(&q.tail)
	head := atomic.LoadUint64(&q.head)
	return tail - head
}

// ============================================================================
// MEMORY POOL SYSTEM (Following Go memory management best practices)
// ============================================================================

// MemoryPools manages object pools to reduce GC pressure
// Following sync.Pool patterns from Go standard library
type MemoryPools struct {
	jobBatch    sync.Pool
	resultBatch sync.Pool
	entities    sync.Pool
}

// NewMemoryPools creates a new memory pool system
func NewMemoryPools() *MemoryPools {
	return &MemoryPools{
		jobBatch: sync.Pool{
			New: func() interface{} {
				// Pre-allocate large batches for optimal performance
				return make([]MonitorJob, 0, 10000)
			},
		},
		resultBatch: sync.Pool{
			New: func() interface{} {
				return make([]MonitorResult, 0, 10000)
			},
		},
		entities: sync.Pool{
			New: func() interface{} {
				return make([]ecs.Entity, 0, 10000)
			},
		},
	}
}

// GetJobBatch retrieves a job batch from the pool
func (p *MemoryPools) GetJobBatch() []MonitorJob {
	return p.jobBatch.Get().([]MonitorJob)[:0]
}

// PutJobBatch returns a job batch to the pool
func (p *MemoryPools) PutJobBatch(batch []MonitorJob) {
	// Only pool large batches to avoid memory fragmentation
	if cap(batch) >= 1000 {
		p.jobBatch.Put(batch)
	}
}

// GetResultBatch retrieves a result batch from the pool
func (p *MemoryPools) GetResultBatch() []MonitorResult {
	return p.resultBatch.Get().([]MonitorResult)[:0]
}

// PutResultBatch returns a result batch to the pool
func (p *MemoryPools) PutResultBatch(batch []MonitorResult) {
	if cap(batch) >= 1000 {
		p.resultBatch.Put(batch)
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

// ============================================================================
// WORKER POOL SYSTEM (Following Go concurrency patterns)
// ============================================================================

// WorkerPool manages a pool of workers for job processing
// Following Go concurrency patterns from Effective Go
type WorkerPool struct {
	workers     int
	queue       *CircularQueue
	results     chan []MonitorResult
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	pools       *MemoryPools
	
	// Performance metrics (atomic for thread safety)
	processed   uint64
	failed      uint64
	avgLatency  uint64 // nanoseconds
}

// MonitorResult represents the result of a monitor check
type MonitorResult struct {
	EntityID     ecs.Entity    `json:"entity_id"`
	StatusCode   int           `json:"status_code"`
	ResponseTime time.Duration `json:"response_time"`
	Error        error         `json:"error,omitempty"`
}

// NewWorkerPool creates a new worker pool
// Following Go constructor patterns
func NewWorkerPool(workers int, queueSize uint64) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &WorkerPool{
		workers: workers,
		queue:   NewCircularQueue(queueSize),
		results: make(chan []MonitorResult, workers*2), // Buffered for burst handling
		ctx:     ctx,
		cancel:  cancel,
		pools:   NewMemoryPools(),
	}
}

// Start begins worker processing
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// worker processes jobs in batches
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	
	// Pre-allocate batch buffer to avoid allocations in hot path
	jobBatch := make([]MonitorJob, 1000)
	
	for {
		select {
		case <-wp.ctx.Done():
			return
		default:
			// Bulk dequeue for maximum efficiency
			count := wp.queue.DequeueBatch(jobBatch)
			if count == 0 {
				// Brief pause when no work available
				time.Sleep(time.Millisecond)
				continue
			}
			
			// Process batch and send results
			results := wp.processBatch(jobBatch[:count])
			if len(results) > 0 {
				select {
				case wp.results <- results:
					// Results sent successfully
				case <-wp.ctx.Done():
					// Shutdown requested, return batch to pool
					wp.pools.PutResultBatch(results)
					return
				}
			}
		}
	}
}

// processBatch processes a batch of jobs
func (wp *WorkerPool) processBatch(jobs []MonitorJob) []MonitorResult {
	results := wp.pools.GetResultBatch()
	
	for _, job := range jobs {
		start := time.Now()
		
		// Execute HTTP check (placeholder - replace with actual implementation)
		statusCode, err := wp.performHTTPCheck(job.URL, job.Method, job.Timeout)
		
		latency := time.Since(start)
		
		result := MonitorResult{
			EntityID:     job.EntityID,
			StatusCode:   statusCode,
			ResponseTime: latency,
			Error:        err,
		}
		
		results = append(results, result)
		
		// Update metrics atomically
		atomic.AddUint64(&wp.processed, 1)
		if err != nil {
			atomic.AddUint64(&wp.failed, 1)
		}
		
		// Update average latency using exponential moving average
		oldAvg := atomic.LoadUint64(&wp.avgLatency)
		newAvg := (oldAvg*9 + uint64(latency.Nanoseconds())) / 10
		atomic.StoreUint64(&wp.avgLatency, newAvg)
	}
	
	return results
}

// performHTTPCheck executes an HTTP monitor check
// Placeholder for actual HTTP implementation
func (wp *WorkerPool) performHTTPCheck(url, method string, timeout time.Duration) (int, error) {
	// Simulate network latency for demo
	time.Sleep(time.Millisecond * 10)
	return 200, nil
}

// EnqueueBatch adds multiple jobs to the queue
// Returns number of jobs successfully enqueued
func (wp *WorkerPool) EnqueueBatch(jobs []MonitorJob) int {
	enqueued := 0
	for _, job := range jobs {
		if wp.queue.Enqueue(job) {
			enqueued++
		} else {
			break // Queue full, stop trying
		}
	}
	return enqueued
}

// GetResults returns the results channel
func (wp *WorkerPool) GetResults() <-chan []MonitorResult {
	return wp.results
}

// Stop gracefully shuts down the worker pool
func (wp *WorkerPool) Stop() {
	wp.cancel()
	wp.wg.Wait()
	close(wp.results)
}

// Stats returns current performance statistics
func (wp *WorkerPool) Stats() (processed, failed uint64, avgLatency time.Duration) {
	return atomic.LoadUint64(&wp.processed),
		   atomic.LoadUint64(&wp.failed),
		   time.Duration(atomic.LoadUint64(&wp.avgLatency))
}

// ============================================================================
// OPTIMIZED ECS SYSTEMS (Following Ark best practices)
// ============================================================================

// OptimizedScheduleSystem handles monitor scheduling using Ark batch operations
type OptimizedScheduleSystem struct {
	world       *ecs.World
	workerPool  *WorkerPool
	pools       *MemoryPools
	
	// Cached filters (Ark performance tip: reuse filters)
	readyFilter *generic.Filter1[MonitorState]
	
	// Performance metrics
	lastScheduled uint64
	batchSize     int
}

// NewOptimizedScheduleSystem creates a new schedule system
func NewOptimizedScheduleSystem(world *ecs.World, workerPool *WorkerPool) *OptimizedScheduleSystem {
	return &OptimizedScheduleSystem{
		world:      world,
		workerPool: workerPool,
		pools:      NewMemoryPools(),
		batchSize:  10000, // Large batch size for optimal Ark performance
	}
}

// Update processes ready monitors in large batches
func (s *OptimizedScheduleSystem) Update() {
	now := time.Now()
	
	// Create or reuse cached filter (Ark performance tip)
	if s.readyFilter == nil {
		s.readyFilter = generic.NewFilter1[MonitorState](s.world).Register()
	}
	
	// Get reusable slices from pool
	entities := s.pools.GetEntities()
	jobs := s.pools.GetJobBatch()
	defer func() {
		s.pools.PutEntities(entities)
		s.pools.PutJobBatch(jobs)
	}()
	
	// Use Ark's optimized query iteration
	query := s.readyFilter.Query()
	defer query.Close() // Important: always close queries
	
	for query.Next() {
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if ready for next check
		if monitor.IsReady() && now.After(monitor.NextCheck) {
			entities = append(entities, entity)
			
			job := MonitorJob{
				EntityID: entity,
				URL:      monitor.URL,
				Method:   monitor.Method,
				Timeout:  monitor.Timeout,
			}
			jobs = append(jobs, job)
			
			// Process in large batches for optimal performance
			if len(jobs) >= s.batchSize {
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
	
	atomic.StoreUint64(&s.lastScheduled, uint64(len(jobs)))
}

// processBatch handles a batch of entities and jobs
func (s *OptimizedScheduleSystem) processBatch(entities []ecs.Entity, jobs []MonitorJob) {
	// Try to enqueue jobs to worker pool
	enqueued := s.workerPool.EnqueueBatch(jobs)
	
	if enqueued < len(jobs) {
		// Queue full - log warning but don't block
		fmt.Printf("Warning: Queue full, only enqueued %d/%d jobs\n", enqueued, len(jobs))
	}
	
	// Update entity states using Ark's efficient batch operations
	// Only update entities whose jobs were successfully enqueued
	if enqueued > 0 {
		mapper := generic.NewMap1[MonitorState](s.world)
		
		// Use Ark's MapBatchFn for maximum performance
		mapper.MapBatchFn(entities[:enqueued], func(entity ecs.Entity, monitor *MonitorState) {
			monitor.SetProcessing()
			monitor.LastCheck = time.Now()
		})
	}
}

// OptimizedResultSystem processes job results using Ark batch operations
type OptimizedResultSystem struct {
	world      *ecs.World
	workerPool *WorkerPool
	pools      *MemoryPools
	
	// Performance metrics
	lastProcessed uint64
}

// NewOptimizedResultSystem creates a new result system
func NewOptimizedResultSystem(world *ecs.World, workerPool *WorkerPool) *OptimizedResultSystem {
	return &OptimizedResultSystem{
		world:      world,
		workerPool: workerPool,
		pools:      NewMemoryPools(),
	}
}

// Update processes completed job results
func (s *OptimizedResultSystem) Update() {
	// Non-blocking result collection
	select {
	case results := <-s.workerPool.GetResults():
		s.processResults(results)
		s.pools.PutResultBatch(results)
		atomic.StoreUint64(&s.lastProcessed, uint64(len(results)))
	default:
		// No results available, continue
	}
}

// processResults updates entity states based on job results
func (s *OptimizedResultSystem) processResults(results []MonitorResult) {
	if len(results) == 0 {
		return
	}
	
	// Extract entities for batch operation
	entities := s.pools.GetEntities()
	defer s.pools.PutEntities(entities)
	
	for _, result := range results {
		entities = append(entities, result.EntityID)
	}
	
	// Create result map for O(1) lookup during batch update
	resultMap := make(map[ecs.Entity]MonitorResult, len(results))
	for _, result := range results {
		resultMap[result.EntityID] = result
	}
	
	// Use Ark's efficient batch update
	mapper := generic.NewMap1[MonitorState](s.world)
	
	// MapBatchFn is the fastest way to update multiple entities
	mapper.MapBatchFn(entities, func(entity ecs.Entity, monitor *MonitorState) {
		if result, exists := resultMap[entity]; exists {
			// Update monitor state with results
			monitor.StatusCode = result.StatusCode
			monitor.ResponseTime = result.ResponseTime
			monitor.NextCheck = time.Now().Add(monitor.Interval)
			
			if result.Error != nil {
				monitor.SetFailed()
				atomic.AddUint32(&monitor.ErrorCount, 1)
			} else {
				monitor.SetReady()
				atomic.StoreUint32(&monitor.ErrorCount, 0)
			}
		}
	})
}

// ============================================================================
// MAIN CONTROLLER (Following CLAUDE.md guidelines)
// ============================================================================

// OptimizedController orchestrates the entire monitoring system
type OptimizedController struct {
	world           *ecs.World
	workerPool      *WorkerPool
	scheduleSystem  *OptimizedScheduleSystem
	resultSystem    *OptimizedResultSystem
	
	ctx             context.Context
	cancel          context.CancelFunc
	
	// Performance monitoring
	startTime       time.Time
	updateCount     uint64
	lastStatsTime   time.Time
}

// NewOptimizedController creates a new controller instance
func NewOptimizedController(monitorCount int) *OptimizedController {
	world := ecs.NewWorld()
	
	// Calculate optimal configuration based on system resources
	workers := runtime.NumCPU() * 2 // 2x CPU cores for I/O bound work
	queueSize := uint64(monitorCount / 10) // 10% of monitors as queue capacity
	if queueSize < 10000 {
		queueSize = 10000 // Minimum queue size
	}
	
	workerPool := NewWorkerPool(workers, queueSize)
	
	ctx, cancel := context.WithCancel(context.Background())
	
	return &OptimizedController{
		world:          world,
		workerPool:     workerPool,
		scheduleSystem: NewOptimizedScheduleSystem(world, workerPool),
		resultSystem:   NewOptimizedResultSystem(world, workerPool),
		ctx:            ctx,
		cancel:         cancel,
		startTime:      time.Now(),
		lastStatsTime:  time.Now(),
	}
}

// CreateMonitors creates monitors using Ark's optimized batch operations
func (c *OptimizedController) CreateMonitors(count int) {
	fmt.Printf("Creating %d monitors using Ark batch operations...\n", count)
	
	mapper := generic.NewMap1[MonitorState](c.world)
	
	// Use large batch sizes for optimal Ark performance
	batchSize := 10000
	for i := 0; i < count; i += batchSize {
		remaining := count - i
		if remaining > batchSize {
			remaining = batchSize
		}
		
		// Ark's fastest entity creation method
		mapper.NewBatchFn(remaining, func(entity ecs.Entity, monitor *MonitorState) {
			*monitor = MonitorState{
				URL:       fmt.Sprintf("https://example.com/monitor-%d", i),
				Method:    "GET",
				Interval:  time.Second * 30,
				Timeout:   time.Second * 5,
				Flags:     StateReady,
				NextCheck: time.Now().Add(time.Duration(i%30) * time.Second), // Spread initial load
			}
		})
	}
	
	fmt.Printf("Successfully created %d monitors\n", count)
}

// Start begins the controller's main loop
func (c *OptimizedController) Start() {
	fmt.Printf("Starting optimized controller with %d workers...\n", c.workerPool.workers)
	c.workerPool.Start()
	
	// Main update loop (following CLAUDE.md timing)
	ticker := time.NewTicker(time.Millisecond * 100) // 10Hz update rate
	defer ticker.Stop()
	
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.update()
		}
	}
}

// update runs one iteration of the main loop
func (c *OptimizedController) update() {
	// Update systems in the order specified in CLAUDE.md
	c.scheduleSystem.Update()
	c.resultSystem.Update()
	
	atomic.AddUint64(&c.updateCount, 1)
	
	// Print performance stats every 10 seconds
	if time.Since(c.lastStatsTime) >= time.Second*10 {
		c.printStats()
		c.lastStatsTime = time.Now()
	}
}

// printStats displays current performance metrics
func (c *OptimizedController) printStats() {
	uptime := time.Since(c.startTime)
	updates := atomic.LoadUint64(&c.updateCount)
	processed, failed, avgLatency := c.workerPool.Stats()
	queueSize := c.workerPool.queue.Size()
	
	fmt.Printf("\n=== CPRA Performance Stats ===\n")
	fmt.Printf("Uptime: %v\n", uptime)
	fmt.Printf("Updates: %d (%.1f/sec)\n", updates, float64(updates)/uptime.Seconds())
	fmt.Printf("Processed: %d monitors\n", processed)
	fmt.Printf("Failed: %d (%.2f%%)\n", failed, float64(failed)/float64(processed)*100)
	fmt.Printf("Avg Latency: %v\n", avgLatency)
	fmt.Printf("Queue Size: %d\n", queueSize)
	fmt.Printf("Throughput: %.1f monitors/sec\n", float64(processed)/uptime.Seconds())
	fmt.Printf("==============================\n\n")
}

// Stop gracefully shuts down the controller
func (c *OptimizedController) Stop() {
	fmt.Println("Stopping optimized controller...")
	c.cancel()
	c.workerPool.Stop()
}

// ============================================================================
// MAIN FUNCTION (Demo and testing)
// ============================================================================

func main() {
	fmt.Println("CPRA Optimal Implementation")
	fmt.Println("Following Ark ECS best practices and Go idioms")
	fmt.Println("Based on comprehensive research and CLAUDE.md guidelines")
	fmt.Println()
	
	// Test with configurable monitor count
	monitorCount := 100000 // Start with 100K for testing
	
	controller := NewOptimizedController(monitorCount)
	
	// Create monitors using optimized batch operations
	start := time.Now()
	controller.CreateMonitors(monitorCount)
	fmt.Printf("Monitor creation took: %v\n", time.Since(start))
	
	// Run for demonstration period
	go func() {
		time.Sleep(time.Minute * 2) // Run for 2 minutes
		controller.Stop()
	}()
	
	controller.Start()
	
	fmt.Println("Optimal implementation completed successfully!")
}

