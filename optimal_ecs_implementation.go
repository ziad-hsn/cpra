package main

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/alitto/pond"
	"github.com/amirylm/lockfree"
)

// ===== CONSOLIDATED COMPONENT DESIGN (Single Component Strategy) =====

type MonitorPhase uint8

const (
	PhaseReady MonitorPhase = iota
	PhasePulsing
	PhaseIntervening
	PhaseCoding
	PhaseDisabled
)

type MonitorFlags uint32

const (
	FlagFirstCheck MonitorFlags = 1 << iota
	FlagYellowCode
	FlagRedCode
	FlagGreenCode
	FlagCyanCode
	FlagGrayCode
	FlagDisabled
	FlagInterventionEnabled
	// Add more flags as needed - extremely efficient bitfield operations
)

// Single consolidated component - eliminates archetype transitions
type MonitorState struct {
	// Core identification
	URL      string
	Name     string
	
	// Timing
	Interval     time.Duration
	LastCheck    time.Time
	NextCheck    time.Time
	
	// Status
	Phase        MonitorPhase
	Status       string
	LastError    error
	
	// Counters
	ConsecutiveFailures int
	RetryCount         int
	MaxFailures        int
	
	// Job tracking
	JobID        uint64  // 0 = not processing, >0 = job ID
	
	// Flags (bitfield for maximum efficiency)
	Flags        MonitorFlags
	
	// Success tracking
	LastSuccessTime time.Time
}

// Efficient flag operations
func (ms *MonitorState) HasFlag(flag MonitorFlags) bool {
	return ms.Flags&flag != 0
}

func (ms *MonitorState) SetFlag(flag MonitorFlags) {
	ms.Flags |= flag
}

func (ms *MonitorState) ClearFlag(flag MonitorFlags) {
	ms.Flags &^= flag
}

func (ms *MonitorState) IsReady() bool {
	return ms.Phase == PhaseReady && ms.JobID == 0
}

func (ms *MonitorState) NeedsPulse(now time.Time) bool {
	return ms.IsReady() && now.After(ms.NextCheck)
}

// ===== MEMORY POOL SYSTEM (Zero-Allocation Pattern) =====

type ObjectPool[T any] struct {
	pool sync.Pool
	new  func() T
}

func NewObjectPool[T any](newFunc func() T) *ObjectPool[T] {
	return &ObjectPool[T]{
		pool: sync.Pool{
			New: func() interface{} {
				return newFunc()
			},
		},
		new: newFunc,
	}
}

func (op *ObjectPool[T]) Get() T {
	return op.pool.Get().(T)
}

func (op *ObjectPool[T]) Put(obj T) {
	op.pool.Put(obj)
}

// Global memory pools for reuse
var (
	entitySlicePool = NewObjectPool(func() []ecs.Entity {
		return make([]ecs.Entity, 0, 10000)
	})
	
	jobSlicePool = NewObjectPool(func() []Job {
		return make([]Job, 0, 10000)
	})
	
	resultSlicePool = NewObjectPool(func() []Result {
		return make([]Result, 0, 10000)
	})
	
	monitorStatePool = NewObjectPool(func() *MonitorState {
		return &MonitorState{}
	})
)

// ===== JOB SYSTEM (Lock-Free High Performance) =====

type Job struct {
	Entity ecs.Entity
	URL    string
	JobID  uint64
}

type Result struct {
	Entity ecs.Entity
	JobID  uint64
	Error  error
	Status string
}

type OptimalJobQueue struct {
	queue     *lockfree.Queue
	workers   *pond.WorkerPool
	results   chan Result
	
	// Atomic counters for metrics
	enqueued  uint64
	processed uint64
	failed    uint64
	completed uint64
}

func NewOptimalJobQueue(workerCount, queueSize int) *OptimalJobQueue {
	return &OptimalJobQueue{
		queue:   lockfree.New(),
		workers: pond.New(workerCount, queueSize, pond.MinWorkers(workerCount/4)),
		results: make(chan Result, queueSize),
	}
}

func (ojq *OptimalJobQueue) EnqueueBatch(jobs []Job) int {
	enqueued := 0
	for _, job := range jobs {
		// Lock-free enqueue
		ojq.queue.Enqueue(job)
		enqueued++
	}
	atomic.AddUint64(&ojq.enqueued, uint64(enqueued))
	return enqueued
}

