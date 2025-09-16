package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/panjf2000/ants/v2"
)

// ===== OPTIMIZED COMPONENTS (Data-Only, Minimal State Changes) =====

type Monitor struct {
	URL           string
	Interval      time.Duration
	NextCheck     time.Time
	Status        MonitorStatus
	Priority      int
	JobID         uint64 // 0 = ready, >0 = processing
	RetryCount    int
	LastError     string
}

type MonitorStatus int

const (
	StatusUnknown MonitorStatus = iota
	StatusSuccess
	StatusFailed
	StatusTimeout
)

// ===== KUBERNETES-STYLE WORK QUEUE (Bulk Processing) =====

type WorkItem struct {
	Entity   ecs.Entity
	URL      string
	Priority int
	JobID    uint64
}

type WorkQueue struct {
	items     []WorkItem
	delayed   map[time.Time][]WorkItem
	capacity  int
	mutex     sync.RWMutex
	metrics   QueueMetrics
}

type QueueMetrics struct {
	Enqueued  uint64
	Dequeued  uint64
	Dropped   uint64
	QueueSize uint64
}

func NewWorkQueue(capacity int) *WorkQueue {
	return &WorkQueue{
		items:    make([]WorkItem, 0, capacity),
		delayed:  make(map[time.Time][]WorkItem),
		capacity: capacity,
	}
}

func (wq *WorkQueue) Enqueue(item WorkItem) bool {
	wq.mutex.Lock()
	defer wq.mutex.Unlock()
	
	if len(wq.items) >= wq.capacity {
		atomic.AddUint64(&wq.metrics.Dropped, 1)
		return false
	}
	
	wq.items = append(wq.items, item)
	atomic.AddUint64(&wq.metrics.Enqueued, 1)
	atomic.StoreUint64(&wq.metrics.QueueSize, uint64(len(wq.items)))
	return true
}

func (wq *WorkQueue) EnqueueDelayed(item WorkItem, delay time.Duration) {
	wq.mutex.Lock()
	defer wq.mutex.Unlock()
	
	executeTime := time.Now().Add(delay)
	wq.delayed[executeTime] = append(wq.delayed[executeTime], item)
}

// Bulk dequeue - Kubernetes pattern
func (wq *WorkQueue) GetWork(maxItems int) []WorkItem {
	wq.mutex.Lock()
	defer wq.mutex.Unlock()
	
	now := time.Now()
	ready := make([]WorkItem, 0, maxItems)
	
	// Process delayed items first
	for timestamp, items := range wq.delayed {
		if now.After(timestamp) {
			ready = append(ready, items...)
			delete(wq.delayed, timestamp)
			
			if len(ready) >= maxItems {
				break
			}
		}
	}
	
	// Add immediate items
	available := min(maxItems-len(ready), len(wq.items))
	if available > 0 {
		ready = append(ready, wq.items[:available]...)
		wq.items = wq.items[available:]
	}
	
	atomic.AddUint64(&wq.metrics.Dequeued, uint64(len(ready)))
	atomic.StoreUint64(&wq.metrics.QueueSize, uint64(len(wq.items)))
	
	return ready
}

func (wq *WorkQueue) GetMetrics() QueueMetrics {
	return QueueMetrics{
		Enqueued:  atomic.LoadUint64(&wq.metrics.Enqueued),
		Dequeued:  atomic.LoadUint64(&wq.metrics.Dequeued),
		Dropped:   atomic.LoadUint64(&wq.metrics.Dropped),
		QueueSize: atomic.LoadUint64(&wq.metrics.QueueSize),
	}
}

// ===== ANTS-BASED WORKER POOL MANAGER (High Performance) =====

type WorkerPoolManager struct {
	pulsePool        *ants.MultiPool
	interventionPool *ants.Pool
	codePool         *ants.Pool
	resultChan       chan WorkResult
	logger           Logger
}

type WorkResult struct {
	Entity ecs.Entity
	JobID  uint64
	Error  error
	Status MonitorStatus
}

