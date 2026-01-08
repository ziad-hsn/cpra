package queue

import (
	"cpra/internal/runtime/jobs"
	"errors"
	"runtime"
	"sync/atomic"
	"time"
)

// AdaptiveQueue is a lock-free, thread-safe, fixed-size circular queue.
// It is designed for high-throughput scenarios with multiple producers and consumers.
// It implements the Queue interface.
type AdaptiveQueue struct {
	signal              chan struct{}
	buffer              []atomic.Value // Each slot stores jobs.Job atomically
	rollingWait         *RollingWait
	emaWait             *EMAWait
	bucketedWait        *TimeBucketedWait
	dequeuedCount       atomic.Int64
	tail                atomic.Uint64
	enqueuedCount       atomic.Int64
	head                atomic.Uint64
	totalQueueWaitNanos atomic.Int64
	maxQueueWaitNanos   atomic.Int64
	startUnixNano       atomic.Int64
	lastEnqueueUnixNano atomic.Int64
	lastDequeueUnixNano atomic.Int64
	capacity            atomic.Uint64
	closed              atomic.Int32
}

// NewAdaptiveQueue creates a new AdaptiveQueue with the given capacity.
// Capacity must be a power of 2 for efficient bitwise operations.
func NewAdaptiveQueue(capacity uint64) (*AdaptiveQueue, error) {
	if (capacity & (capacity - 1)) != 0 {
		return nil, errors.New("capacity must be a power of 2")
	}
	queue := &AdaptiveQueue{
		buffer: make([]atomic.Value, capacity),
		signal: make(chan struct{}, 1),
	}
	queue.startUnixNano.Store(time.Now().UnixNano())
	queue.capacity.Store(capacity)
	queue.rollingWait = NewRollingWait(defaultRollingWaitSize)
	queue.emaWait = NewEMAWait(0.2)
	queue.bucketedWait = NewTimeBucketedWait(time.Minute, 10)
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
			q.buffer[tail&(capacity-1)].Store(job)
			q.enqueuedCount.Add(1)
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			q.notify()
			return nil
		}
		// CAS failed - another producer got there first, use progressive backoff
		// to reduce CPU burn while maintaining responsiveness
		backoff = adaptiveBackoff(backoff, maxBackoff)
	}
}

