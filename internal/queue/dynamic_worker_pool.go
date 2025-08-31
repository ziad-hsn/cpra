package queue

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// WorkerPoolLogger interface for structured logging
type WorkerPoolLogger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Warn(format string, args ...interface{})
}

// DynamicWorkerPool manages a pool of workers that can scale up and down
type DynamicWorkerPool struct {
	// Configuration
	minWorkers    int32
	maxWorkers    int32
	scaleInterval time.Duration

	// State
	currentWorkers int32
	targetWorkers  int32

	// Work distribution
	workChan    chan func()
	workerChans []chan func()

	// Statistics
	tasksProcessed   int64
	tasksQueued      int64
	workersCreated   int64
	workersDestroyed int64

	// Control
	running int32
	mu      sync.RWMutex

	// Logging
	logger WorkerPoolLogger
}

// WorkerPoolConfig holds worker pool configuration
type WorkerPoolConfig struct {
	MinWorkers    int           // Minimum number of workers
	MaxWorkers    int           // Maximum number of workers
	ScaleInterval time.Duration // How often to check scaling
	QueueSize     int           // Work queue size
}

// WorkerPoolStats holds worker pool statistics
type WorkerPoolStats struct {
	CurrentWorkers   int32
	TargetWorkers    int32
	TasksProcessed   int64
	TasksQueued      int64
	WorkersCreated   int64
	WorkersDestroyed int64
	QueueDepth       int
}

// DefaultWorkerPoolConfig returns default configuration
func DefaultWorkerPoolConfig() WorkerPoolConfig {
	numCPU := runtime.NumCPU()
	return WorkerPoolConfig{
		MinWorkers:    numCPU,
		MaxWorkers:    numCPU * 10, // 10x CPU cores
		ScaleInterval: 5 * time.Second,
		QueueSize:     1000,
	}
}

// NewDynamicWorkerPool creates a new dynamic worker pool
func NewDynamicWorkerPool(config WorkerPoolConfig, logger WorkerPoolLogger) *DynamicWorkerPool {
	pool := &DynamicWorkerPool{
		minWorkers:    int32(config.MinWorkers),
		maxWorkers:    int32(config.MaxWorkers),
		scaleInterval: config.ScaleInterval,
		workChan:      make(chan func(), config.QueueSize),
		workerChans:   make([]chan func(), 0, config.MaxWorkers),
		targetWorkers: int32(config.MinWorkers),
		logger:        logger,
	}

	return pool
}

// Start starts the worker pool
func (dwp *DynamicWorkerPool) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&dwp.running, 0, 1) {
		return fmt.Errorf("worker pool already running")
	}

	// Start initial workers
	dwp.scaleWorkers(int(dwp.minWorkers))

	// Start scaling goroutine
	go dwp.scalingLoop(ctx)

	// Start work distribution
	go dwp.distributeWork(ctx)

	return nil
}

// Stop stops the worker pool
func (dwp *DynamicWorkerPool) Stop() {
	atomic.StoreInt32(&dwp.running, 0)
	close(dwp.workChan)
}

// Submit submits work to the pool
func (dwp *DynamicWorkerPool) Submit(work func()) error {
	if atomic.LoadInt32(&dwp.running) == 0 {
		return fmt.Errorf("worker pool not running")
	}

	select {
	case dwp.workChan <- work:
		atomic.AddInt64(&dwp.tasksQueued, 1)
		return nil
	default:
		return fmt.Errorf("work queue full")
	}
}

// scalingLoop monitors and adjusts worker count
func (dwp *DynamicWorkerPool) scalingLoop(ctx context.Context) {
	ticker := time.NewTicker(dwp.scaleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if atomic.LoadInt32(&dwp.running) == 0 {
				return
			}
			dwp.adjustWorkerCount()
		}
	}
}

// adjustWorkerCount adjusts the number of workers based on load
func (dwp *DynamicWorkerPool) adjustWorkerCount() {
	queueDepth := len(dwp.workChan)
	currentWorkers := atomic.LoadInt32(&dwp.currentWorkers)

	var targetWorkers int32

	// Scale up if queue is getting full
	if queueDepth > int(currentWorkers)*2 {
		targetWorkers = currentWorkers + int32(runtime.NumCPU())
		if targetWorkers > dwp.maxWorkers {
			targetWorkers = dwp.maxWorkers
		}
	} else if queueDepth < int(currentWorkers)/2 && currentWorkers > dwp.minWorkers {
		// Scale down if queue is mostly empty
		targetWorkers = currentWorkers - int32(runtime.NumCPU()/2)
		if targetWorkers < dwp.minWorkers {
			targetWorkers = dwp.minWorkers
		}
	} else {
		targetWorkers = currentWorkers
	}

	if targetWorkers != currentWorkers {
		atomic.StoreInt32(&dwp.targetWorkers, targetWorkers)

		if targetWorkers > currentWorkers {
			dwp.scaleUp(int(targetWorkers - currentWorkers))
		} else if targetWorkers < currentWorkers {
			dwp.scaleDown(int(currentWorkers - targetWorkers))
		}
	}
}

