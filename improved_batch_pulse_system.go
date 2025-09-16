package systems

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue"
	"github.com/mlange-42/ark/ecs"
)

// ImprovedBatchPulseSystem replaces the current BatchPulseSystem with optimized batching logic
type ImprovedBatchPulseSystem struct {
	world  *ecs.World
	mapper *entities.EntityManager
	queue  *queue.BoundedQueue
	logger SystemLogger

	// Adaptive batching configuration
	minBatchSize     int
	maxBatchSize     int
	currentBatchSize int

	// Memory pools for efficient allocation
	jobPool    sync.Pool
	entityPool sync.Pool

	// Performance tracking
	lastProcessTime   time.Duration
	queueDepthHistory []float64
	historyMutex      sync.RWMutex

	// Metrics
	processedJobs uint64
	droppedJobs   uint64
	batchCount    uint64
}

// NewImprovedBatchPulseSystem creates an optimized batch pulse system
func NewImprovedBatchPulseSystem(
	world *ecs.World,
	mapper *entities.EntityManager,
	queue *queue.BoundedQueue,
	logger SystemLogger) *ImprovedBatchPulseSystem {

	system := &ImprovedBatchPulseSystem{
		world:            world,
		mapper:           mapper,
		queue:            queue,
		logger:           logger,
		minBatchSize:     25,  // Much smaller than current 5000
		maxBatchSize:     100, // Still much smaller than current 5000
		currentBatchSize: 50,  // Start with middle ground
		queueDepthHistory: make([]float64, 0, 10),
	}

	// Initialize memory pools
	system.jobPool = sync.Pool{
		New: func() interface{} {
			return make([]jobs.Job, 0, system.maxBatchSize)
		},
	}

	system.entityPool = sync.Pool{
		New: func() interface{} {
			return make([]ecs.Entity, 0, system.maxBatchSize)
		},
	}

	return system
}

// Update implements the optimized batching logic
func (ibps *ImprovedBatchPulseSystem) Update(ctx context.Context) error {
	start := time.Now()

	// Calculate optimal batch size based on current conditions
	batchSize := ibps.calculateOptimalBatchSize()

	// Get reusable slices from memory pool
	batchJobs := ibps.getJobBatch()
	batchEntities := ibps.getEntityBatch()
	defer ibps.returnJobBatch(batchJobs)
	defer ibps.returnEntityBatch(batchEntities)

	processedCount := 0

	// Stream processing: process batches as they fill up
	ibps.mapper.PulseNeeded.Map(func(entity ecs.Entity, comp *components.PulseNeeded) {
		// Get the pulse job for this entity
		pulseJob := ibps.mapper.PulseJob.Get(entity).Job

		// Add to current batch
		batchJobs = append(batchJobs, pulseJob)
		batchEntities = append(batchEntities, entity)

		// Process immediately when batch reaches optimal size
		if len(batchJobs) >= batchSize {
			processed := ibps.processStreamingBatch(batchJobs, batchEntities)
			processedCount += processed
			atomic.AddUint64(&ibps.batchCount, 1)

			// Reset slices but keep capacity for reuse
			batchJobs = batchJobs[:0]
			batchEntities = batchEntities[:0]
		}
	})

	// Process final partial batch if any entities remain
	if len(batchJobs) > 0 {
		processed := ibps.processStreamingBatch(batchJobs, batchEntities)
		processedCount += processed
		atomic.AddUint64(&ibps.batchCount, 1)
	}

	// Update adaptive batching strategy based on performance
	processingTime := time.Since(start)
	ibps.updateBatchingStrategy(processingTime, processedCount)

	return nil
}

// processStreamingBatch handles individual job enqueuing with state safety
func (ibps *ImprovedBatchPulseSystem) processStreamingBatch(
	batchJobs []jobs.Job,
	batchEntities []ecs.Entity) int {

	if len(batchJobs) == 0 {
		return 0
	}

	successCount := 0

	// Process each job individually for better success rate and state safety
	for i, job := range batchJobs {
		entity := batchEntities[i]

		// Create a single-job batch for the current queue interface
		singleJobBatch := []jobs.Job{job}

		// Try to enqueue the job
		if err := ibps.queue.EnqueueBatch(singleJobBatch); err == nil {
			// SUCCESS: Job enqueued successfully, safe to transition state
			ibps.mapper.PulseNeeded.Remove(entity)
			ibps.mapper.PulsePending.Add(entity, &components.PulsePending{
				StartTime: time.Now(),
			})
			successCount++
			atomic.AddUint64(&ibps.processedJobs, 1)

		} else {
			// FAILURE: Queue full, keep entity in PulseNeeded state for retry
			// This is the critical fix - don't transition state on failure
			atomic.AddUint64(&ibps.droppedJobs, 1)
		}
	}

	// Log performance information
	if successCount < len(batchJobs) {
		ibps.logger.Debug("Batch processed %d/%d jobs, %d dropped due to queue full",
			successCount, len(batchJobs), len(batchJobs)-successCount)
	}

	return successCount
}