// EnqueueBatch adds a batch of jobs to the queue using a highly concurrent, lock-free algorithm.
func (q *AdaptiveQueue) EnqueueBatch(jobsInterface []interface{}) error {
	if len(jobsInterface) == 0 {
		return nil
	}

	// Convert interface{} slice to jobs.Job slice, skipping nils
	convertedJobs := make([]jobs.Job, 0, len(jobsInterface))
	for _, job := range jobsInterface {
		if job == nil {
			continue
		}
		if j, ok := job.(jobs.Job); ok {
			convertedJobs = append(convertedJobs, j)
		} else {
			return errors.New("invalid job type in batch")
		}
	}
	if len(convertedJobs) == 0 {
		return nil
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
				q.buffer[(tail+i)&mask].Store(job)
			}
			q.enqueuedCount.Add(int64(n))
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			q.notify()
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
	if maxSize <= 0 {
		return nil, nil
	}

	batch := make([]jobs.Job, 0, maxSize)
	for i := 0; i < maxSize; i++ {
		job, err := q.Dequeue()
		if err != nil {
			// If queue closed mid-drain, return what we have
			if errors.Is(err, ErrQueueClosed) && len(batch) > 0 {
				return batch, nil
			}
			return batch, err
		}
		if job == nil {
			break
		}
		batch = append(batch, job)
	}
	return batch, nil
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
		idx := head & (capacity - 1)
		jobVal := q.buffer[idx].Load()
		if jobVal == nil {
			runtime.Gosched()
			continue
		}
		job := jobVal.(jobs.Job)
		if job.IsNil() {
			runtime.Gosched()
			continue
		}
		newHead := head + 1
		if q.head.CompareAndSwap(head, newHead) {
			now := time.Now()
			enqueueTime := job.GetEnqueueTime()
			if !enqueueTime.IsZero() {
				wait := now.Sub(enqueueTime)
				q.totalQueueWaitNanos.Add(int64(wait))
				if wait > 0 {
					if q.rollingWait != nil {
						q.rollingWait.Record(wait)
					}
					if q.emaWait != nil {
						q.emaWait.Record(wait)
					}
					if q.bucketedWait != nil {
						q.bucketedWait.Record(wait)
					}
				}
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

	var rollingAvg, rollingP50, rollingP95 time.Duration
	var rollingSamples int
	if q.rollingWait != nil {
		rollingAvg, rollingP50, rollingP95, rollingSamples = q.rollingWait.Snapshot()
	}

	var emaWait time.Duration
	if q.emaWait != nil {
		emaWait = q.emaWait.Value()
	}

	var bucketAvg, bucketMax time.Duration
	var bucketCnt int64
	if q.bucketedWait != nil {
		bucketAvg, bucketMax, bucketCnt = q.bucketedWait.RecentStats(1)
	}

	stats := Stats{
		QueueDepth:     int(depth),
		Capacity:       int(q.capacity.Load()),
		Enqueued:       enq,
		Dequeued:       deq,
		Dropped:        0,
		MaxQueueTime:   time.Duration(q.maxQueueWaitNanos.Load()),
		AvgQueueTime:   time.Duration(avgWaitNs),
		EnqueueRate:    float64(enq) / elapsed.Seconds(),
		DequeueRate:    float64(deq) / elapsed.Seconds(),
		LastEnqueue:    time.Unix(0, q.lastEnqueueUnixNano.Load()),
		LastDequeue:    time.Unix(0, q.lastDequeueUnixNano.Load()),
		SampleWindow:   elapsed,
		RollingAvgWait: rollingAvg,
		RollingP50Wait: rollingP50,
		RollingP95Wait: rollingP95,
		RollingSamples: rollingSamples,
		EMAWait:        emaWait,
		BucketAvg1m:    bucketAvg,
		BucketMax1m:    bucketMax,
		BucketCnt1m:    bucketCnt,
	}
	return stats
}

// EnsureCapacity is a no-op for AdaptiveQueue as it has a fixed capacity.
func (q *AdaptiveQueue) EnsureCapacity(targetCap int) {
	// No-op: AdaptiveQueue has fixed capacity set at construction
}

func (q *AdaptiveQueue) Notify() <-chan struct{} {
	return q.signal
}

func (q *AdaptiveQueue) notify() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// adaptiveBackoff implements a progressive backoff strategy that starts with
// fast spins (Gosched) and progressively moves to sleeps when contention persists.
// This reduces CPU burn during high contention while maintaining responsiveness
// when contention is brief.
//
// Backoff progression:
//   - attempts 1-4: runtime.Gosched() (fast spin, ~tens of nanoseconds)
//   - attempts 5-8: 1 microsecond sleep (transition to sleep)
//   - attempts 9+:  10 microsecond sleep (reduced CPU burn)
func adaptiveBackoff(backoff, maxBackoff uint64) uint64 {
	switch {
	case backoff < 4:
		// Fast path: brief Gosched for transient contention
		runtime.Gosched()
	case backoff < 8:
		// Transition: double Gosched + yield
		runtime.Gosched()
		runtime.Gosched()
	case backoff < 16:
		// Sleep path: 1 microsecond for moderate contention
		time.Sleep(time.Microsecond)
	default:
		// Sustained contention: 10 microseconds to reduce CPU burn
		time.Sleep(10 * time.Microsecond)
	}

	// Exponential backoff until maxBackoff
	if backoff < maxBackoff {
		return backoff << 1
	}
	return maxBackoff
}