func (ojq *OptimalJobQueue) ProcessBatch(maxJobs int) {
	processed := 0
	
	for processed < maxJobs {
		item := ojq.queue.Dequeue()
		if item == nil {
			break // Queue empty
		}
		
		job := item.(Job)
		processed++
		
		// Submit to worker pool
		ojq.workers.Submit(func() {
			result := ojq.executeJob(job)
			
			// Non-blocking result send
			select {
			case ojq.results <- result:
				atomic.AddUint64(&ojq.completed, 1)
			default:
				atomic.AddUint64(&ojq.failed, 1)
			}
		})
	}
	
	atomic.AddUint64(&ojq.processed, uint64(processed))
}

func (ojq *OptimalJobQueue) executeJob(job Job) Result {
	// Simulate HTTP request - replace with actual implementation
	start := time.Now()
	
	// TODO: Implement actual HTTP client with connection pooling
	time.Sleep(time.Millisecond * 10) // Simulate network call
	
	duration := time.Since(start)
	
	var status string
	var err error
	
	if duration > time.Millisecond*100 {
		status = "timeout"
		err = fmt.Errorf("request timeout after %v", duration)
	} else if duration > time.Millisecond*50 {
		status = "failed"
		err = fmt.Errorf("request failed after %v", duration)
	} else {
		status = "success"
	}
	
	return Result{
		Entity: job.Entity,
		JobID:  job.JobID,
		Error:  err,
		Status: status,
	}
}

func (ojq *OptimalJobQueue) GetResults(maxResults int) []Result {
	results := resultSlicePool.Get()
	results = results[:0] // Reset length, keep capacity
	
	for len(results) < maxResults {
		select {
		case result := <-ojq.results:
			results = append(results, result)
		default:
			break
		}
	}
	
	return results
}

func (ojq *OptimalJobQueue) GetMetrics() (enqueued, processed, failed, completed uint64) {
	return atomic.LoadUint64(&ojq.enqueued),
		   atomic.LoadUint64(&ojq.processed),
		   atomic.LoadUint64(&ojq.failed),
		   atomic.LoadUint64(&ojq.completed)
}

func (ojq *OptimalJobQueue) Close() {
	ojq.workers.StopAndWait()
	close(ojq.results)
}

// ===== OPTIMAL MONITOR SYSTEM (Ark Best Practices) =====

type OptimalMonitorSystem struct {
	world     *ecs.World
	monitors  *ecs.Map1[MonitorState]
	
	// Job processing
	jobQueue  *OptimalJobQueue
	
	// Batch processing buffers (reused via pools)
	batchSize int
	
	// Atomic counters for metrics
	cycleCount     uint64
	entitiesProcessed uint64
	jobsEnqueued   uint64
	resultsProcessed uint64
	
	// Configuration
	updateInterval time.Duration
	maxJobsPerCycle int
	
	logger Logger
}

func NewOptimalMonitorSystem(world *ecs.World, workerCount int, logger Logger) *OptimalMonitorSystem {
	return &OptimalMonitorSystem{
		world:           world,
		monitors:        ecs.NewMap1[MonitorState](world),
		jobQueue:        NewOptimalJobQueue(workerCount, 1000000), // 1M queue capacity
		batchSize:       100000, // Large batches for Ark optimization
		updateInterval:  10 * time.Millisecond,
		maxJobsPerCycle: 50000,
		logger:          logger,
	}
}

// Bulk create monitors using Ark's batch operations
func (oms *OptimalMonitorSystem) CreateMonitors(configs []MonitorConfig) error {
	start := time.Now()
	
	// Pre-allocate entities and states
	entities := make([]ecs.Entity, len(configs))
	states := make([]*MonitorState, len(configs))
	
	now := time.Now()
	for i, config := range configs {
		entities[i] = oms.world.NewEntity()
		
		state := monitorStatePool.Get()
		*state = MonitorState{
			URL:       config.URL,
			Name:      config.Name,
			Interval:  config.Interval,
			NextCheck: now.Add(time.Duration(i) * time.Millisecond), // Stagger
			Phase:     PhaseReady,
			Status:    "unknown",
			MaxFailures: config.MaxFailures,
		}
		
		// Set flags based on config
		if config.FirstCheck {
			state.SetFlag(FlagFirstCheck)
		}
		if config.YellowCode {
			state.SetFlag(FlagYellowCode)
		}
		if config.InterventionEnabled {
			state.SetFlag(FlagInterventionEnabled)
		}
		
		states[i] = state
	}
	
	// Bulk add components - 11x faster than individual adds
	oms.monitors.AddBatch(entities, states...)
	
	oms.logger.Info("Created %d monitors in %v using Ark batch operations", 
		len(configs), time.Since(start))
	
	return nil
}

