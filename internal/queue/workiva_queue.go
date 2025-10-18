package queue

import (
	"cpra/internal/jobs"
	"errors"
	wqueue "github.com/Workiva/go-datastructures/queue"
	"runtime"
	"sync/atomic"
	"time"
)

// Wrapper built on Workiva's lock-free RingBuffer, with capacity expansion.
// Uses linked RingBuffer segments; producers expand when current segment is full.

var (
	errNilJobType = errors.New("invalid job type in batch")
)

type rbSeg struct {
	rb   *wqueue.RingBuffer
	next atomic.Pointer[rbSeg]
	cap  uint64
}

// WorkivaQueue is a lock-free, capacity-expanding MPMC queue using Workiva RBs.
type WorkivaQueue struct {
	head atomic.Pointer[rbSeg]
	tail atomic.Pointer[rbSeg]

	closed atomic.Int32

	// metrics
	capacity            atomic.Uint64 // cumulative capacity across segments
	enqueuedCount       atomic.Int64
	dequeuedCount       atomic.Int64
	totalQueueWaitNanos atomic.Int64
	maxQueueWaitNanos   atomic.Int64
	startUnixNano       atomic.Int64
	lastEnqueueUnixNano atomic.Int64
	lastDequeueUnixNano atomic.Int64
}

// NewWorkivaQueue creates a new expanding queue backed by Workiva RingBuffers.
func NewWorkivaQueue(capacity int) Queue {
	if capacity < 1 {
		capacity = 1
	}
	rb := wqueue.NewRingBuffer(uint64(capacity))
	seg := &rbSeg{rb: rb, cap: rb.Cap()}
	q := &WorkivaQueue{}
	q.head.Store(seg)
	q.tail.Store(seg)
	q.capacity.Store(seg.cap)
	q.startUnixNano.Store(time.Now().UnixNano())
	return q
}

// Enqueue adds a single job; expands capacity if the current segment is full.
func (q *WorkivaQueue) Enqueue(job jobs.Job) error {
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}
	now := time.Now()
	if !isNilJob(job) {
		job.SetEnqueueTime(now)
	}

	for {
		if q.closed.Load() == 1 {
			return ErrQueueClosed
		}
		tail := q.tail.Load()
		if ok, err := tail.rb.Offer(job); err != nil {
			return err
		} else if ok {
			q.enqueuedCount.Add(1)
			q.lastEnqueueUnixNano.Store(now.UnixNano())
			return nil
		}
		// Full: attempt to expand by linking a larger segment
		if next := tail.next.Load(); next == nil {
			newRB := wqueue.NewRingBuffer(tail.cap << 1)
			newSeg := &rbSeg{rb: newRB, cap: newRB.Cap()}
			if tail.next.CompareAndSwap(nil, newSeg) {
				q.capacity.Add(newSeg.cap)
				q.tail.CompareAndSwap(tail, newSeg)
			}
		} else {
			q.tail.CompareAndSwap(tail, next)
		}
		runtime.Gosched()
	}
}

// EnqueueBatch enqueues a slice of jobs, expanding as needed.
func (q *WorkivaQueue) EnqueueBatch(items []interface{}) error {
	if len(items) == 0 {
		return nil
	}
	if q.closed.Load() == 1 {
		return ErrQueueClosed
	}
	now := time.Now()
	batch := make([]jobs.Job, len(items))
	for i, it := range items {
		j, ok := it.(jobs.Job)
		if !ok {
			return errNilJobType
		}
		if !isNilJob(j) {
			j.SetEnqueueTime(now)
		}
		batch[i] = j
	}

	enq := int64(0)
	for i := range batch {
		for {
			if q.closed.Load() == 1 {
				return ErrQueueClosed
			}
			tail := q.tail.Load()
			if ok, err := tail.rb.Offer(batch[i]); err != nil {
				return err
			} else if ok {
				enq++
				break
			}
			if next := tail.next.Load(); next == nil {
				newRB := wqueue.NewRingBuffer(tail.cap << 1)
				newSeg := &rbSeg{rb: newRB, cap: newRB.Cap()}
				if tail.next.CompareAndSwap(nil, newSeg) {
					q.capacity.Add(newSeg.cap)
					q.tail.CompareAndSwap(tail, newSeg)
				}
			} else {
				q.tail.CompareAndSwap(tail, next)
			}
			runtime.Gosched()
		}
	}
	if enq > 0 {
		q.enqueuedCount.Add(enq)
		q.lastEnqueueUnixNano.Store(now.UnixNano())
	}
	return nil
}

