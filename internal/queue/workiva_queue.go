package queue

import (
	"cpra/internal/jobs"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// WorkivaQueue is a queue implementation inspired by Workiva's go-datastructures
// It combines the best aspects: unbounded growth with better performance characteristics
// while maintaining compatibility with the existing Queue interface
type WorkivaQueue struct {
	items    []jobs.Job
	head     int64
	tail     int64
	mu       sync.RWMutex
	disposed int32

	// Metrics
	enqueuedCount       int64
	dequeuedCount       int64
	totalQueueWaitNanos int64
	maxQueueWaitNanos   int64
	startUnixNano       int64
	lastEnqueueUnixNano atomic.Int64
	lastDequeueUnixNano atomic.Int64
}

// NewWorkivaQueue creates a new Workiva-inspired queue
func NewWorkivaQueue(initialCapacity int) *WorkivaQueue {
	return &WorkivaQueue{
		items:         make([]jobs.Job, 0, initialCapacity),
		startUnixNano: time.Now().UnixNano(),
	}
}

// Enqueue adds a single job to the queue
func (q *WorkivaQueue) Enqueue(job jobs.Job) error {
	if q.isDisposed() {
		return ErrQueueClosed
	}

	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isDisposed() {
		return ErrQueueClosed
	}

	q.items = append(q.items, job)
	q.enqueuedCount++
	q.lastEnqueueUnixNano.Store(now.UnixNano())

	return nil
}

// EnqueueBatch adds a batch of jobs to the queue
func (q *WorkivaQueue) EnqueueBatch(jobsInterface []interface{}) error {
	if len(jobsInterface) == 0 {
		return nil
	}

	if q.isDisposed() {
		return ErrQueueClosed
	}

	// Convert interface{} slice to jobs.Job slice
	convertedJobs := make([]jobs.Job, 0, len(jobsInterface))
	for _, job := range jobsInterface {
		if j, ok := job.(jobs.Job); ok {
			convertedJobs = append(convertedJobs, j)
		} else {
			return errors.New("invalid job type in batch")
		}
	}

	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isDisposed() {
		return ErrQueueClosed
	}

	q.items = append(q.items, convertedJobs...)
	q.enqueuedCount += int64(len(convertedJobs))
	q.lastEnqueueUnixNano.Store(now.UnixNano())

	return nil
}

// Dequeue removes and returns a single job from the queue
func (q *WorkivaQueue) Dequeue() (jobs.Job, error) {
	if q.isDisposed() && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isDisposed() {
		return nil, ErrQueueClosed
	}

	if len(q.items) == 0 {
		return nil, nil // Queue is empty
	}

	job := q.items[0]
	// Remove first element by slicing
	q.items = q.items[1:]

	q.dequeuedCount++
	now := time.Now()
	q.lastDequeueUnixNano.Store(now.UnixNano())

	// Track wait time if job has enqueue time
	if job != nil {
		enqueueTime := job.GetEnqueueTime()
		if !enqueueTime.IsZero() {
			wait := now.Sub(enqueueTime)
			q.updateWaitMetrics(wait)
		}
	}

	return job, nil
}

// DequeueBatch removes and returns a batch of jobs from the queue
func (q *WorkivaQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if q.isDisposed() && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isDisposed() {
		return nil, ErrQueueClosed
	}

	if len(q.items) == 0 {
		return nil, nil // Queue is empty
	}

	batchSize := len(q.items)
	if batchSize > maxSize {
		batchSize = maxSize
	}

	batch := make([]jobs.Job, batchSize)
	copy(batch, q.items[:batchSize])

	// Remove batched items
	q.items = q.items[batchSize:]

	q.dequeuedCount += int64(batchSize)
	now := time.Now()
	q.lastDequeueUnixNano.Store(now.UnixNano())

	// Track wait times for batched jobs
	for _, job := range batch {
		if job != nil {
			enqueueTime := job.GetEnqueueTime()
			if !enqueueTime.IsZero() {
				wait := now.Sub(enqueueTime)
				q.updateWaitMetrics(wait)
			}
		}
	}

	return batch, nil
}

// IsEmpty checks if the queue is empty
func (q *WorkivaQueue) IsEmpty() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items) == 0
}

// Close marks the queue as closed
func (q *WorkivaQueue) Close() {
	atomic.StoreInt32(&q.disposed, 1)
}

// Stats returns the current statistics for the queue
func (q *WorkivaQueue) Stats() Stats {
	q.mu.RLock()
	queueDepth := len(q.items)
	capacity := cap(q.items)
	q.mu.RUnlock()

	enq := atomic.LoadInt64(&q.enqueuedCount)
	deq := atomic.LoadInt64(&q.dequeuedCount)
	elapsed := time.Since(time.Unix(0, q.startUnixNano))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	avgWaitNs := int64(0)
	if deq > 0 {
		avgWaitNs = atomic.LoadInt64(&q.totalQueueWaitNanos) / deq
	}

	// WorkivaQueue is intentionally unbounded; once the slice is drained Go reports a
	// zero capacity even though future appends grow it without blocking. Expose a
	// sentinel capacity so callers can detect the unbounded behaviour instead of
	// treating the queue as full.
	if capacity == 0 {
		capacity = -1
	}

	return Stats{
		QueueDepth:   queueDepth,
		Capacity:     capacity,
		Enqueued:     enq,
		Dequeued:     deq,
		Dropped:      0,
		MaxQueueTime: time.Duration(atomic.LoadInt64(&q.maxQueueWaitNanos)),
		AvgQueueTime: time.Duration(avgWaitNs),
		EnqueueRate:  float64(enq) / elapsed.Seconds(),
		DequeueRate:  float64(deq) / elapsed.Seconds(),
		LastEnqueue:  time.Unix(0, q.lastEnqueueUnixNano.Load()),
		LastDequeue:  time.Unix(0, q.lastDequeueUnixNano.Load()),
		SampleWindow: elapsed,
	}
}

// Helper methods

func (q *WorkivaQueue) isDisposed() bool {
	return atomic.LoadInt32(&q.disposed) == 1
}

func (q *WorkivaQueue) updateWaitMetrics(wait time.Duration) {
	waitNs := int64(wait)
	atomic.AddInt64(&q.totalQueueWaitNanos, waitNs)

	// Update max wait time atomically
	for {
		currentMax := atomic.LoadInt64(&q.maxQueueWaitNanos)
		if waitNs <= currentMax {
			break
		}
		if atomic.CompareAndSwapInt64(&q.maxQueueWaitNanos, currentMax, waitNs) {
			break
		}
	}
}
