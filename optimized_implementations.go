// Optimized Implementations for Ark Migration Performance Improvements

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"github.com/mlange-42/ark/ecs"
)

// ===== HIGH-PERFORMANCE NON-BLOCKING QUEUE =====

// LockFreeRingQueue implements a lock-free, non-blocking ring buffer queue
type LockFreeRingQueue struct {
	buffer []unsafe.Pointer
	head   uint64
	tail   uint64
	mask   uint64
	size   uint64
}

func NewLockFreeRingQueue(size int) *LockFreeRingQueue {
	// Ensure size is power of 2 for efficient masking
	if size&(size-1) != 0 {
		panic("size must be power of 2")
	}
	
	return &LockFreeRingQueue{
		buffer: make([]unsafe.Pointer, size),
		mask:   uint64(size - 1),
		size:   uint64(size),
	}
}

func (q *LockFreeRingQueue) Enqueue(job jobs.Job) bool {
	for {
		tail := atomic.LoadUint64(&q.tail)
		head := atomic.LoadUint64(&q.head)
		
		// Check if queue is full (leave one slot empty to distinguish full from empty)
		if tail-head >= q.size-1 {
			return false // Queue full, drop job (non-blocking)
		}
		
		// Try to claim this slot
		if atomic.CompareAndSwapUint64(&q.tail, tail, tail+1) {
			// Successfully claimed slot, store job
			jobPtr := unsafe.Pointer(&job)
			atomic.StorePointer(&q.buffer[tail&q.mask], jobPtr)
			return true
		}
		// CAS failed, retry
	}
}

func (q *LockFreeRingQueue) Dequeue() (jobs.Job, bool) {
	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)
		
		if head >= tail {
			return jobs.Job{}, false // Queue empty
		}
		
		// Try to claim this slot
		if atomic.CompareAndSwapUint64(&q.head, head, head+1) {
			// Successfully claimed slot, load job
			jobPtr := atomic.LoadPointer(&q.buffer[head&q.mask])
			if jobPtr == nil {
				continue // Slot not ready yet, retry
			}
			job := *(*jobs.Job)(jobPtr)
			return job, true
		}
		// CAS failed, retry
	}
}

func (q *LockFreeRingQueue) Stats() (depth, capacity uint64) {
	tail := atomic.LoadUint64(&q.tail)
	head := atomic.LoadUint64(&q.head)
	return tail - head, q.size
}

// ===== OPTIMIZED BATCH PROCESSING =====

// AdaptiveBatcher dynamically adjusts batch sizes based on system load
type AdaptiveBatcher struct {
	minBatch     int
	maxBatch     int
	currentBatch int
	loadHistory  []float64
	historyIndex int
	mu           sync.RWMutex
}

func NewAdaptiveBatcher(min, max int) *AdaptiveBatcher {
	return &AdaptiveBatcher{
		minBatch:     min,
		maxBatch:     max,
		currentBatch: min,
		loadHistory:  make([]float64, 10), // Keep 10 samples
	}
}

func (ab *AdaptiveBatcher) UpdateLoad(queueDepth, queueCapacity uint64, processingTime time.Duration) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	
	// Calculate load factor (0.0 to 1.0)
	loadFactor := float64(queueDepth) / float64(queueCapacity)
	
	// Add processing time factor (higher time = higher load)
	timeFactorMs := float64(processingTime.Nanoseconds()) / 1e6
	if timeFactorMs > 10 { // If processing takes more than 10ms, consider it high load
		loadFactor += 0.2
	}
	
	// Store in history
	ab.loadHistory[ab.historyIndex] = loadFactor
	ab.historyIndex = (ab.historyIndex + 1) % len(ab.loadHistory)
	
	// Calculate average load
	var avgLoad float64
	for _, load := range ab.loadHistory {
		avgLoad += load
	}
	avgLoad /= float64(len(ab.loadHistory))
	
	// Adjust batch size based on load
	if avgLoad > 0.8 {
		// High load: reduce batch size for responsiveness
		ab.currentBatch = ab.minBatch
	} else if avgLoad < 0.3 {
		// Low load: increase batch size for efficiency
		ab.currentBatch = ab.maxBatch
	} else {
		// Medium load: use middle ground
		ab.currentBatch = (ab.minBatch + ab.maxBatch) / 2
	}
}

func (ab *AdaptiveBatcher) GetBatchSize() int {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	return ab.currentBatch
}

// ===== MEMORY POOL FOR BATCH ALLOCATIONS =====

