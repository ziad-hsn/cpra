package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/jobs"
)

var (
	ErrQueueFull   = errors.New("queue is full")
	ErrQueueClosed = errors.New("queue is closed")
)

// FixedBoundedQueue implements a thread-safe bounded queue with batching
// Fixes race conditions in the original implementation
type FixedBoundedQueue struct {
	// Queue storage
	batches  chan []jobs.Job
	maxSize  int32
	maxBatch int32

	// State management
	closed int32

	// Statistics (all atomic for thread safety)
	enqueued int64
	dequeued int64
	dropped  int64
	
	// Latency tracking (protected by mutex)
	maxQueueTime    int64 // nanoseconds
	totalQueueTime  int64 // nanoseconds for average calculation
	maxJobLatency   int64 // nanoseconds
	totalJobLatency int64 // nanoseconds for average calculation
	statsMu         sync.RWMutex // Separate mutex for stats to reduce contention

	// Configuration
	batchTimeout time.Duration
}

// BoundedQueueConfig holds queue configuration
type BoundedQueueConfig struct {
	MaxSize      int           // Maximum number of batches
	MaxBatch     int           // Maximum jobs per batch
	BatchTimeout time.Duration // Maximum time to wait for batch to fill
}

// Stats holds queue statistics
type Stats struct {
	Enqueued        int64
	Dequeued        int64
	Dropped         int64
	QueueDepth      int32
	BatchCount      int32
	MaxQueueTime    time.Duration
	AvgQueueTime    time.Duration
	MaxJobLatency   time.Duration
	AvgJobLatency   time.Duration
}

// NewFixedBoundedQueue creates a new thread-safe bounded queue
func NewFixedBoundedQueue(config BoundedQueueConfig) *FixedBoundedQueue {
	return &FixedBoundedQueue{
		batches:      make(chan []jobs.Job, config.MaxSize),
		maxSize:      int32(config.MaxSize),
		maxBatch:     int32(config.MaxBatch),
		batchTimeout: config.BatchTimeout,
	}
}

// EnqueueBatch adds a batch of jobs to the queue
// Fixed: Proper handling of job copying to avoid shared data races
func (q *FixedBoundedQueue) EnqueueBatch(batch []jobs.Job) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}

	if len(batch) == 0 {
		return nil
	}

	// Create a copy of the batch to avoid modifying shared data
	batchCopy := make([]jobs.Job, len(batch))
	enqueueTime := time.Now()
	
	// Copy jobs and set enqueue time on the copies
	for i, job := range batch {
		batchCopy[i] = job // Copy the job
		batchCopy[i].SetEnqueueTime(enqueueTime) // Modify the copy, not original
	}

	// Limit batch size
	if len(batchCopy) > int(q.maxBatch) {
		batchCopy = batchCopy[:q.maxBatch]
	}

	select {
	case q.batches <- batchCopy:
		atomic.AddInt64(&q.enqueued, int64(len(batchCopy)))
		return nil
	default:
		// Queue is full, drop the batch
		atomic.AddInt64(&q.dropped, int64(len(batchCopy)))
		return ErrQueueFull
	}
}

// DequeueBatch removes a batch of jobs from the queue
func (q *FixedBoundedQueue) DequeueBatch(ctx context.Context) ([]jobs.Job, error) {
	if atomic.LoadInt32(&q.closed) == 1 {
		return nil, ErrQueueClosed
	}

	// Create timeout context if batch timeout is configured
	var timeoutCtx context.Context
	var cancel context.CancelFunc
	if q.batchTimeout > 0 {
		timeoutCtx, cancel = context.WithTimeout(ctx, q.batchTimeout)
		defer cancel()
	} else {
		timeoutCtx = ctx
	}

	select {
	case batch := <-q.batches:
		// Calculate queue time for latency tracking
		dequeueTime := time.Now()
		for _, job := range batch {
			if !job.GetEnqueueTime().IsZero() {
				queueTime := dequeueTime.Sub(job.GetEnqueueTime())
				q.updateQueueTime(queueTime)
			}
		}
		
		atomic.AddInt64(&q.dequeued, int64(len(batch)))
		return batch, nil
	case <-timeoutCtx.Done():
		if timeoutCtx != ctx {
			// Batch timeout occurred, return timeout error
			return nil, context.DeadlineExceeded
		}
		return nil, ctx.Err()
	}
}

// Close closes the queue
func (q *FixedBoundedQueue) Close() {
	if atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		close(q.batches)
	}
}

// updateQueueTime updates queue time statistics thread-safely
func (q *FixedBoundedQueue) updateQueueTime(queueTime time.Duration) {
	q.statsMu.Lock()
	defer q.statsMu.Unlock()
	
	queueTimeNs := queueTime.Nanoseconds()
	
	// Update max queue time
	if queueTimeNs > q.maxQueueTime {
		q.maxQueueTime = queueTimeNs
	}
	
	// Update total for average calculation
	q.totalQueueTime += queueTimeNs
}

