package queue

import (
	"cpra/internal/jobs"
	"fmt"
	"sync"
)

// Handle gives producers, workers and observers the same stable queue identity.
// Replacement is allowed only when empty, before controller execution begins.
type Handle struct {
	mu    sync.RWMutex
	queue Queue
}

func NewHandle(q Queue) *Handle { return &Handle{queue: q} }
func (h *Handle) ReplaceEmpty(q Queue) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.queue.Stats().QueueDepth != 0 {
		return fmt.Errorf("queue replacement requires an empty queue")
	}
	h.queue.Close()
	h.queue = q
	return nil
}
func (h *Handle) Enqueue(j jobs.Job) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.queue.Enqueue(j)
}
func (h *Handle) EnqueueBatch(js []interface{}) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.queue.EnqueueBatch(js)
}
func (h *Handle) Dequeue() (jobs.Job, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.queue.Dequeue()
}
func (h *Handle) DequeueBatch(n int) ([]jobs.Job, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.queue.DequeueBatch(n)
}
func (h *Handle) Stats() Stats { h.mu.RLock(); defer h.mu.RUnlock(); return h.queue.Stats() }
func (h *Handle) Close()       { h.mu.Lock(); defer h.mu.Unlock(); h.queue.Close() }