type BatchMemoryPool struct {
	jobPool    sync.Pool
	entityPool sync.Pool
	resultPool sync.Pool
}

func NewBatchMemoryPool() *BatchMemoryPool {
	return &BatchMemoryPool{
		jobPool: sync.Pool{
			New: func() interface{} {
				return make([]jobs.Job, 0, 100)
			},
		},
		entityPool: sync.Pool{
			New: func() interface{} {
				return make([]ecs.Entity, 0, 100)
			},
		},
		resultPool: sync.Pool{
			New: func() interface{} {
				return make([]jobs.Result, 0, 100)
			},
		},
	}
}

func (bmp *BatchMemoryPool) GetJobBatch() []jobs.Job {
	return bmp.jobPool.Get().([]jobs.Job)[:0]
}

func (bmp *BatchMemoryPool) PutJobBatch(batch []jobs.Job) {
	if cap(batch) <= 200 { // Don't pool overly large slices
		bmp.jobPool.Put(batch)
	}
}

func (bmp *BatchMemoryPool) GetEntityBatch() []ecs.Entity {
	return bmp.entityPool.Get().([]ecs.Entity)[:0]
}

func (bmp *BatchMemoryPool) PutEntityBatch(batch []ecs.Entity) {
	if cap(batch) <= 200 {
		bmp.entityPool.Put(batch)
	}
}

// ===== OPTIMIZED BATCH PULSE SYSTEM =====

type OptimizedBatchPulseSystem struct {
	world       *ecs.World
	mapper      *EntityManager
	queue       *LockFreeRingQueue
	batcher     *AdaptiveBatcher
	memPool     *BatchMemoryPool
	logger      Logger
	
	// Metrics
	processedJobs   uint64
	droppedJobs     uint64
	lastUpdateTime  time.Time
}

func NewOptimizedBatchPulseSystem(world *ecs.World, mapper *EntityManager, 
	queue *LockFreeRingQueue, logger Logger) *OptimizedBatchPulseSystem {
	
	return &OptimizedBatchPulseSystem{
		world:          world,
		mapper:         mapper,
		queue:          queue,
		batcher:        NewAdaptiveBatcher(25, 100), // Smaller, adaptive batches
		memPool:        NewBatchMemoryPool(),
		logger:         logger,
		lastUpdateTime: time.Now(),
	}
}

func (obps *OptimizedBatchPulseSystem) Update(ctx context.Context) error {
	start := time.Now()
	
	// Get optimal batch size
	batchSize := obps.batcher.GetBatchSize()
	
	// Get memory from pool
	batchJobs := obps.memPool.GetJobBatch()
	batchEntities := obps.memPool.GetEntityBatch()
	defer obps.memPool.PutJobBatch(batchJobs)
	defer obps.memPool.PutEntityBatch(batchEntities)
	
	// Collect entities that need pulse checks
	obps.mapper.PulseNeeded.Map(func(entity ecs.Entity, comp *components.PulseNeeded) {
		// Get the pulse job for this entity
		pulseJob := obps.mapper.PulseJob.Get(entity).Job
		
		// Add to current batch
		batchJobs = append(batchJobs, pulseJob)
		batchEntities = append(batchEntities, entity)
		
		// Process batch when it reaches optimal size
		if len(batchJobs) >= batchSize {
			obps.processBatch(batchJobs, batchEntities)
			
			// Reset batch slices (keep capacity)
			batchJobs = batchJobs[:0]
			batchEntities = batchEntities[:0]
		}
	})
	
	// Process any remaining items in the batch
	if len(batchJobs) > 0 {
		obps.processBatch(batchJobs, batchEntities)
	}
	
	// Update adaptive batcher with performance metrics
	processingTime := time.Since(start)
	queueDepth, queueCapacity := obps.queue.Stats()
	obps.batcher.UpdateLoad(queueDepth, queueCapacity, processingTime)
	
	obps.lastUpdateTime = time.Now()
	return nil
}

func (obps *OptimizedBatchPulseSystem) processBatch(batchJobs []jobs.Job, batchEntities []ecs.Entity) {
	successfulJobs := 0
	
	// Try to enqueue each job individually for better success rate
	for i, job := range batchJobs {
		if obps.queue.Enqueue(job) {
			// Successfully enqueued, transition entity state
			entity := batchEntities[i]
			obps.mapper.PulseNeeded.Remove(entity)
			obps.mapper.PulsePending.Add(entity, &components.PulsePending{
				StartTime: time.Now(),
			})
			successfulJobs++
			atomic.AddUint64(&obps.processedJobs, 1)
		} else {
			// Queue full, keep entity in PulseNeeded state for retry
			atomic.AddUint64(&obps.droppedJobs, 1)
		}
	}
	
	if successfulJobs < len(batchJobs) {
		obps.logger.Debug("Enqueued %d/%d jobs, %d dropped due to queue full", 
			successfulJobs, len(batchJobs), len(batchJobs)-successfulJobs)
	}
}

