package queue

import (
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

// BoundedQueue implements a high-performance bounded queue with batching.
// It now implements the queue.Queue interface.
type BoundedQueue struct {
	batches      chan []jobs.Job
	maxSize      int32
	maxBatch     int32
	closed       int32
	enqueued     int64
	dequeued     int64
	dropped      int64
	mu           sync.RWMutex
	batchTimeout time.Duration
}

// BoundedQueueConfig holds queue configuration.
type BoundedQueueConfig struct {
	MaxSize      int
	MaxBatch     int
	BatchTimeout time.Duration
}

// NewBoundedQueue creates a new bounded queue.
func NewBoundedQueue(config BoundedQueueConfig) *BoundedQueue {
	return &BoundedQueue{
		batches:      make(chan []jobs.Job, config.MaxSize),
		maxSize:      int32(config.MaxSize),
		maxBatch:     int32(config.MaxBatch),
		batchTimeout: config.BatchTimeout,
	}
}

// Enqueue adds a single job to the queue.
func (q *BoundedQueue) Enqueue(job jobs.Job) error {
	return q.EnqueueBatch([]jobs.Job{job})
}

// EnqueueBatch adds a batch of jobs to the queue.
func (q *BoundedQueue) EnqueueBatch(jobs []jobs.Job) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}

	if len(jobs) == 0 {
		return nil
	}

	enqueueTime := time.Now()
	for _, job := range jobs {
		job.SetEnqueueTime(enqueueTime)
	}

	batch := jobs
	if len(batch) > int(q.maxBatch) {
		batch = batch[:q.maxBatch]
	}

	select {
	case q.batches <- batch:
		atomic.AddInt64(&q.enqueued, int64(len(batch)))
		return nil
	default:
		atomic.AddInt64(&q.dropped, int64(len(batch)))
		return ErrQueueFull
	}
}

// Dequeue removes and returns a single job from the queue.
func (q *BoundedQueue) Dequeue() (jobs.Job, error) {
	// This is inefficient for a bounded queue, but it satisfies the interface.
	// The adaptive queue will have a proper single-item dequeue.
	select {
	case batch, ok := <-q.batches:
		if !ok {
			return nil, ErrQueueClosed
		}
		atomic.AddInt64(&q.dequeued, int64(len(batch)))
		if len(batch) > 1 {
			// Re-enqueue the rest of the batch. This is very inefficient.
			go func() { q.batches <- batch[1:] }()
		}
		return batch[0], nil
	case <-time.After(10 * time.Millisecond): // Non-blocking with a small timeout
		return nil, nil // Queue is empty
	}
}

// DequeueBatch removes a batch of jobs from the queue.
func (q *BoundedQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	select {
	case batch, ok := <-q.batches:
		if !ok {
			return nil, ErrQueueClosed
		}
		atomic.AddInt64(&q.dequeued, int64(len(batch)))
		if len(batch) > maxSize {
			// This is inefficient, but necessary to respect maxSize.
			go func() { q.batches <- batch[maxSize:] }()
			return batch[:maxSize], nil
		}
		return batch, nil
	default:
		// Non-blocking, return empty if no batch is immediately available.
		return nil, nil
	}
}

// Close closes the queue.
func (q *BoundedQueue) Close() {
	if atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		close(q.batches)
	}
}

// Stats returns current queue statistics.
func (q *BoundedQueue) Stats() QueueStats {
	return QueueStats{
		Enqueued:   atomic.LoadInt64(&q.enqueued),
		Dequeued:   atomic.LoadInt64(&q.dequeued),
		Dropped:    atomic.LoadInt64(&q.dropped),
		QueueDepth: len(q.batches),
		Capacity:   int(q.maxSize),
	}
}
