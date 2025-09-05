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

// BoundedQueue implements a high-performance bounded queue with batching
type BoundedQueue struct {
	// Queue storage
	batches  chan []jobs.Job
	maxSize  int32
	maxBatch int32

	// State management
	closed int32

	// Statistics
	enqueued int64
	dequeued int64
	dropped  int64
	
	// Latency tracking
	maxQueueTime    int64 // nanoseconds
	totalQueueTime  int64 // nanoseconds for average calculation
	maxJobLatency   int64 // nanoseconds
	totalJobLatency int64 // nanoseconds for average calculation
	mu              sync.RWMutex

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

// NewBoundedQueue creates a new bounded queue
func NewBoundedQueue(config BoundedQueueConfig) *BoundedQueue {
	return &BoundedQueue{
		batches:      make(chan []jobs.Job, config.MaxSize),
		maxSize:      int32(config.MaxSize),
		maxBatch:     int32(config.MaxBatch),
		batchTimeout: config.BatchTimeout,
	}
}

// EnqueueBatch adds a batch of jobs to the queue
func (q *BoundedQueue) EnqueueBatch(batch []jobs.Job) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}

	if len(batch) == 0 {
		return nil
	}

	// Set enqueue time for all jobs in batch
	enqueueTime := time.Now()
	for _, job := range batch {
		job.SetEnqueueTime(enqueueTime)
	}

	// Limit batch size
	if len(batch) > int(q.maxBatch) {
		batch = batch[:q.maxBatch]
	}

	select {
	case q.batches <- batch:
		atomic.AddInt64(&q.enqueued, int64(len(batch)))
		return nil
	default:
		// Queue is full, drop the batch
		atomic.AddInt64(&q.dropped, int64(len(batch)))
		return ErrQueueFull
	}
}

// DequeueBatch removes a batch of jobs from the queue
func (q *BoundedQueue) DequeueBatch(ctx context.Context) ([]jobs.Job, error) {
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
func (q *BoundedQueue) Close() {
	if atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		close(q.batches)
	}
}

// updateQueueTime updates queue time statistics
func (q *BoundedQueue) updateQueueTime(queueTime time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	queueTimeNs := queueTime.Nanoseconds()
	
	// Update max queue time
	if queueTimeNs > q.maxQueueTime {
		q.maxQueueTime = queueTimeNs
	}
	
	// Update total for average calculation
	q.totalQueueTime += queueTimeNs
}

// UpdateJobLatency updates job execution latency statistics
func (q *BoundedQueue) UpdateJobLatency(jobLatency time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	latencyNs := jobLatency.Nanoseconds()
	
	// Update max job latency
	if latencyNs > q.maxJobLatency {
		q.maxJobLatency = latencyNs
	}
	
	// Update total for average calculation
	q.totalJobLatency += latencyNs
}

// Stats returns current queue statistics
func (q *BoundedQueue) Stats() Stats {
	q.mu.RLock()
	maxQueueTime := q.maxQueueTime
	totalQueueTime := q.totalQueueTime
	maxJobLatency := q.maxJobLatency
	totalJobLatency := q.totalJobLatency
	q.mu.RUnlock()
	
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

// BatchCollector collects individual jobs into batches
type BatchCollector struct {
	queue        *BoundedQueue
	currentBatch []jobs.Job
	batchSize    int
	timeout      time.Duration

	lastFlush time.Time
	closed    int32
}

// flushTimer periodically flushes incomplete batches
func (bc *BatchCollector) flushTimer() {
	ticker := time.NewTicker(bc.timeout)
	defer ticker.Stop()

	for range ticker.C {
		if atomic.LoadInt32(&bc.closed) == 1 {
			return
		}

		if time.Since(bc.lastFlush) >= bc.timeout && len(bc.currentBatch) > 0 {
			err := bc.flushLocked()
			if err != nil {
				return
			}
		}
	}
}

// flushLocked flushes the current batch (must hold mutex)
func (bc *BatchCollector) flushLocked() error {
	if len(bc.currentBatch) == 0 {
		return nil
	}

	// Create a copy of the batch
	batch := make([]jobs.Job, len(bc.currentBatch))
	copy(batch, bc.currentBatch)

	// Reset current batch
	bc.currentBatch = bc.currentBatch[:0]
	bc.lastFlush = time.Now()

	// Enqueue the batch
	return bc.queue.EnqueueBatch(batch)
}

// Flush manually flushes the current batch
func (bc *BatchCollector) Flush() error {
	return bc.flushLocked()
}

// Close closes the batch collector
func (bc *BatchCollector) Close() error {
	if atomic.CompareAndSwapInt32(&bc.closed, 0, 1) {
		// Flush any remaining items
		return bc.Flush()
	}
	return nil
}