// Main update loop - optimized for 1M+ monitors
func (oms *OptimalMonitorSystem) Update(ctx context.Context) error {
	start := time.Now()
	cycleNum := atomic.AddUint64(&oms.cycleCount, 1)
	
	// Process results first to free up workers
	oms.processResults()
	
	// Process monitors that need checking
	oms.processMonitors()
	
	// Process queued jobs
	oms.jobQueue.ProcessBatch(oms.maxJobsPerCycle)
	
	// Log metrics periodically
	if cycleNum%1000 == 0 {
		oms.logMetrics(time.Since(start), cycleNum)
	}
	
	return nil
}

// Process monitors using optimal Ark patterns
func (oms *OptimalMonitorSystem) processMonitors() {
	now := time.Now()
	
	// Get reusable entity slice from pool
	readyEntities := entitySlicePool.Get()
	readyEntities = readyEntities[:0] // Reset length, keep capacity
	defer entitySlicePool.Put(readyEntities)
	
	// Single query with simple filter - no complex Without() clauses
	query := oms.monitors.Query(oms.world)
	defer query.Close()
	
	// Fast iteration - Ark's strength
	for query.Next() {
		entity := query.Entity()
		state := query.Get()
		
		// Efficient check using consolidated state
		if state.NeedsPulse(now) {
			readyEntities = append(readyEntities, entity)
			
			// Process in large batches
			if len(readyEntities) >= oms.batchSize {
				oms.processBatch(readyEntities, now)
				readyEntities = readyEntities[:0] // Reset for reuse
			}
		}
	}
	
	// Process final batch
	if len(readyEntities) > 0 {
		oms.processBatch(readyEntities, now)
	}
}

// Batch processing with optimal Ark operations
func (oms *OptimalMonitorSystem) processBatch(entities []ecs.Entity, now time.Time) {
	if len(entities) == 0 {
		return
	}
	
	// Get reusable job slice from pool
	jobs := jobSlicePool.Get()
	jobs = jobs[:0]
	defer jobSlicePool.Put(jobs)
	
	// Create jobs for batch
	jobIDCounter := atomic.AddUint64(&oms.jobsEnqueued, uint64(len(entities)))
	for i, entity := range entities {
		state := oms.monitors.Get(entity)
		jobID := jobIDCounter - uint64(len(entities)) + uint64(i) + 1
		
		jobs = append(jobs, Job{
			Entity: entity,
			URL:    state.URL,
			JobID:  jobID,
		})
	}
	
	// Enqueue jobs to lock-free queue
	enqueued := oms.jobQueue.EnqueueBatch(jobs)
	
	// Bulk update states using Ark's MapBatchFn - 11x faster!
	oms.updateStatesBatch(entities, now, jobIDCounter)
	
	atomic.AddUint64(&oms.entitiesProcessed, uint64(enqueued))
}

// Bulk state update using Ark's batch operations
func (oms *OptimalMonitorSystem) updateStatesBatch(entities []ecs.Entity, now time.Time, jobIDStart uint64) {
	// Use Ark's MapBatchFn - the fastest way to update multiple entities
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, state *MonitorState) {
		// Calculate job ID for this entity
		entityIndex := 0
		for i, e := range entities {
			if e == entity {
				entityIndex = i
				break
			}
		}
		
		// Update state efficiently
		state.Phase = PhasePulsing
		state.JobID = jobIDStart - uint64(len(entities)) + uint64(entityIndex) + 1
		state.LastCheck = now
		state.NextCheck = now.Add(state.Interval)
		
		// Clear first check flag if set
		if state.HasFlag(FlagFirstCheck) {
			state.ClearFlag(FlagFirstCheck)
		}
	})
}

// Process results in bulk
func (oms *OptimalMonitorSystem) processResults() {
	results := oms.jobQueue.GetResults(10000)
	if len(results) == 0 {
		return
	}
	
	defer resultSlicePool.Put(results) // Return to pool
	
	// Group results by entity for batch processing
	entityResults := make(map[ecs.Entity]Result, len(results))
	completedEntities := entitySlicePool.Get()
	completedEntities = completedEntities[:0]
	defer entitySlicePool.Put(completedEntities)
	
	for _, result := range results {
		if oms.world.Alive(result.Entity) {
			entityResults[result.Entity] = result
			completedEntities = append(completedEntities, result.Entity)
		}
	}
	
	if len(completedEntities) > 0 {
		// Bulk update completed monitors using Ark's batch operations
		oms.updateCompletedMonitors(completedEntities, entityResults)
		atomic.AddUint64(&oms.resultsProcessed, uint64(len(completedEntities)))
	}
}

