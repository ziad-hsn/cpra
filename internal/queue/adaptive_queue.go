package queue

import (
	"cpra/internal/jobs"
	"errors"
	"runtime"
	"sync/atomic"
	"time"
)

// AdaptiveQueue is a lock-free, thread-safe, fixed-size circular queue.
// It is designed for high-throughput scenarios with multiple producers and consumers.
// It implements the Queue interface.
type AdaptiveQueue struct {
	buffer   []jobs.Job
	capacity atomic.Uint64
	head     atomic.Uint64
	tail     atomic.Uint64
	closed   atomic.Int32

	enqueuedCount       atomic.Int64
	dequeuedCount       atomic.Int64
	totalQueueWaitNanos atomic.Int64
	maxQueueWaitNanos   atomic.Int64
	startUnixNano       atomic.Int64
	lastEnqueueUnixNano atomic.Int64
	lastDequeueUnixNano atomic.Int64
}

// NewAdaptiveQueue creates a new AdaptiveQueue with the given capacity.
// Capacity must be a power of 2 for efficient bitwise operations.
func NewAdaptiveQueue(capacity uint64) (*AdaptiveQueue, error) {
	if (capacity & (capacity - 1)) != 0 {
		return nil, errors.New("capacity must be a power of 2")
	}
	queue := &AdaptiveQueue{
		buffer: make([]jobs.Job, capacity),
	}
	queue.startUnixNano.Store(time.Now().UnixNano())
	queue.capacity.Store(capacity)
	return queue, nil
}

// Enqueue adds a single job to the queue.
func (q *AdaptiveQueue) Enqueue(job jobs.Job) error {
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}

	now := time.Now()
	capacity := q.capacity.Load() // Cache capacity to avoid repeated loads
	backoff := uint64(1)
	maxBackoff := uint64(1024)

	for {
		head := q.head.Load()
		tail := q.tail.Load()

		// Check capacity before attempting CAS to avoid unnecessary operations
		if tail-head >= capacity {
			return ErrQueueFull // Queue is full
		}

		// Attempt to claim the next spot
		newTail := tail + 1
		if q.tail.CompareAndSwap(tail, newTail) {
			if !isNilJob(job) {
				job.SetEnqueueTime(now)
			}
			q.buffer[tail&(capacity-1)] = job
			q.enqueuedCount.Add(1)
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			return nil
		}
		// CAS failed - another producer got there first, use exponential backoff
		if backoff < maxBackoff {
			for i := uint64(0); i < backoff; i++ {
				runtime.Gosched()
			}
			backoff <<= 1
		} else {
			runtime.Gosched()
		}
	}
}

// EnqueueBatch adds a batch of jobs to the queue using a highly concurrent, lock-free algorithm.
func (q *AdaptiveQueue) EnqueueBatch(jobsInterface []interface{}) error {
	if len(jobsInterface) == 0 {
		return nil
	}

	// Convert interface{} slice to jobs.Job slice
	convertedJobs := make([]jobs.Job, len(jobsInterface))
	for i, job := range jobsInterface {
		if j, ok := job.(jobs.Job); ok {
			convertedJobs[i] = j
		} else {
			return errors.New("invalid job type in batch")
		}
	}
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}
	n := uint64(len(convertedJobs))

	now := time.Now()
	capacity := q.capacity.Load() // Cache capacity
	backoff := uint64(1)
	maxBackoff := uint64(1024)

	for {
		head := q.head.Load()
		tail := q.tail.Load()
		available := capacity - (tail - head)
		if available < n {
			return ErrQueueFull
		}
		// Atomically claim slots for the entire batch
		newTail := tail + n
		if q.tail.CompareAndSwap(tail, newTail) {
			// Once slots are claimed atomically, we can safely write
			mask := capacity - 1
			for i := uint64(0); i < n; i++ {
				job := convertedJobs[i]
				if !isNilJob(job) {
					job.SetEnqueueTime(now)
				}
				q.buffer[(tail+i)&mask] = job
			}
			q.enqueuedCount.Add(int64(n))
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			return nil
		}
		// CAS failed - use exponential backoff
		if backoff < maxBackoff {
			for i := uint64(0); i < backoff; i++ {
				runtime.Gosched()
			}
			backoff <<= 1
		} else {
			runtime.Gosched()
		}
	}
}