// Dequeue removes and returns a job. Returns (nil, nil) if empty.
func (q *WorkivaQueue) Dequeue() (jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}
	for {
		head := q.head.Load()
		item, err := head.rb.Poll(1 * time.Microsecond)
		if err == nil && item != nil {
			job := item.(jobs.Job)
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
		if err != nil && !errors.Is(err, wqueue.ErrTimeout) {
			return nil, err
		}
		// Try to advance to next segment if drained
		if next := head.next.Load(); next != nil && head.rb.Len() == 0 {
			q.head.CompareAndSwap(head, next)
			continue
		}
		return nil, nil // observed empty
	}
}

// DequeueBatch removes up to maxSize jobs. Returns quickly if empty.
func (q *WorkivaQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if q.closed.Load() == 1 && q.IsEmpty() {
		return nil, ErrQueueClosed
	}
	if maxSize <= 0 {
		return nil, nil
	}
	out := make([]jobs.Job, 0, maxSize)
	for len(out) < maxSize {
		head := q.head.Load()
		item, err := head.rb.Poll(1 * time.Microsecond)
		if err == nil && item != nil {
			out = append(out, item.(jobs.Job))
			continue
		}
		if err != nil && !errors.Is(err, wqueue.ErrTimeout) {
			return nil, err
		}
		if next := head.next.Load(); next != nil && head.rb.Len() == 0 {
			q.head.CompareAndSwap(head, next)
			continue
		}
		break
	}
	if len(out) > 0 {
		now := time.Now()
		var totalWait, maxWait int64
		for _, j := range out {
			enqueueTime := j.GetEnqueueTime()
			if !enqueueTime.IsZero() {
				w := int64(now.Sub(enqueueTime))
				totalWait += w
				if w > maxWait {
					maxWait = w
				}
			}
		}
		if totalWait > 0 {
			q.totalQueueWaitNanos.Add(totalWait)
			for {
				currentMax := q.maxQueueWaitNanos.Load()
				if maxWait <= currentMax {
					break
				}
				if q.maxQueueWaitNanos.CompareAndSwap(currentMax, maxWait) {
					break
				}
			}
		}
		q.dequeuedCount.Add(int64(len(out)))
		q.lastDequeueUnixNano.Store(now.UnixNano())
	}
	return out, nil
}

func (q *WorkivaQueue) IsEmpty() bool {
	return q.enqueuedCount.Load() <= q.dequeuedCount.Load()
}

func (q *WorkivaQueue) Close() {
	q.closed.Store(1)
	// Dispose all ring buffer segments to unblock any waiters.
	seg := q.head.Load()
	for seg != nil {
		if seg.rb != nil {
			seg.rb.Dispose()
		}
		seg = seg.next.Load()
	}
}

func (q *WorkivaQueue) Stats() Stats {
	enq := q.enqueuedCount.Load()
	deq := q.dequeuedCount.Load()
	depth := enq - deq
	if depth < 0 {
		depth = 0
	}
	elapsed := time.Since(time.Unix(0, q.startUnixNano.Load()))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	avgWaitNs := int64(0)
	if deq > 0 {
		avgWaitNs = q.totalQueueWaitNanos.Load() / deq
	}
	return Stats{
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
}
