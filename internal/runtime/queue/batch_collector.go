package queue

import (
	"cpra/internal/runtime/jobs"
	"sync"
	"time"
)

// batchCollector uses sync.Cond for efficient batch collection with immediate
// signaling when batch is full. This reduces CPU burn compared to ticker-based polling.
type batchCollector struct {
	mu      sync.Mutex
	cond    *sync.Cond
	batch   []jobs.Result
	maxSize int
	ready   bool          // true when batch is full or timeout fired
	closed  bool          // true when collector is shut down
	timer   *time.Timer   // for timeout-based flushing
	timeout time.Duration // batch flush timeout
	onFlush func()        // called after flush signals are sent
}

// newBatchCollector creates a batch collector with sync.Cond signaling.
func newBatchCollector(maxSize int, timeout time.Duration) *batchCollector {
	c := &batchCollector{
		batch:   make([]jobs.Result, 0, maxSize),
		maxSize: maxSize,
		timeout: timeout,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Add adds a result to the batch. Signals when batch is full.
func (c *batchCollector) Add(r jobs.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	c.batch = append(c.batch, r)

	// Start timer on first item
	if len(c.batch) == 1 && c.timeout > 0 {
		c.startTimerLocked()
	}

	// Signal immediately when batch is full
	if len(c.batch) >= c.maxSize {
		c.ready = true
		c.stopTimerLocked()
		c.cond.Signal()
	}
}

// startTimerLocked starts the timeout timer (must hold lock).
func (c *batchCollector) startTimerLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(c.timeout, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.batch) > 0 && !c.closed {
			c.ready = true
			c.cond.Signal()
		}
	})
}

// stopTimerLocked stops the timeout timer (must hold lock).
func (c *batchCollector) stopTimerLocked() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// Wait blocks until a batch is ready (full or timeout). Returns the batch.
// Returns nil if closed with no pending items.
func (c *batchCollector) Wait() []jobs.Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Wait for ready signal
	for !c.ready && !c.closed {
		c.cond.Wait()
	}

	// Return nil if closed with empty batch
	if c.closed && len(c.batch) == 0 {
		return nil
	}

	// Swap out the batch
	result := c.batch
	c.batch = make([]jobs.Result, 0, c.maxSize)
	c.ready = false
	c.stopTimerLocked()

	return result
}

// Flush forces a flush of any pending items.
func (c *batchCollector) Flush() []jobs.Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stopTimerLocked()
	result := c.batch
	c.batch = make([]jobs.Result, 0, c.maxSize)
	c.ready = false
	return result
}

// Close shuts down the collector and wakes any waiting goroutine.
func (c *batchCollector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	c.stopTimerLocked()
	c.cond.Broadcast() // Wake all waiters
}

// Len returns the current batch size.
func (c *batchCollector) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batch)
}