// DequeueBatch removes and returns a batch of jobs from the queue.
func (q *AdaptiveQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	capacity := q.capacity.Load() // Cache capacity
	backoff := uint64(1)
	maxBackoff := uint64(1024)

	for {
		head := q.head.Load()
		tail := q.tail.Load()
		if head >= tail {
			return nil, nil // Queue is empty
		}
		n := tail - head
		if n > uint64(maxSize) {
			n = uint64(maxSize)
		}
		// Atomically claim batch for dequeuing
		newHead := head + n
		if q.head.CompareAndSwap(head, newHead) {
			batch := make([]jobs.Job, n)
			now := time.Now()
			mask := capacity - 1
			for i := uint64(0); i < n; i++ {
				idx := (head + i) & mask
				batch[i] = q.buffer[idx]
				q.buffer[idx] = nil // Help GC
				enqueueTime := batch[i].GetEnqueueTime()
				if !enqueueTime.IsZero() {
					wait := now.Sub(enqueueTime)
					q.totalQueueWaitNanos.Add(int64(wait))
					for {
						currentMax := q.maxQueueWaitNanos.Load()
						if int64(wait) <= currentMax {
							break
						}
						if q.maxQueueWaitNanos.CompareAndSwap(currentMax, int64(wait)) {
							break
						}
					}
				}
			}
			q.dequeuedCount.Add(int64(n))
			q.lastDequeueUnixNano.Store(now.UnixNano())
			return batch, nil
		}
		// CAS failed - backoff
		if backoff < maxBackoff {
			for i := uint64(0); i < backoff; i++ {
				runtime.Gosched()
			}
			backoff <<= 1
		} else {
			runtime.Gosched()
		}
	}
}

// Dequeue removes and returns a single job from the queue.
func (q *AdaptiveQueue) Dequeue() (jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	capacity := q.capacity.Load()
	backoff := uint64(1)
	maxBackoff := uint64(1024)

	for {
		head := q.head.Load()
		tail := q.tail.Load()
		if head >= tail {
			return nil, nil // Queue is empty
		}
		job := q.buffer[head&(capacity-1)]
		newHead := head + 1
		if q.head.CompareAndSwap(head, newHead) {
			now := time.Now()
			enqueueTime := job.GetEnqueueTime()
			if !enqueueTime.IsZero() {
				wait := now.Sub(enqueueTime)
				q.totalQueueWaitNanos.Add(int64(wait))
				for {
					currentMax := q.maxQueueWaitNanos.Load()
					if int64(wait) <= currentMax {
						break
					}
					if q.maxQueueWaitNanos.CompareAndSwap(currentMax, int64(wait)) {
						break
					}
				}
			}
			q.dequeuedCount.Add(1)
			q.lastDequeueUnixNano.Store(now.UnixNano())
			return job, nil
		}
		if backoff < maxBackoff {
			for i := uint64(0); i < backoff; i++ {
				runtime.Gosched()
			}
			backoff <<= 1
		} else {
			runtime.Gosched()
		}
	}
}

// IsEmpty checks if the queue is empty.
func (q *AdaptiveQueue) IsEmpty() bool {
	return q.head.Load() == q.tail.Load()
}

// Close marks the queue as closed.
func (q *AdaptiveQueue) Close() {
	q.closed.Store(1)
}

func isNilJob(job jobs.Job) bool { return job == nil || job.IsNil() }

// Stats returns the current statistics for the queue.
func (q *AdaptiveQueue) Stats() Stats {
	head := q.head.Load()
	tail := q.tail.Load()
	depth := tail - head
	enq := q.enqueuedCount.Load()
	deq := q.dequeuedCount.Load()
	elapsed := time.Since(time.Unix(0, q.startUnixNano.Load()))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	avgWaitNs := int64(0)
	if deq > 0 {
		avgWaitNs = q.totalQueueWaitNanos.Load() / deq
	}
	stats := Stats{
		QueueDepth:   int(depth),
		Capacity:     int(q.capacity.Load()),
		Enqueued:     enq,
		Dequeued:     deq,
		Dropped:      0,
		MaxQueueTime: time.Duration(q.maxQueueWaitNanos.Load()),
		AvgQueueTime: time.Duration(avgWaitNs),
		EnqueueRate:  float64(enq) / elapsed.Seconds(),
		DequeueRate:  float64(deq) / elapsed.Seconds(),
		LastEnqueue:  time.Unix(0, q.lastEnqueueUnixNano.Load()),
		LastDequeue:  time.Unix(0, q.lastDequeueUnixNano.Load()),
		SampleWindow: elapsed,
	}
	return stats
}

// EnsureCapacity is a no-op for AdaptiveQueue as it has a fixed capacity.
func (q *AdaptiveQueue) EnsureCapacity(targetCap int) {
	// No-op: AdaptiveQueue has fixed capacity set at construction
}