func NewWorkerPoolManager(logger Logger) (*WorkerPoolManager, error) {
	resultChan := make(chan WorkResult, 100000) // Large buffer for results
	
	// Multi-pool with load balancing for main pulse workload
	pulsePool, err := ants.NewMultiPool(
		1000000, // Total capacity for 1M monitors
		10000,   // Size per sub-pool
		ants.RoundRobin, // Load balancing strategy
		ants.WithPreAlloc(true), // Pre-allocate for performance
	)
	if err != nil {
		return nil, err
	}
	
	// Smaller pools for other job types
	interventionPool, err := ants.NewPool(10000, 
		ants.WithPreAlloc(true),
		ants.WithPanicHandler(func(p interface{}) {
			logger.Error("Intervention worker panic: %v", p)
		}),
	)
	if err != nil {
		return nil, err
	}
	
	codePool, err := ants.NewPool(10000,
		ants.WithPreAlloc(true),
		ants.WithPanicHandler(func(p interface{}) {
			logger.Error("Code worker panic: %v", p)
		}),
	)
	if err != nil {
		return nil, err
	}
	
	return &WorkerPoolManager{
		pulsePool:        pulsePool,
		interventionPool: interventionPool,
		codePool:         codePool,
		resultChan:       resultChan,
		logger:           logger,
	}, nil
}

func (wpm *WorkerPoolManager) SubmitPulseWork(items []WorkItem) error {
	for _, item := range items {
		err := wpm.pulsePool.Submit(func() {
			result := wpm.executePulseJob(item)
			wpm.resultChan <- result
		})
		if err != nil {
			// If submission fails, send error result
			wpm.resultChan <- WorkResult{
				Entity: item.Entity,
				JobID:  item.JobID,
				Error:  err,
				Status: StatusFailed,
			}
		}
	}
	return nil
}

func (wpm *WorkerPoolManager) executePulseJob(item WorkItem) WorkResult {
	// Simulate HTTP request
	start := time.Now()
	
	// TODO: Replace with actual HTTP client
	time.Sleep(time.Millisecond * 10) // Simulate network call
	
	duration := time.Since(start)
	
	// Simulate success/failure based on duration
	var status MonitorStatus
	var err error
	
	if duration > time.Millisecond*100 {
		status = StatusTimeout
		err = fmt.Errorf("request timeout after %v", duration)
	} else if duration > time.Millisecond*50 {
		status = StatusFailed
		err = fmt.Errorf("request failed after %v", duration)
	} else {
		status = StatusSuccess
	}
	
	return WorkResult{
		Entity: item.Entity,
		JobID:  item.JobID,
		Error:  err,
		Status: status,
	}
}

func (wpm *WorkerPoolManager) GetResultChannel() <-chan WorkResult {
	return wpm.resultChan
}

func (wpm *WorkerPoolManager) GetStats() PoolStats {
	return PoolStats{
		PulseRunning:        wpm.pulsePool.Running(),
		PulseWaiting:        wpm.pulsePool.Waiting(),
		InterventionRunning: wpm.interventionPool.Running(),
		InterventionWaiting: wpm.interventionPool.Waiting(),
		CodeRunning:         wpm.codePool.Running(),
		CodeWaiting:         wpm.codePool.Waiting(),
	}
}

func (wpm *WorkerPoolManager) Close() {
	wpm.pulsePool.ReleaseTimeout(time.Second * 5)
	wpm.interventionPool.ReleaseTimeout(time.Second * 5)
	wpm.codePool.ReleaseTimeout(time.Second * 5)
	close(wpm.resultChan)
}

// ===== OPTIMIZED MONITOR SYSTEM (Ark Best Practices) =====

type OptimalMonitorSystem struct {
	world       *ecs.World
	monitors    *ecs.Map1[Monitor]
	workQueue   *WorkQueue
	workerPools *WorkerPoolManager
	
	// Configuration
	batchSize       int           // 100,000+ for 1M monitors
	updateInterval  time.Duration
	maxJobsPerCycle int
	
	// Metrics
	processedCount uint64
	enqueuedCount  uint64
	completedCount uint64
	
	logger Logger
}

func NewOptimalMonitorSystem(
	world *ecs.World,
	logger Logger) (*OptimalMonitorSystem, error) {
	
	workQueue := NewWorkQueue(1000000) // 1M capacity based on Little's Law
	
	workerPools, err := NewWorkerPoolManager(logger)
	if err != nil {
		return nil, err
	}
	
	return &OptimalMonitorSystem{
		world:           world,
		monitors:        ecs.NewMap1[Monitor](world),
		workQueue:       workQueue,
		workerPools:     workerPools,
		batchSize:       100000, // Large batches for Ark optimization
		updateInterval:  10 * time.Millisecond, // Responsive updates
		maxJobsPerCycle: 50000,  // Prevent overwhelming
		logger:          logger,
	}, nil
}