// UpdateJobLatency updates job execution latency statistics thread-safely
func (q *FixedBoundedQueue) UpdateJobLatency(jobLatency time.Duration) {
	q.statsMu.Lock()
	defer q.statsMu.Unlock()
	
	latencyNs := jobLatency.Nanoseconds()
	
	// Update max job latency
	if latencyNs > q.maxJobLatency {
		q.maxJobLatency = latencyNs
	}
	
	// Update total for average calculation
	q.totalJobLatency += latencyNs
}

// Stats returns current queue statistics
func (q *FixedBoundedQueue) Stats() Stats {
	q.statsMu.RLock()
	maxQueueTime := q.maxQueueTime
	totalQueueTime := q.totalQueueTime
	maxJobLatency := q.maxJobLatency
	totalJobLatency := q.totalJobLatency
	q.statsMu.RUnlock()
	
	dequeued := atomic.LoadInt64(&q.dequeued)
	
	// Calculate averages
	var avgQueueTime, avgJobLatency time.Duration
	if dequeued > 0 {
		avgQueueTime = time.Duration(totalQueueTime / dequeued)
		avgJobLatency = time.Duration(totalJobLatency / dequeued)
	}
	
	return Stats{
		Enqueued:      atomic.LoadInt64(&q.enqueued),
		Dequeued:      dequeued,
		Dropped:       atomic.LoadInt64(&q.dropped),
		QueueDepth:    int32(len(q.batches)),
		BatchCount:    int32(cap(q.batches)),
		MaxQueueTime:  time.Duration(maxQueueTime),
		AvgQueueTime:  avgQueueTime,
		MaxJobLatency: time.Duration(maxJobLatency),
		AvgJobLatency: avgJobLatency,
	}
}

// FixedBatchCollector collects individual jobs into batches with proper synchronization
type FixedBatchCollector struct {
	queue        *FixedBoundedQueue
	currentBatch []jobs.Job
	batchSize    int
	timeout      time.Duration

	lastFlush time.Time
	closed    int32
	mu        sync.Mutex // Protects currentBatch and lastFlush
	
	// Channel for coordinating shutdown
	shutdownCh chan struct{}
	wg         sync.WaitGroup
}

// NewFixedBatchCollector creates a new thread-safe batch collector
func NewFixedBatchCollector(queue *FixedBoundedQueue, batchSize int, timeout time.Duration) *FixedBatchCollector {
	bc := &FixedBatchCollector{
		queue:      queue,
		batchSize:  batchSize,
		timeout:    timeout,
		lastFlush:  time.Now(),
		shutdownCh: make(chan struct{}),
	}
	
	// Start the flush timer goroutine
	bc.wg.Add(1)
	go bc.flushTimer()
	
	return bc
}

// Add adds a job to the current batch
func (bc *FixedBatchCollector) Add(job jobs.Job) error {
	if atomic.LoadInt32(&bc.closed) == 1 {
		return ErrQueueClosed
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.currentBatch = append(bc.currentBatch, job)

	// Flush if batch is full
	if len(bc.currentBatch) >= bc.batchSize {
		return bc.flushLocked()
	}

	return nil
}

// flushTimer periodically flushes incomplete batches
func (bc *FixedBatchCollector) flushTimer() {
	defer bc.wg.Done()
	
	ticker := time.NewTicker(bc.timeout)
	defer ticker.Stop()

	for {
		select {
		case <-bc.shutdownCh:
			return
		case <-ticker.C:
			bc.mu.Lock()
			if time.Since(bc.lastFlush) >= bc.timeout && len(bc.currentBatch) > 0 {
				bc.flushLocked() // Ignore error during periodic flush
			}
			bc.mu.Unlock()
		}
	}
}

// flushLocked flushes the current batch (must hold mutex)
func (bc *FixedBatchCollector) flushLocked() error {
	if len(bc.currentBatch) == 0 {
		return nil
	}

	// Create a copy of the batch
	batch := make([]jobs.Job, len(bc.currentBatch))
	copy(batch, bc.currentBatch)

	// Reset current batch
	bc.currentBatch = bc.currentBatch[:0]
	bc.lastFlush = time.Now()

	// Enqueue the batch (release mutex during I/O)
	bc.mu.Unlock()
	err := bc.queue.EnqueueBatch(batch)
	bc.mu.Lock()
	
	return err
}

// Flush manually flushes the current batch
func (bc *FixedBatchCollector) Flush() error {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.flushLocked()
}

// Close closes the batch collector
func (bc *FixedBatchCollector) Close() error {
	if atomic.CompareAndSwapInt32(&bc.closed, 0, 1) {
		// Signal shutdown to flush timer
		close(bc.shutdownCh)
		
		// Wait for flush timer to stop
		bc.wg.Wait()
		
		// Flush any remaining items
		return bc.Flush()
	}
	return nil
}

// Interface compatibility - ensure we can replace the original
type QueueInterface interface {
	EnqueueBatch(batch []jobs.Job) error
	DequeueBatch(ctx context.Context) ([]jobs.Job, error)
	Close()
	Stats() Stats
}

// Ensure both implementations satisfy the interface
var _ QueueInterface = (*BoundedQueue)(nil)
var _ QueueInterface = (*FixedBoundedQueue)(nil)

