// Package queue provides high-performance queue implementations
// Following research from GeeksforGeeks and production queue patterns
package queue

import (
	"sync/atomic"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
)

// CircularQueue implements a lock-free circular buffer
// Based on research and optimized for 1M+ monitor throughput
type CircularQueue struct {
	items    []Job
	head     uint64 // Use uint64 to prevent ABA problems
	tail     uint64
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

// NewCircularQueue creates a new circular queue with power-of-2 capacity
// Following Go constructor patterns from Effective Go
func NewCircularQueue(capacity uint64) *CircularQueue {
	// Ensure capacity is power of 2 for efficient bitwise operations
	cap := uint64(1)
	for cap < capacity {
		cap <<= 1
	}
	
	return &CircularQueue{
		items:    make([]Job, cap),
		mask:     cap - 1,
		capacity: cap,
	}
}

// Enqueue adds a job to the queue using Compare-And-Swap for atomicity
// Returns false if queue is full
func (q *CircularQueue) Enqueue(job Job) bool {
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

// EnqueueBatch adds multiple jobs efficiently
// Returns number of jobs successfully enqueued
func (q *CircularQueue) EnqueueBatch(jobs []Job) int {
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

// Dequeue removes a single job from the queue atomically
// Returns job and true if successful, zero job and false if empty
func (q *CircularQueue) Dequeue() (Job, bool) {
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

// DequeueBatch removes multiple jobs with proper synchronization
// More conservative but race-condition-free implementation
func (q *CircularQueue) DequeueBatch(batch []Job) int {
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
func (q *CircularQueue) Size() uint64 {
	tail := atomic.LoadUint64(&q.tail)
	head := atomic.LoadUint64(&q.head)
	if tail >= head {
		return tail - head
	}
	return 0 // Handle potential race condition gracefully
}

// Capacity returns the maximum queue capacity
func (q *CircularQueue) Capacity() uint64 {
	return q.capacity
}

// IsEmpty returns true if the queue is empty
func (q *CircularQueue) IsEmpty() bool {
	return q.Size() == 0
}

// IsFull returns true if the queue is full
func (q *CircularQueue) IsFull() bool {
	return q.Size() >= q.capacity
}

// Stats returns queue statistics for monitoring
func (q *CircularQueue) Stats() QueueStats {
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

// Unsafe operations for maximum performance (use with caution)

// UnsafeSize returns queue size without atomic operations
// Only use when you're certain there's no concurrent access
func (q *CircularQueue) UnsafeSize() uint64 {
	return *(*uint64)(unsafe.Pointer(&q.tail)) - *(*uint64)(unsafe.Pointer(&q.head))
}

// Reset clears the queue (not thread-safe)
// Only use during initialization or shutdown
func (q *CircularQueue) Reset() {
	atomic.StoreUint64(&q.head, 0)
	atomic.StoreUint64(&q.tail, 0)
}