// calculateOptimalBatchSize dynamically adjusts batch size based on system performance
func (ibps *ImprovedBatchPulseSystem) calculateOptimalBatchSize() int {
	// Get current queue statistics
	queueStats := ibps.queue.Stats()
	loadFactor := float64(queueStats.QueueDepth) / float64(queueStats.QueueDepth+1000) // Avoid division by zero

	// Update load history for smoothing
	ibps.historyMutex.Lock()
	ibps.queueDepthHistory = append(ibps.queueDepthHistory, loadFactor)
	if len(ibps.queueDepthHistory) > 10 {
		ibps.queueDepthHistory = ibps.queueDepthHistory[1:]
	}

	// Calculate average load over recent history
	var avgLoad float64
	for _, load := range ibps.queueDepthHistory {
		avgLoad += load
	}
	if len(ibps.queueDepthHistory) > 0 {
		avgLoad /= float64(len(ibps.queueDepthHistory))
	}
	ibps.historyMutex.Unlock()

	// Determine target batch size based on system conditions
	var targetBatchSize int

	switch {
	case avgLoad > 0.8:
		// High load: use small batches for responsiveness
		targetBatchSize = ibps.minBatchSize

	case avgLoad < 0.3:
		// Low load: use larger batches for efficiency
		targetBatchSize = ibps.maxBatchSize

	case ibps.lastProcessTime > 10*time.Millisecond:
		// Processing is slow: reduce batch size
		targetBatchSize = max(ibps.minBatchSize, ibps.currentBatchSize-10)

	default:
		// Medium load: use balanced batch size
		targetBatchSize = (ibps.minBatchSize + ibps.maxBatchSize) / 2
	}

	// Smooth transitions to avoid oscillation
	if targetBatchSize > ibps.currentBatchSize {
		ibps.currentBatchSize = min(targetBatchSize, ibps.currentBatchSize+5)
	} else if targetBatchSize < ibps.currentBatchSize {
		ibps.currentBatchSize = max(targetBatchSize, ibps.currentBatchSize-5)
	}

	return ibps.currentBatchSize
}

// updateBatchingStrategy adjusts the batching strategy based on recent performance
func (ibps *ImprovedBatchPulseSystem) updateBatchingStrategy(processingTime time.Duration, processedCount int) {
	ibps.lastProcessTime = processingTime

	// Log performance metrics periodically
	if ibps.batchCount%100 == 0 { // Every 100 batches
		processed := atomic.LoadUint64(&ibps.processedJobs)
		dropped := atomic.LoadUint64(&ibps.droppedJobs)
		total := processed + dropped

		var successRate float64
		if total > 0 {
			successRate = float64(processed) / float64(total) * 100
		}

		ibps.logger.Info("Batch performance: processed=%d, dropped=%d, success_rate=%.1f%%, batch_size=%d, process_time=%v",
			processed, dropped, successRate, ibps.currentBatchSize, processingTime)
	}
}

// Memory pool management functions
func (ibps *ImprovedBatchPulseSystem) getJobBatch() []jobs.Job {
	if batch := ibps.jobPool.Get(); batch != nil {
		return batch.([]jobs.Job)[:0] // Reset length but keep capacity
	}
	return make([]jobs.Job, 0, ibps.maxBatchSize)
}

func (ibps *ImprovedBatchPulseSystem) returnJobBatch(batch []jobs.Job) {
	if cap(batch) <= ibps.maxBatchSize*2 { // Don't pool overly large slices
		ibps.jobPool.Put(batch)
	}
}

func (ibps *ImprovedBatchPulseSystem) getEntityBatch() []ecs.Entity {
	if batch := ibps.entityPool.Get(); batch != nil {
		return batch.([]ecs.Entity)[:0]
	}
	return make([]ecs.Entity, 0, ibps.maxBatchSize)
}

func (ibps *ImprovedBatchPulseSystem) returnEntityBatch(batch []ecs.Entity) {
	if cap(batch) <= ibps.maxBatchSize*2 {
		ibps.entityPool.Put(batch)
	}
}

// GetMetrics returns current performance metrics
func (ibps *ImprovedBatchPulseSystem) GetMetrics() (entities, batches int) {
	return int(atomic.LoadUint64(&ibps.processedJobs)), int(atomic.LoadUint64(&ibps.batchCount))
}

// SetBatchSizeRange allows runtime adjustment of batch size limits
func (ibps *ImprovedBatchPulseSystem) SetBatchSizeRange(min, max int) {
	if min > 0 && max > min {
		ibps.minBatchSize = min
		ibps.maxBatchSize = max
		if ibps.currentBatchSize < min {
			ibps.currentBatchSize = min
		} else if ibps.currentBatchSize > max {
			ibps.currentBatchSize = max
		}
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

// SystemLogger interface for compatibility
type SystemLogger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

