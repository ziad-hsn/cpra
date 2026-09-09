package queue

import (
	"cpra/internal/jobs"
	"errors"
	"runtime"
	"sync/atomic"
	"time"
)

// AdaptiveQueue is a lock-free, thread-safe, fixed-size circular queue using
// the Vyukov bounded MPMC algorithm. Each cell carries a sequence number that
// establishes a happens-before edge between the producer's write and the
// consumer's read, so a consumer can never observe a slot before its job has
// been published (the previous implementation advanced tail before writing the
// slot, which could hand a nil/stale job to a concurrent consumer).
type AdaptiveQueue struct {
	arrivals   durationMetrics
	buffer     []adaptiveCell
	mask       uint64
	capacity   atomic.Uint64
	enqueuePos atomic.Uint64
	dequeuePos atomic.Uint64
	closed     atomic.Int32

	enqueuedCount       atomic.Int64
	dequeuedCount       atomic.Int64
	totalQueueWaitNanos atomic.Int64
	maxQueueWaitNanos   atomic.Int64
	startUnixNano       atomic.Int64
	lastEnqueueUnixNano atomic.Int64
	lastDequeueUnixNano atomic.Int64
}

// adaptiveCell is a single slot in the ring buffer. sequence is the Vyukov
// per-cell sequence number: a cell is writable when sequence == pos, readable
// when sequence == pos+1, and free for the next wrap when sequence == pos+mask+1.
type adaptiveCell struct {
	sequence atomic.Uint64
	job      jobs.Job
}

// NewAdaptiveQueue creates a new AdaptiveQueue with the given capacity.
// Capacity must be a power of 2 and at least 2.
func NewAdaptiveQueue(capacity uint64) (*AdaptiveQueue, error) {
	if capacity < 2 || (capacity&(capacity-1)) != 0 {
		return nil, errors.New("capacity must be a power of 2 and at least 2")
	}
	q := &AdaptiveQueue{
		buffer: make([]adaptiveCell, capacity),
		mask:   capacity - 1,
	}
	// Initialize each cell's sequence to its index so it is writable at
	// position i on the first wrap.
	for i := uint64(0); i < capacity; i++ {
		q.buffer[i].sequence.Store(i)
	}
	q.startUnixNano.Store(time.Now().UnixNano())
	q.capacity.Store(capacity)
	return q, nil
}

// Enqueue adds a single job to the queue.
func (q *AdaptiveQueue) Enqueue(job jobs.Job) error {
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}
	now := time.Now()
	pos := q.enqueuePos.Load()
	for {
		cell := &q.buffer[pos&q.mask]
		seq := cell.sequence.Load()
		dif := int64(seq) - int64(pos)
		if dif == 0 {
			// Cell is writable at this position.
			if q.enqueuePos.CompareAndSwap(pos, pos+1) {
				if !isNilJob(job) {
					job.SetEnqueueTime(now)
				}
				cell.job = job
				// Publish: make the job visible to consumers.
				cell.sequence.Store(pos + 1)
				q.enqueuedCount.Add(1)
				q.arrivals.recordArrival()
				q.lastEnqueueUnixNano.Store(now.UnixNano())
				return nil
			}
		} else if dif < 0 {
			// Queue is full.
			return ErrQueueFull
		} else {
			// Another producer advanced enqueuePos; reload.
			pos = q.enqueuePos.Load()
		}
		runtime.Gosched()
	}
}

// EnqueueBatch adds a batch of jobs to the queue.
func (q *AdaptiveQueue) EnqueueBatch(jobsInterface []interface{}) error {
	if len(jobsInterface) == 0 {
		return nil
	}
	for _, j := range jobsInterface {
		job, ok := j.(jobs.Job)
		if !ok {
			return errors.New("invalid job type in batch")
		}
		if err := q.Enqueue(job); err != nil {
			return err
		}
	}
	return nil
}

// Dequeue removes and returns a single job from the queue.
func (q *AdaptiveQueue) Dequeue() (jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}
	pos := q.dequeuePos.Load()
	for {
		cell := &q.buffer[pos&q.mask]
		seq := cell.sequence.Load()
		dif := int64(seq) - int64(pos+1)
		if dif == 0 {
			// Cell is readable at this position.
			if q.dequeuePos.CompareAndSwap(pos, pos+1) {
				job := cell.job
				cell.job = nil // help GC
				// Free the cell for the next wrap.
				cell.sequence.Store(pos + q.mask + 1)
				now := time.Now()
				if job != nil {
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
				}
				q.dequeuedCount.Add(1)
				q.lastDequeueUnixNano.Store(now.UnixNano())
				return job, nil
			}
		} else if dif < 0 {
			// Queue is empty.
			return nil, nil
		} else {
			// Another consumer advanced dequeuePos; reload.
			pos = q.dequeuePos.Load()
		}
		runtime.Gosched()
	}
}

// DequeueBatch removes and returns a batch of jobs from the queue.
func (q *AdaptiveQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}
	batch := make([]jobs.Job, 0, maxSize)
	for i := 0; i < maxSize; i++ {
		job, err := q.Dequeue()
		if err != nil {
			if len(batch) > 0 {
				return batch, nil
			}
			return nil, err
		}
		if job == nil {
			break
		}
		batch = append(batch, job)
	}
	return batch, nil
}

// IsEmpty checks if the queue is empty.
func (q *AdaptiveQueue) IsEmpty() bool {
	return q.enqueuePos.Load() == q.dequeuePos.Load()
}

// Close marks the queue as closed.
func (q *AdaptiveQueue) Close() {
	q.closed.Store(1)
}

func isNilJob(job jobs.Job) bool { return job == nil || job.IsNil() }

// Stats returns the current statistics for the queue.
func (q *AdaptiveQueue) Stats() Stats {
	_, arrivalCV, arrivalSamples := q.arrivals.snapshot()
	enq := q.enqueuePos.Load()
	deq := q.dequeuePos.Load()
	depth := enq - deq
	enqueued := q.enqueuedCount.Load()
	dequeued := q.dequeuedCount.Load()
	elapsed := time.Since(time.Unix(0, q.startUnixNano.Load()))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	avgWaitNs := int64(0)
	if dequeued > 0 {
		avgWaitNs = q.totalQueueWaitNanos.Load() / dequeued
	}
	return Stats{
		ArrivalCV: arrivalCV, ArrivalSamples: arrivalSamples,
		QueueDepth:   int(depth),
		Capacity:     int(q.capacity.Load()),
		Enqueued:     enqueued,
		Dequeued:     dequeued,
		Dropped:      0,
		MaxQueueTime: time.Duration(q.maxQueueWaitNanos.Load()),
		AvgQueueTime: time.Duration(avgWaitNs),
		EnqueueRate:  float64(enqueued) / elapsed.Seconds(),
		DequeueRate:  float64(dequeued) / elapsed.Seconds(),
		LastEnqueue:  time.Unix(0, q.lastEnqueueUnixNano.Load()),
		LastDequeue:  time.Unix(0, q.lastDequeueUnixNano.Load()),
		SampleWindow: elapsed,
	}
}

// EnsureCapacity is a no-op for AdaptiveQueue as it has a fixed capacity.
func (q *AdaptiveQueue) EnsureCapacity(targetCap int) {
	// No-op: AdaptiveQueue has fixed capacity set at construction
}
