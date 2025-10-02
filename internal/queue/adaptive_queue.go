package queue

import (
	"cpra/internal/jobs"
	"errors"
	"reflect"
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
	for {
		head := q.head.Load()
		tail := q.tail.Load()

		if tail-head >= q.capacity.Load() {
			return ErrQueueFull // Queue is full
		}

		// Attempt to claim the next spot
		if q.tail.CompareAndSwap(tail, tail+1) {
			if !isNilJob(job) {
				job.SetEnqueueTime(now)
			}
			q.buffer[tail&(q.capacity.Load()-1)] = job
			q.enqueuedCount.Add(1)
			q.lastEnqueueUnixNano.Store(now.UnixNano())
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
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}
	n := uint64(len(convertedJobs))

	now := time.Now()
	for {
		head := q.head.Load()
		tail := q.tail.Load()

		if tail-head+n > q.capacity.Load() {
			return ErrQueueFull
		}

		// Atomically claim a slot for the entire batch
		if q.tail.CompareAndSwap(tail, tail+n) {
			// Once the slot is claimed, we can write the batch without further atomics
			for i := uint64(0); i < n; i++ {
				job := convertedJobs[i]
				if !isNilJob(job) {
					job.SetEnqueueTime(now)
				}
				q.buffer[(tail+i)&(q.capacity.Load()-1)] = job
			}
			q.enqueuedCount.Add(int64(n))
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			return nil
		}
		// If CAS fails, another producer got there first. Loop and try again.
	}
}

// DequeueBatch removes and returns a batch of jobs from the queue.
func (q *AdaptiveQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

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

		// Atomically claim the batch for dequeuing
		if q.head.CompareAndSwap(head, head+n) {
			batch := make([]jobs.Job, n)
			now := time.Now()
			for i := uint64(0); i < n; i++ {
				batch[i] = q.buffer[(head+i)&(q.capacity.Load()-1)]
				// Nil out the buffer slot to help the GC
				q.buffer[(head+i)&(q.capacity.Load()-1)] = nil

				if !isNilJob(batch[i]) {
					enqueueTime := batch[i].GetEnqueueTime()
					if !enqueueTime.IsZero() {
						wait := now.Sub(enqueueTime)
						q.totalQueueWaitNanos.Add(int64(wait))
						for {
							currentMax := q.maxQueueWaitNanos.Load()
							if waitNs := int64(wait); waitNs <= currentMax {
								break
							}
							if q.maxQueueWaitNanos.CompareAndSwap(currentMax, int64(wait)) {
								break
							}
						}
					}
				}
			}
			q.dequeuedCount.Add(int64(n))
			q.lastDequeueUnixNano.Store(now.UnixNano())
			return batch, nil
		}
		// If CAS fails, another consumer got there first. Loop and try again.
	}
}

// Dequeue removes and returns a single job from the queue.
func (q *AdaptiveQueue) Dequeue() (jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}

	for {
		head := q.head.Load()
		tail := q.tail.Load()

		if head >= tail {
			return nil, nil // Queue is empty
		}

		job := q.buffer[head&(q.capacity.Load()-1)]

		// Attempt to move the head pointer
		if q.head.CompareAndSwap(head, head+1) {
			now := time.Now()
			if !isNilJob(job) {
				enqueueTime := job.GetEnqueueTime()
				if !enqueueTime.IsZero() {
					wait := now.Sub(enqueueTime)
					q.totalQueueWaitNanos.Add(int64(wait))
					for {
						currentMax := q.maxQueueWaitNanos.Load()
						if waitNs := int64(wait); waitNs <= currentMax {
							break
						}
						if q.maxQueueWaitNanos.CompareAndSwap(currentMax, int64(wait)) {
							break
						}
					}
				}
			}
			q.dequeuedCount.Add(1)
			q.lastDequeueUnixNano.Store(now.UnixNano())
			return job, nil
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

func isNilJob(job jobs.Job) bool {
	if job == nil {
		return true
	}
	val := reflect.ValueOf(job)
	switch val.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return val.IsNil()
	default:
		return false
	}
}

// Stats returns the current statistics for the queue.
// Note: This is a simplified version. A full implementation would track more metrics.
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