// scaleUp adds more workers
func (dwp *DynamicWorkerPool) scaleUp(count int) {
	dwp.mu.Lock()
	defer dwp.mu.Unlock()

	for i := 0; i < count; i++ {
		workerChan := make(chan func(), 1)
		dwp.workerChans = append(dwp.workerChans, workerChan)

		go dwp.worker(workerChan)
		atomic.AddInt32(&dwp.currentWorkers, 1)
		atomic.AddInt64(&dwp.workersCreated, 1)
	}

	dwp.logger.Info("Worker pool scaled up: %d workers (total: %d)", count, atomic.LoadInt32(&dwp.currentWorkers))
}

// scaleDown removes workers
func (dwp *DynamicWorkerPool) scaleDown(count int) {
	dwp.mu.Lock()
	defer dwp.mu.Unlock()

	if count > len(dwp.workerChans) {
		count = len(dwp.workerChans)
	}

	// Close worker channels to signal shutdown
	for i := 0; i < count; i++ {
		if len(dwp.workerChans) > 0 {
			close(dwp.workerChans[len(dwp.workerChans)-1])
			dwp.workerChans = dwp.workerChans[:len(dwp.workerChans)-1]
			atomic.AddInt32(&dwp.currentWorkers, -1)
			atomic.AddInt64(&dwp.workersDestroyed, 1)
		}
	}

	dwp.logger.Info("Worker pool scaled down: %d workers (total: %d)", count, atomic.LoadInt32(&dwp.currentWorkers))
}

// scaleWorkers sets the initial number of workers
func (dwp *DynamicWorkerPool) scaleWorkers(count int) {
	dwp.mu.Lock()
	defer dwp.mu.Unlock()

	for i := 0; i < count; i++ {
		workerChan := make(chan func(), 1)
		dwp.workerChans = append(dwp.workerChans, workerChan)

		go dwp.worker(workerChan)
		atomic.AddInt32(&dwp.currentWorkers, 1)
		atomic.AddInt64(&dwp.workersCreated, 1)
	}
}

// distributeWork distributes work to available workers
func (dwp *DynamicWorkerPool) distributeWork(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case work, ok := <-dwp.workChan:
			if !ok {
				return
			}

			// Find available worker
			dwp.mu.RLock()
			if len(dwp.workerChans) == 0 {
				dwp.mu.RUnlock()
				continue
			}

			// Round-robin distribution
			workerIndex := int(atomic.LoadInt64(&dwp.tasksProcessed)) % len(dwp.workerChans)
			workerChan := dwp.workerChans[workerIndex]
			dwp.mu.RUnlock()

			// Send work to worker
			select {
			case workerChan <- work:
				// Work assigned successfully
			default:
				// Worker busy, try to execute directly
				go func() {
					work()
					atomic.AddInt64(&dwp.tasksProcessed, 1)
				}()
			}
		}
	}
}

// worker is the main worker loop
func (dwp *DynamicWorkerPool) worker(workChan <-chan func()) {
	for work := range workChan {
		work()
		atomic.AddInt64(&dwp.tasksProcessed, 1)
	}
}

// Stats returns current worker pool statistics
func (dwp *DynamicWorkerPool) Stats() WorkerPoolStats {
	dwp.mu.RLock()
	defer dwp.mu.RUnlock()

	return WorkerPoolStats{
		CurrentWorkers:   atomic.LoadInt32(&dwp.currentWorkers),
		TargetWorkers:    atomic.LoadInt32(&dwp.targetWorkers),
		TasksProcessed:   atomic.LoadInt64(&dwp.tasksProcessed),
		TasksQueued:      atomic.LoadInt64(&dwp.tasksQueued),
		WorkersCreated:   atomic.LoadInt64(&dwp.workersCreated),
		WorkersDestroyed: atomic.LoadInt64(&dwp.workersDestroyed),
		QueueDepth:       len(dwp.workChan),
	}
}

// IsRunning returns true if the worker pool is currently running
func (dwp *DynamicWorkerPool) IsRunning() bool {
	return atomic.LoadInt32(&dwp.running) == 1
}
