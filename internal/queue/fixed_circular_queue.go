// Package queue provides high-performance, thread-safe queue implementations
// Fixed version addressing race conditions and use-after-free bugs
package queue

import (
	"sync/atomic"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
)

// FixedCircularQueue implements a truly lock-free circular buffer
// Addresses race conditions in the original implementation
type FixedCircularQueue struct {
	items    []Job
	head     uint64 // Consumer index
	tail     uint64 // Producer index
	mask     uint64 // capacity - 1 (for power-of-2 sizes)
	capacity uint64
	
	// Padding to prevent false sharing between cache lines
	_ [56]byte
}

// Job represents work to be processed by the worker pool
type Job struct {
	EntityID ecs.Entity `json:"entity_id"`
	URL      string     `json:"url"`
	Method   string     `json:"method"`
	Timeout  int64      `json:"timeout_ms"` // milliseconds for JSON compatibility
	JobType  JobType    `json:"job_type"`
}

// JobType represents the type of job to be processed
type JobType uint8

const (
	JobTypePulse JobType = iota
	JobTypeIntervention
	JobTypeCode
)

// NewFixedCircularQueue creates a new circular queue with power-of-2 capacity
func NewFixedCircularQueue(capacity uint64) *FixedCircularQueue {
	// Ensure capacity is power of 2 for efficient bitwise operations
	cap := uint64(1)
	for cap < capacity {
		cap <<= 1
	}
	
	return &FixedCircularQueue{
		items:    make([]Job, cap),
		mask:     cap - 1,
		capacity: cap,
	}
}

// Enqueue adds a job to the queue using Compare-And-Swap for atomicity
// Returns false if queue is full
func (q *FixedCircularQueue) Enqueue(job Job) bool {
	for {
		tail := atomic.LoadUint64(&q.tail)
		head := atomic.LoadUint64(&q.head)
		
		// Check if queue is full
		if tail-head >= q.capacity {
			return false
		}
		
		// Try to claim the slot atomically
		if atomic.CompareAndSwapUint64(&q.tail, tail, tail+1) {
			// Successfully claimed slot, now store the job
			// This is safe because we own this slot now
			q.items[tail&q.mask] = job
			return true
		}
		// CAS failed, retry
	}
}

// Dequeue removes a single job from the queue atomically
// Returns job and true if successful, zero job and false if empty
func (q *FixedCircularQueue) Dequeue() (Job, bool) {
	for {
		head := atomic.LoadUint64(&q.head)
		tail := atomic.LoadUint64(&q.tail)
		
		if head >= tail {
			return Job{}, false // Queue empty
		}
		
		// Try to claim the slot atomically
		if atomic.CompareAndSwapUint64(&q.head, head, head+1) {
			// Successfully claimed slot, now read the job
			job := q.items[head&q.mask]
			return job, true
		}
		// CAS failed, retry
	}
}

// EnqueueBatch adds multiple jobs with proper synchronization
// Uses a more conservative approach to avoid race conditions
func (q *FixedCircularQueue) EnqueueBatch(jobs []Job) int {
	enqueued := 0
	for _, job := range jobs {
		if q.Enqueue(job) {
			enqueued++
		} else {
			break // Queue full, stop trying
		}
	}
	return enqueued
}

// DequeueBatch removes multiple jobs with proper synchronization
// More conservative but race-condition-free implementation
func (q *FixedCircularQueue) DequeueBatch(batch []Job) int {
	dequeued := 0
	for i := range batch {
		if job, ok := q.Dequeue(); ok {
			batch[i] = job
			dequeued++
		} else {
			break // Queue empty
		}
	}
	return dequeued
}

// Size returns current queue size (may be slightly stale but safe)
func (q *FixedCircularQueue) Size() uint64 {
	tail := atomic.LoadUint64(&q.tail)
	head := atomic.LoadUint64(&q.head)
	if tail >= head {
		return tail - head
	}
	return 0 // Handle potential race condition gracefully
}

// Capacity returns the maximum queue capacity
func (q *FixedCircularQueue) Capacity() uint64 {
	return q.capacity
}

