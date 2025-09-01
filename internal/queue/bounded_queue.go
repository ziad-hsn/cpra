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

	// Configuration
	batchTimeout time.Duration

	mu sync.RWMutex
}

// BoundedQueueConfig holds queue configuration
type BoundedQueueConfig struct {
	MaxSize      int           // Maximum number of batches
	MaxBatch     int           // Maximum jobs per batch
	BatchTimeout time.Duration // Maximum time to wait for batch to fill
}

// Stats holds queue statistics
type Stats struct {
	Enqueued   int64
	Dequeued   int64
	Dropped    int64
	QueueDepth int32
	BatchCount int32
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

	select {
	case batch := <-q.batches:
		atomic.AddInt64(&q.dequeued, int64(len(batch)))
		return batch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the queue
func (q *BoundedQueue) Close() {
	if atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		close(q.batches)
	}
}

// Stats returns current queue statistics
func (q *BoundedQueue) Stats() Stats {
	return Stats{
		Enqueued:   atomic.LoadInt64(&q.enqueued),
		Dequeued:   atomic.LoadInt64(&q.dequeued),
		Dropped:    atomic.LoadInt64(&q.dropped),
		QueueDepth: int32(len(q.batches)),
		BatchCount: int32(cap(q.batches)),
	}
}

// BatchCollector collects individual jobs into batches
type BatchCollector struct {
	queue        *BoundedQueue
	currentBatch []jobs.Job
	batchSize    int
	timeout      time.Duration

	mu        sync.Mutex
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

		bc.mu.Lock()
		if time.Since(bc.lastFlush) >= bc.timeout && len(bc.currentBatch) > 0 {
			err := bc.flushLocked()
			if err != nil {
				return
			}
		}
		bc.mu.Unlock()
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
	bc.mu.Lock()
	defer bc.mu.Unlock()
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