// Bulk create monitors using Ark's batch operations
func (oms *OptimalMonitorSystem) CreateMonitors(configs []MonitorConfig) error {
	start := time.Now()
	
	// Prepare entities and components
	entities := make([]ecs.Entity, len(configs))
	monitors := make([]*Monitor, len(configs))
	
	now := time.Now()
	for i, config := range configs {
		entities[i] = oms.world.NewEntity()
		monitors[i] = &Monitor{
			URL:       config.URL,
			Interval:  config.Interval,
			NextCheck: now.Add(time.Duration(i) * time.Millisecond), // Stagger initial checks
			Status:    StatusUnknown,
			Priority:  config.Priority,
			JobID:     0,
		}
	}
	
	// Bulk add components - 11x faster than individual adds
	oms.monitors.AddBatch(entities, monitors...)
	
	oms.logger.Info("Created %d monitors in %v using Ark batch operations", 
		len(configs), time.Since(start))
	
	return nil
}

// Main update loop using optimal patterns
func (oms *OptimalMonitorSystem) Update(ctx context.Context) error {
	start := time.Now()
	
	// Process results first to free up workers
	oms.processResults()
	
	// Process monitors that need checking
	oms.processMonitors()
	
	// Log metrics periodically
	if atomic.LoadUint64(&oms.processedCount)%100000 == 0 {
		oms.logMetrics(time.Since(start))
	}
	
	return nil
}

// Process monitors using Ark's bulk iteration
func (oms *OptimalMonitorSystem) processMonitors() {
	now := time.Now()
	readyEntities := make([]ecs.Entity, 0, oms.batchSize)
	jobsThisCycle := 0
	
	// Single pass through ALL monitors - Ark's strength
	query := oms.monitors.Query(oms.world)
	defer query.Close()
	
	for query.Next() {
		if jobsThisCycle >= oms.maxJobsPerCycle {
			break
		}
		
		entity := query.Entity()
		monitor := query.Get()
		
		if now.After(monitor.NextCheck) && monitor.JobID == 0 {
			readyEntities = append(readyEntities, entity)
			jobsThisCycle++
			
			// Process in large batches
			if len(readyEntities) >= oms.batchSize {
				oms.processBulkBatch(readyEntities, now)
				readyEntities = readyEntities[:0]
			}
		}
	}
	
	// Process final batch
	if len(readyEntities) > 0 {
		oms.processBulkBatch(readyEntities, now)
	}
	
	atomic.AddUint64(&oms.processedCount, uint64(jobsThisCycle))
}

// Bulk batch processing with priority handling
func (oms *OptimalMonitorSystem) processBulkBatch(entities []ecs.Entity, now time.Time) {
	if len(entities) == 0 {
		return
	}
	
	// Create work items with priority separation
	highPriorityItems := make([]WorkItem, 0)
	normalPriorityItems := make([]WorkItem, 0)
	jobIDCounter := atomic.AddUint64(&oms.enqueuedCount, uint64(len(entities)))
	
	for i, entity := range entities {
		monitor := oms.monitors.Get(entity)
		jobID := jobIDCounter - uint64(len(entities)) + uint64(i) + 1
		
		item := WorkItem{
			Entity:   entity,
			URL:      monitor.URL,
			Priority: monitor.Priority,
			JobID:    jobID,
		}
		
		if monitor.Priority > 5 {
			highPriorityItems = append(highPriorityItems, item)
		} else {
			normalPriorityItems = append(normalPriorityItems, item)
		}
	}
	
	// Submit to worker pools (high priority first)
	if len(highPriorityItems) > 0 {
		oms.workerPools.SubmitPulseWork(highPriorityItems)
	}
	if len(normalPriorityItems) > 0 {
		oms.workerPools.SubmitPulseWork(normalPriorityItems)
	}
	
	// Bulk update monitor states using Ark's batch operations
	oms.updateMonitorStates(entities, now)
}