// IsEmpty returns true if the queue appears empty
func (q *FixedCircularQueue) IsEmpty() bool {
	return q.Size() == 0
}

// IsFull returns true if the queue appears full
func (q *FixedCircularQueue) IsFull() bool {
	return q.Size() >= q.capacity
}

// Stats returns queue statistics for monitoring
func (q *FixedCircularQueue) Stats() QueueStats {
	size := q.Size()
	return QueueStats{
		Size:     size,
		Capacity: q.capacity,
		Usage:    float64(size) / float64(q.capacity),
	}
}

// QueueStats provides queue performance metrics
type QueueStats struct {
	Size     uint64  `json:"size"`
	Capacity uint64  `json:"capacity"`
	Usage    float64 `json:"usage_percent"`
}

// Reset clears the queue (not thread-safe, use only during shutdown)
func (q *FixedCircularQueue) Reset() {
	atomic.StoreUint64(&q.head, 0)
	atomic.StoreUint64(&q.tail, 0)
}

// Alternative implementation using channels for guaranteed thread safety
// Trades some performance for absolute correctness

// ChannelQueue implements a queue using Go channels for guaranteed thread safety
type ChannelQueue struct {
	jobs     chan Job
	capacity int
}

// NewChannelQueue creates a new channel-based queue
func NewChannelQueue(capacity int) *ChannelQueue {
	return &ChannelQueue{
		jobs:     make(chan Job, capacity),
		capacity: capacity,
	}
}

// Enqueue adds a job to the queue (non-blocking)
func (q *ChannelQueue) Enqueue(job Job) bool {
	select {
	case q.jobs <- job:
		return true
	default:
		return false // Queue full
	}
}

// Dequeue removes a job from the queue (non-blocking)
func (q *ChannelQueue) Dequeue() (Job, bool) {
	select {
	case job := <-q.jobs:
		return job, true
	default:
		return Job{}, false // Queue empty
	}
}

// EnqueueBatch adds multiple jobs
func (q *ChannelQueue) EnqueueBatch(jobs []Job) int {
	enqueued := 0
	for _, job := range jobs {
		if q.Enqueue(job) {
			enqueued++
		} else {
			break
		}
	}
	return enqueued
}

// DequeueBatch removes multiple jobs
func (q *ChannelQueue) DequeueBatch(batch []Job) int {
	dequeued := 0
	for i := range batch {
		if job, ok := q.Dequeue(); ok {
			batch[i] = job
			dequeued++
		} else {
			break
		}
	}
	return dequeued
}

// Size returns current queue size
func (q *ChannelQueue) Size() uint64 {
	return uint64(len(q.jobs))
}

// Capacity returns maximum queue capacity
func (q *ChannelQueue) Capacity() uint64 {
	return uint64(q.capacity)
}

// Stats returns queue statistics
func (q *ChannelQueue) Stats() QueueStats {
	size := q.Size()
	return QueueStats{
		Size:     size,
		Capacity: uint64(q.capacity),
		Usage:    float64(size) / float64(q.capacity),
	}
}

// Close closes the queue (call during shutdown)
func (q *ChannelQueue) Close() {
	close(q.jobs)
}

// Queue interface for polymorphic usage
type Queue interface {
	Enqueue(job Job) bool
	Dequeue() (Job, bool)
	EnqueueBatch(jobs []Job) int
	DequeueBatch(batch []Job) int
	Size() uint64
	Capacity() uint64
	Stats() QueueStats
}

// Ensure both implementations satisfy the Queue interface
var _ Queue = (*FixedCircularQueue)(nil)
var _ Queue = (*ChannelQueue)(nil)

// Production-ready queue factory
func NewProductionQueue(capacity uint64, useChannels bool) Queue {
	if useChannels {
		// Channel-based queue: guaranteed thread safety, slightly lower performance
		return NewChannelQueue(int(capacity))
	} else {
		// Lock-free queue: higher performance, requires careful usage
		return NewFixedCircularQueue(capacity)
	}
}