// Bulk update completed monitors
func (oms *OptimalMonitorSystem) updateCompletedMonitors(
	entities []ecs.Entity,
	results map[ecs.Entity]Result) {
	
	// Use Ark's MapBatchFn for maximum performance
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, state *MonitorState) {
		result, exists := results[entity]
		if !exists {
			return
		}
		
		// Clear job ID and update phase
		state.JobID = 0
		state.Phase = PhaseReady
		
		if result.Error != nil {
			// Handle failure
			state.Status = result.Status
			state.LastError = result.Error
			state.ConsecutiveFailures++
			
			// Set yellow code flag on first failure
			if state.ConsecutiveFailures == 1 && state.HasFlag(FlagYellowCode) {
				// Schedule code job (implement as needed)
			}
			
			// Check for intervention needed
			if state.ConsecutiveFailures%state.MaxFailures == 0 && 
			   state.HasFlag(FlagInterventionEnabled) {
				state.Phase = PhaseIntervening
				// Schedule intervention (implement as needed)
			}
		} else {
			// Handle success
			lastStatus := state.Status
			state.Status = result.Status
			state.LastError = nil
			state.ConsecutiveFailures = 0
			state.LastSuccessTime = time.Now()
			
			// Set green code flag if recovering from failure
			if lastStatus != "success" && lastStatus != "" {
				// Schedule green code (implement as needed)
			}
		}
	})
}

func (oms *OptimalMonitorSystem) logMetrics(processingTime time.Duration, cycleNum uint64) {
	enqueued, processed, failed, completed := oms.jobQueue.GetMetrics()
	
	oms.logger.Info("Cycle %d: entities=%d, jobs_enqueued=%d, jobs_processed=%d, jobs_completed=%d, jobs_failed=%d, results=%d, cycle_time=%v",
		cycleNum,
		atomic.LoadUint64(&oms.entitiesProcessed),
		enqueued,
		processed,
		completed,
		failed,
		atomic.LoadUint64(&oms.resultsProcessed),
		processingTime)
}

func (oms *OptimalMonitorSystem) GetMetrics() SystemMetrics {
	enqueued, processed, failed, completed := oms.jobQueue.GetMetrics()
	
	return SystemMetrics{
		CycleCount:        atomic.LoadUint64(&oms.cycleCount),
		EntitiesProcessed: atomic.LoadUint64(&oms.entitiesProcessed),
		JobsEnqueued:      enqueued,
		JobsProcessed:     processed,
		JobsCompleted:     completed,
		JobsFailed:        failed,
		ResultsProcessed:  atomic.LoadUint64(&oms.resultsProcessed),
	}
}

func (oms *OptimalMonitorSystem) Close() {
	oms.jobQueue.Close()
}

// ===== SUPPORTING TYPES =====

type MonitorConfig struct {
	URL                 string
	Name                string
	Interval            time.Duration
	MaxFailures         int
	FirstCheck          bool
	YellowCode          bool
	InterventionEnabled bool
}

type SystemMetrics struct {
	CycleCount        uint64
	EntitiesProcessed uint64
	JobsEnqueued      uint64
	JobsProcessed     uint64
	JobsCompleted     uint64
	JobsFailed        uint64
	ResultsProcessed  uint64
}

type Logger interface {
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// ===== MAIN FUNCTION EXAMPLE =====

func main() {
	// Create ECS world
	world := ecs.NewWorld()
	
	// Create logger
	logger := &SimpleLogger{}
	
	// Create optimal monitor system
	system := NewOptimalMonitorSystem(&world, 1000, logger)
	defer system.Close()
	
	// Create 1M monitors
	configs := make([]MonitorConfig, 1000000)
	for i := range configs {
		configs[i] = MonitorConfig{
			URL:                 fmt.Sprintf("https://example.com/monitor/%d", i),
			Name:                fmt.Sprintf("monitor-%d", i),
			Interval:            time.Duration(10+rand.Intn(20)) * time.Second,
			MaxFailures:         3,
			FirstCheck:          true,
			YellowCode:          true,
			InterventionEnabled: true,
		}
	}
	
	// Bulk create monitors
	start := time.Now()
	err := system.CreateMonitors(configs)
	if err != nil {
		log.Fatal(err)
	}
	logger.Info("Created 1M monitors in %v", time.Since(start))
	
	// Run system
	ctx := context.Background()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	
	// Performance monitoring
	metricsTimer := time.NewTicker(10 * time.Second)
	defer metricsTimer.Stop()
	
	for {
		select {
		case <-ticker.C:
			err := system.Update(ctx)
			if err != nil {
				logger.Error("System update error: %v", err)
			}
			
		case <-metricsTimer.C:
			metrics := system.GetMetrics()
			logger.Info("System metrics: %+v", metrics)
			
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

