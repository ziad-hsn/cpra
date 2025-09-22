package queue

import (
	"cpra/internal/jobs"
	"errors"
	"sync/atomic"
	"time"
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

	enqueuedCount       int64
	dequeuedCount       int64
	totalQueueWaitNanos int64
	maxQueueWaitNanos   int64
	startUnixNano       int64
	lastEnqueueUnixNano int64
	lastDequeueUnixNano int64
}

// NewAdaptiveQueue creates a new AdaptiveQueue with the given capacity.
// Capacity must be a power of 2 for efficient bitwise operations.
func NewAdaptiveQueue(capacity uint64) (*AdaptiveQueue, error) {
	if (capacity & (capacity - 1)) != 0 {
		return nil, errors.New("capacity must be a power of 2")
	}
	queue := &AdaptiveQueue{
		buffer:        make([]jobs.Job, capacity),
		capacity:      capacity,
		startUnixNano: time.Now().UnixNano(),
	}
	return queue, nil
}

// Enqueue adds a single job to the queue.
func (q *AdaptiveQueue) Enqueue(job jobs.Job) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrQueueClosed
	}

	now := time.Now()
	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)

		if tail-head >= q.capacity {
			return ErrQueueFull // Queue is full
		}

		// Attempt to claim the next spot
		if atomic.CompareAndSwapUint64(&q.tail, tail, tail+1) {
			if job != nil {
				job.SetEnqueueTime(now)
			}
			q.buffer[tail&(q.capacity-1)] = job
			atomic.AddInt64(&q.enqueuedCount, 1)
			atomic.StoreInt64(&q.lastEnqueueUnixNano, now.UnixNano())
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

	now := time.Now()
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
				job := convertedJobs[i]
				if job != nil {
					job.SetEnqueueTime(now)
				}
				q.buffer[(tail+i)&(q.capacity-1)] = job
			}
			atomic.AddInt64(&q.enqueuedCount, int64(n))
			atomic.StoreInt64(&q.lastEnqueueUnixNano, now.UnixNano())
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
			now := time.Now()
			for i := uint64(0); i < n; i++ {
				batch[i] = q.buffer[(head+i)&(q.capacity-1)]
				// Nil out the buffer slot to help the GC
				q.buffer[(head+i)&(q.capacity-1)] = nil

				if batch[i] != nil {
					enqueueTime := batch[i].GetEnqueueTime()
					if !enqueueTime.IsZero() {
						wait := now.Sub(enqueueTime)
						atomic.AddInt64(&q.totalQueueWaitNanos, int64(wait))
						for {
							currentMax := atomic.LoadInt64(&q.maxQueueWaitNanos)
							if waitNs := int64(wait); waitNs <= currentMax {
								break
							}
							if atomic.CompareAndSwapInt64(&q.maxQueueWaitNanos, currentMax, int64(wait)) {
								break
							}
						}
					}
				}
			}
			atomic.AddInt64(&q.dequeuedCount, int64(n))
			atomic.StoreInt64(&q.lastDequeueUnixNano, now.UnixNano())
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
			now := time.Now()
			if job != nil {
				enqueueTime := job.GetEnqueueTime()
				if !enqueueTime.IsZero() {
					wait := now.Sub(enqueueTime)
					atomic.AddInt64(&q.totalQueueWaitNanos, int64(wait))
					for {
						currentMax := atomic.LoadInt64(&q.maxQueueWaitNanos)
						if waitNs := int64(wait); waitNs <= currentMax {
							break
						}
						if atomic.CompareAndSwapInt64(&q.maxQueueWaitNanos, currentMax, int64(wait)) {
							break
						}
					}
				}
			}
			atomic.AddInt64(&q.dequeuedCount, 1)
			atomic.StoreInt64(&q.lastDequeueUnixNano, now.UnixNano())
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
	depth := tail - head
	enq := atomic.LoadInt64(&q.enqueuedCount)
	deq := atomic.LoadInt64(&q.dequeuedCount)
	elapsed := time.Since(time.Unix(0, atomic.LoadInt64(&q.startUnixNano)))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	avgWaitNs := int64(0)
	if deq > 0 {
		avgWaitNs = atomic.LoadInt64(&q.totalQueueWaitNanos) / deq
	}
	stats := QueueStats{
		QueueDepth:   int(depth),
		Capacity:     int(q.capacity),
		Enqueued:     enq,
		Dequeued:     deq,
		Dropped:      0,
		MaxQueueTime: time.Duration(atomic.LoadInt64(&q.maxQueueWaitNanos)),
		AvgQueueTime: time.Duration(avgWaitNs),
		EnqueueRate:  float64(enq) / elapsed.Seconds(),
		DequeueRate:  float64(deq) / elapsed.Seconds(),
		LastEnqueue:  time.Unix(0, atomic.LoadInt64(&q.lastEnqueueUnixNano)),
		LastDequeue:  time.Unix(0, atomic.LoadInt64(&q.lastDequeueUnixNano)),
		SampleWindow: elapsed,
	}
	return stats
}