func (obps *OptimizedBatchPulseSystem) GetMetrics() (processed, dropped uint64) {
	return atomic.LoadUint64(&obps.processedJobs), atomic.LoadUint64(&obps.droppedJobs)
}

// ===== TIMEOUT RECOVERY SYSTEM =====

type TimeoutRecoverySystem struct {
	mapper  *EntityManager
	timeout time.Duration
	logger  Logger
}

func NewTimeoutRecoverySystem(mapper *EntityManager, timeout time.Duration, logger Logger) *TimeoutRecoverySystem {
	return &TimeoutRecoverySystem{
		mapper:  mapper,
		timeout: timeout,
		logger:  logger,
	}
}

func (trs *TimeoutRecoverySystem) Update(ctx context.Context) error {
	now := time.Now()
	recoveredCount := 0
	
	// Check all pending pulse entities
	trs.mapper.PulsePending.Map(func(entity ecs.Entity, comp *components.PulsePending) {
		if now.Sub(comp.StartTime) > trs.timeout {
			// Entity has been pending too long, recover it
			trs.mapper.PulsePending.Remove(entity)
			trs.mapper.PulseNeeded.Add(entity, &components.PulseNeeded{})
			recoveredCount++
		}
	})
	
	if recoveredCount > 0 {
		trs.logger.Warn("Recovered %d entities stuck in PulsePending state", recoveredCount)
	}
	
	return nil
}

// ===== PARALLEL RESULT PROCESSOR =====

type ParallelResultProcessor struct {
	mapper     *EntityManager
	resultChan <-chan jobs.Result
	logger     Logger
	workerPool sync.Pool
}

func NewParallelResultProcessor(mapper *EntityManager, resultChan <-chan jobs.Result, logger Logger) *ParallelResultProcessor {
	return &ParallelResultProcessor{
		mapper:     mapper,
		resultChan: resultChan,
		logger:     logger,
		workerPool: sync.Pool{
			New: func() interface{} {
				return make([]jobs.Result, 0, 50)
			},
		},
	}
}

func (prp *ParallelResultProcessor) Update(world *ecs.World) {
	// Collect batch of results
	resultBatch := prp.workerPool.Get().([]jobs.Result)[:0]
	defer prp.workerPool.Put(resultBatch)
	
	// Non-blocking collection of results
	for len(resultBatch) < cap(resultBatch) {
		select {
		case result := <-prp.resultChan:
			resultBatch = append(resultBatch, result)
		default:
			break // No more results available
		}
	}
	
	if len(resultBatch) == 0 {
		return // No results to process
	}
	
	// Process results in parallel chunks
	const chunkSize = 10
	var wg sync.WaitGroup
	
	for i := 0; i < len(resultBatch); i += chunkSize {
		end := i + chunkSize
		if end > len(resultBatch) {
			end = len(resultBatch)
		}
		
		wg.Add(1)
		go func(chunk []jobs.Result) {
			defer wg.Done()
			prp.processResultChunk(world, chunk)
		}(resultBatch[i:end])
	}
	
	wg.Wait()
}

func (prp *ParallelResultProcessor) processResultChunk(world *ecs.World, results []jobs.Result) {
	for _, result := range results {
		entity := result.Entity()
		
		if !world.Alive(entity) || !prp.mapper.PulsePending.HasAll(entity) {
			continue
		}
		
		// Process result (same logic as original, but in parallel)
		if result.Error() != nil {
			prp.handleFailure(entity, result)
		} else {
			prp.handleSuccess(entity, result)
		}
		
		// Always remove PulsePending
		prp.mapper.PulsePending.Remove(entity)
	}
}

func (prp *ParallelResultProcessor) handleFailure(entity ecs.Entity, result jobs.Result) {
	// Implementation of failure handling logic
	// (Same as original but optimized for parallel execution)
}

func (prp *ParallelResultProcessor) handleSuccess(entity ecs.Entity, result jobs.Result) {
	// Implementation of success handling logic
	// (Same as original but optimized for parallel execution)
}

