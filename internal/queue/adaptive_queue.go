package queue

import (
	"cpra/internal/jobs"
	"errors"
	"sync/atomic"
)

// AdaptiveQueue is a lock-free, thread-safe, fixed-size circular queue.
// It is designed for high-throughput scenarios with multiple producers and consumers.
// It implements the Queue interface.
type AdaptiveQueue struct {
	buffer   []jobs.Job
	capacity uint64
	head     uint64
	tail     uint64
	closed   int32
}

// NewAdaptiveQueue creates a new AdaptiveQueue with the given capacity.
// Capacity must be a power of 2 for efficient bitwise operations.
func NewAdaptiveQueue(capacity uint64) (*AdaptiveQueue, error) {
	if (capacity & (capacity - 1)) != 0 {
		return nil, errors.New("capacity must be a power of 2")
	}
	return &AdaptiveQueue{
		buffer:   make([]jobs.Job, capacity),
		capacity: capacity,
	}, nil
}

// Enqueue adds a single job to the queue.
func (q *AdaptiveQueue) Enqueue(job jobs.Job) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}

	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)

		if tail-head >= q.capacity {
			return ErrQueueFull // Queue is full
		}

		// Attempt to claim the next spot
		if atomic.CompareAndSwapUint64(&q.tail, tail, tail+1) {
			q.buffer[tail& (q.capacity-1)] = job
			return nil
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
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}
	n := uint64(len(convertedJobs))

	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)

		if tail-head+n > q.capacity {
			return ErrQueueFull
		}

		// Atomically claim a slot for the entire batch
		if atomic.CompareAndSwapUint64(&q.tail, tail, tail+n) {
			// Once the slot is claimed, we can write the batch without further atomics
			for i := uint64(0); i < n; i++ {
				q.buffer[(tail+i)&(q.capacity-1)] = convertedJobs[i]
			}
			return nil
		}
		// If CAS fails, another producer got there first. Loop and try again.
	}
}

// DequeueBatch removes and returns a batch of jobs from the queue.
func (q *AdaptiveQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if atomic.LoadInt32(&q.closed) == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)

		if head >= tail {
			return nil, nil // Queue is empty
		}

		n := tail - head
		if n > uint64(maxSize) {
			n = uint64(maxSize)
		}

		// Atomically claim the batch for dequeuing
		if atomic.CompareAndSwapUint64(&q.head, head, head+n) {
			batch := make([]jobs.Job, n)
			for i := uint64(0); i < n; i++ {
				batch[i] = q.buffer[(head+i)&(q.capacity-1)]
				// Nil out the buffer slot to help the GC
				q.buffer[(head+i)&(q.capacity-1)] = nil
			}
			return batch, nil
		}
		// If CAS fails, another consumer got there first. Loop and try again.
	}
}

// Dequeue removes and returns a single job from the queue.
func (q *AdaptiveQueue) Dequeue() (jobs.Job, error) {
	if atomic.LoadInt32(&q.closed) == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)

		if head >= tail {
			return nil, nil // Queue is empty
		}

		job := q.buffer[head&(q.capacity-1)]

		// Attempt to move the head pointer
		if atomic.CompareAndSwapUint64(&q.head, head, head+1) {
			return job, nil
		}
	}
}

// IsEmpty checks if the queue is empty.
func (q *AdaptiveQueue) IsEmpty() bool {
	return atomic.LoadUint64(&q.head) == atomic.LoadUint64(&q.tail)
}

// Close marks the queue as closed.
func (q *AdaptiveQueue) Close() {
	atomic.StoreInt32(&q.closed, 1)
}

// Stats returns the current statistics for the queue.
// Note: This is a simplified version. A full implementation would track more metrics.
func (q *AdaptiveQueue) Stats() QueueStats {
	head := atomic.LoadUint64(&q.head)
	tail := atomic.LoadUint64(&q.tail)
	return QueueStats{
		QueueDepth: int(tail - head),
	}
}