// Bulk state update using Ark's MapBatchFn
func (oms *OptimalMonitorSystem) updateMonitorStates(entities []ecs.Entity, now time.Time) {
	jobIDStart := atomic.LoadUint64(&oms.enqueuedCount) - uint64(len(entities)) + 1
	
	// Use Ark's batch function - 10x faster than individual updates
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, monitor *Monitor) {
		// Calculate job ID for this entity
		entityIndex := 0
		for i, e := range entities {
			if e == entity {
				entityIndex = i
				break
			}
		}
		
		monitor.JobID = jobIDStart + uint64(entityIndex)
		monitor.NextCheck = now.Add(monitor.Interval)
	})
}

// Bulk process results
func (oms *OptimalMonitorSystem) processResults() {
	results := make([]WorkResult, 0, 10000)
	
	// Non-blocking collection of all available results
	for len(results) < cap(results) {
		select {
		case result := <-oms.workerPools.GetResultChannel():
			results = append(results, result)
		default:
			break
		}
	}
	
	if len(results) == 0 {
		return
	}
	
	// Group results by entity
	entityResults := make(map[ecs.Entity]WorkResult, len(results))
	completedEntities := make([]ecs.Entity, 0, len(results))
	
	for _, result := range results {
		if oms.world.Alive(result.Entity) {
			entityResults[result.Entity] = result
			completedEntities = append(completedEntities, result.Entity)
		}
	}
	
	if len(completedEntities) > 0 {
		// Bulk update completed monitors
		oms.updateCompletedMonitors(completedEntities, entityResults)
		atomic.AddUint64(&oms.completedCount, uint64(len(completedEntities)))
	}
}

// Bulk update completed monitors using Ark's batch operations
func (oms *OptimalMonitorSystem) updateCompletedMonitors(
	entities []ecs.Entity,
	results map[ecs.Entity]WorkResult) {
	
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, monitor *Monitor) {
		result, exists := results[entity]
		if !exists {
			return
		}
		
		// Clear job ID
		monitor.JobID = 0
		
		// Update status
		monitor.Status = result.Status
		if result.Error != nil {
			monitor.LastError = result.Error.Error()
			monitor.RetryCount++
		} else {
			monitor.LastError = ""
			monitor.RetryCount = 0
		}
	})
}

func (oms *OptimalMonitorSystem) logMetrics(processingTime time.Duration) {
	queueMetrics := oms.workQueue.GetMetrics()
	poolStats := oms.workerPools.GetStats()
	
	oms.logger.Info("System metrics: processed=%d, enqueued=%d, completed=%d, queue_size=%d, workers_running=%d, process_time=%v",
		atomic.LoadUint64(&oms.processedCount),
		atomic.LoadUint64(&oms.enqueuedCount),
		atomic.LoadUint64(&oms.completedCount),
		queueMetrics.QueueSize,
		poolStats.PulseRunning,
		processingTime)
}

// ===== SUPPORTING TYPES =====

type MonitorConfig struct {
	URL      string
	Interval time.Duration
	Priority int
}

type PoolStats struct {
	PulseRunning        int
	PulseWaiting        int
	InterventionRunning int
	InterventionWaiting int
	CodeRunning         int
	CodeWaiting         int
}

type Logger interface {
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ===== MAIN FUNCTION EXAMPLE =====

func main() {
	// Create ECS world
	world := ecs.NewWorld()
	
	// Create logger (implement as needed)
	logger := &SimpleLogger{}
	
	// Create optimal monitor system
	system, err := NewOptimalMonitorSystem(&world, logger)
	if err != nil {
		log.Fatal(err)
	}
	
	// Create 1M monitors
	configs := make([]MonitorConfig, 1000000)
	for i := range configs {
		configs[i] = MonitorConfig{
			URL:      fmt.Sprintf("https://example.com/monitor/%d", i),
			Interval: time.Duration(10+rand.Intn(20)) * time.Second,
			Priority: rand.Intn(10),
		}
	}
	
	// Bulk create monitors
	err = system.CreateMonitors(configs)
	if err != nil {
		log.Fatal(err)
	}
	
	// Run system
	ctx := context.Background()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			err := system.Update(ctx)
			if err != nil {
				logger.Error("System update error: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

type SimpleLogger struct{}

func (sl *SimpleLogger) Info(format string, args ...interface{}) {
	fmt.Printf("[INFO] "+format+"\n", args...)
}

func (sl *SimpleLogger) Error(format string, args ...interface{}) {
	fmt.Printf("[ERROR] "+format+"\n", args...)
}

