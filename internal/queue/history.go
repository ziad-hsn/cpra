package queue

import (
	"sync"
	"time"
)

// Sample is a timestamped value stored in a History ring buffer.
type Sample[T any] struct {
	Timestamp time.Time `json:"timestamp"`
	Value     T         `json:"value"`
}

// History is a fixed-size, thread-safe ring buffer of timestamped samples. It
// retains a rolling window of queue and worker-pool statistics for the web
// API's history endpoints. Writers (the sampler goroutine) and readers (HTTP
// handlers) may run concurrently.
type History[T any] struct {
	mu       sync.RWMutex
	buf      []Sample[T]
	capacity int
	head     int // index of the next write
	size     int // number of valid samples
}

// NewHistory returns a History that retains up to capacity samples.
func NewHistory[T any](capacity int) *History[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &History[T]{buf: make([]Sample[T], capacity), capacity: capacity}
}

// Record appends a sample with the current wall-clock time.
func (h *History[T]) Record(v T) {
	h.RecordAt(time.Now(), v)
}

// RecordAt appends a sample with an explicit timestamp.
func (h *History[T]) RecordAt(t time.Time, v T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf[h.head] = Sample[T]{Timestamp: t, Value: v}
	h.head = (h.head + 1) % h.capacity
	if h.size < h.capacity {
		h.size++
	}
}

// Snapshot returns the retained samples in chronological order (oldest first).
func (h *History[T]) Snapshot() []Sample[T] {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Sample[T], 0, h.size)
	for i := 0; i < h.size; i++ {
		idx := (h.head - h.size + i + h.capacity) % h.capacity
		out = append(out, h.buf[idx])
	}
	return out
}

// Len returns the number of retained samples.
func (h *History[T]) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.size
}
