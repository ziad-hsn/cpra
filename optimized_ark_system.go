package systems

import (
	"context"
	"sync/atomic"
	"time"

	"cpra/internal/jobs"
	"cpra/internal/queue"
	"github.com/mlange-42/ark/ecs"
)

// Optimized components - minimal state changes
type Monitor struct {
	URL           string
	Interval      time.Duration
	LastCheck     time.Time
	NextCheck     time.Time
	Status        MonitorStatus
	JobID         uint64 // 0 = not processing, >0 = job ID
	RetryCount    int
	ErrorMessage  string
}

type ActiveJob struct {
	JobID     uint64
	StartTime time.Time
}

type MonitorStatus int

const (
	MonitorStatusUnknown MonitorStatus = iota
	MonitorStatusSuccess
	MonitorStatusFailed
	MonitorStatusTimeout
)

// OptimizedMonitorSystem - designed for 1M+ monitors using proper Ark patterns
type OptimizedMonitorSystem struct {
	world   *ecs.World
	
	// Component mappers
	monitors   *ecs.Map1[Monitor]
	activeJobs *ecs.Map1[ActiveJob]
	
	// Queue and workers
	queue      *queue.LockFreeQueue
	resultChan <-chan jobs.Result
	
	// Configuration
	batchSize       int           // Large batches (100K+)
	maxJobsPerCycle int           // Limit jobs per update cycle
	
	// Metrics
	processedCount  uint64
	enqueuedCount   uint64
	droppedCount    uint64
	completedCount  uint64
	
	logger SystemLogger
}

func NewOptimizedMonitorSystem(
	world *ecs.World,
	queue *queue.LockFreeQueue,
	resultChan <-chan jobs.Result,
	logger SystemLogger) *OptimizedMonitorSystem {
	
	return &OptimizedMonitorSystem{
		world:           world,
		monitors:        ecs.NewMap1[Monitor](world),
		activeJobs:      ecs.NewMap1[ActiveJob](world),
		queue:           queue,
		resultChan:      resultChan,
		batchSize:       100000, // Large batches for 1M monitors
		maxJobsPerCycle: 50000,  // Limit to prevent overwhelming queue
		logger:          logger,
	}
}

// CreateMonitors - Bulk create 1M monitors using Ark's batch operations
func (oms *OptimizedMonitorSystem) CreateMonitors(monitorConfigs []MonitorConfig) error {
	start := time.Now()
	
	// Use Ark's bulk entity creation - 5x faster than individual
	entities := make([]ecs.Entity, len(monitorConfigs))
	monitors := make([]*Monitor, len(monitorConfigs))
	
	// Prepare monitor components
	now := time.Now()
	for i, config := range monitorConfigs {
		entities[i] = oms.world.NewEntity()
		monitors[i] = &Monitor{
			URL:       config.URL,
			Interval:  config.Interval,
			LastCheck: time.Time{}, // Never checked
			NextCheck: now,         // Check immediately
			Status:    MonitorStatusUnknown,
			JobID:     0,
		}
	}
	
	// Bulk add components - 11x faster than individual adds
	oms.monitors.AddBatch(entities, monitors...)
	
	oms.logger.Info("Created %d monitors in %v using Ark batch operations", 
		len(monitorConfigs), time.Since(start))
	
	return nil
}

// Update - Main system update using proper Ark patterns
func (oms *OptimizedMonitorSystem) Update(ctx context.Context) error {
	start := time.Now()
	
	// Process results first to free up active jobs
	oms.processResults()
	
	// Process monitors that need checking
	oms.processMonitors()
	
	// Log performance metrics periodically
	if atomic.LoadUint64(&oms.processedCount)%10000 == 0 {
		oms.logMetrics(time.Since(start))
	}
	
	return nil
}

// processMonitors - Bulk process monitors using Ark's fast iteration
func (oms *OptimizedMonitorSystem) processMonitors() {
	now := time.Now()
	
	// Pre-allocate slices for bulk operations
	readyEntities := make([]ecs.Entity, 0, oms.batchSize)
	readyJobs := make([]jobs.Job, 0, oms.batchSize)
	jobsThisCycle := 0
	
	// Single pass through ALL monitors - leverages Ark's cache-friendly iteration
	query := oms.monitors.Query(oms.world)
	defer query.Close()
	
	for query.Next() {
		if jobsThisCycle >= oms.maxJobsPerCycle {
			break // Limit jobs per cycle to prevent overwhelming
		}
		
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if monitor needs pulse (no component changes yet!)
		if oms.shouldPulseMonitor(monitor, now) {
			job := jobs.NewPulseJob(entity, monitor.URL)
			
			readyEntities = append(readyEntities, entity)
			readyJobs = append(readyJobs, job)
			jobsThisCycle++
			
			// Process in large batches for optimal performance
			if len(readyJobs) >= oms.batchSize {
				oms.processBulkBatch(readyEntities, readyJobs, now)
				readyEntities = readyEntities[:0]
				readyJobs = readyJobs[:0]
			}
		}
	}
	
	// Process final batch
	if len(readyJobs) > 0 {
		oms.processBulkBatch(readyEntities, readyJobs, now)
	}
	
	atomic.AddUint64(&oms.processedCount, uint64(jobsThisCycle))
}

func (oms *OptimizedMonitorSystem) shouldPulseMonitor(monitor *Monitor, now time.Time) bool {
	return now.After(monitor.NextCheck) && monitor.JobID == 0
}

// processBulkBatch - Bulk enqueue and state update using Ark batch operations
func (oms *OptimizedMonitorSystem) processBulkBatch(
	entities []ecs.Entity,
	jobs []jobs.Job,
	now time.Time) {
	
	if len(entities) == 0 {
		return
	}
	
	// Bulk enqueue jobs
	successfulEntities := make([]ecs.Entity, 0, len(entities))
	successfulJobIDs := make([]uint64, 0, len(entities))
	
	for i, job := range jobs {
		if jobID := oms.queue.Enqueue(job); jobID > 0 {
			successfulEntities = append(successfulEntities, entities[i])
			successfulJobIDs = append(successfulJobIDs, jobID)
		} else {
			atomic.AddUint64(&oms.droppedCount, 1)
		}
	}
	
	if len(successfulEntities) == 0 {
		return // No jobs enqueued successfully
	}
	
	atomic.AddUint64(&oms.enqueuedCount, uint64(len(successfulEntities)))
	
	// Bulk update monitor states using Ark's batch operations
	oms.updateMonitorsBatch(successfulEntities, successfulJobIDs, now)
	
	// Bulk add ActiveJob components
	oms.addActiveJobsBatch(successfulEntities, successfulJobIDs, now)
}

// updateMonitorsBatch - Use Ark's batch function for bulk updates
func (oms *OptimizedMonitorSystem) updateMonitorsBatch(
	entities []ecs.Entity,
	jobIDs []uint64,
	now time.Time) {
	
	// Create a map for fast lookup
	jobIDMap := make(map[ecs.Entity]uint64, len(entities))
	for i, entity := range entities {
		jobIDMap[entity] = jobIDs[i]
	}
	
	// Use Ark's batch function - 5x faster than individual updates
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, monitor *Monitor) {
		if jobID, exists := jobIDMap[entity]; exists {
			monitor.JobID = jobID
			monitor.LastCheck = now
			monitor.NextCheck = now.Add(monitor.Interval)
		}
	})
}

// addActiveJobsBatch - Bulk add ActiveJob components
func (oms *OptimizedMonitorSystem) addActiveJobsBatch(
	entities []ecs.Entity,
	jobIDs []uint64,
	now time.Time) {
	
	// Prepare ActiveJob components
	activeJobs := make([]*ActiveJob, len(entities))
	for i, jobID := range jobIDs {
		activeJobs[i] = &ActiveJob{
			JobID:     jobID,
			StartTime: now,
		}
	}
	
	// Bulk add components - 11x faster than individual adds
	oms.activeJobs.AddBatch(entities, activeJobs...)
}

// processResults - Bulk process job results
func (oms *OptimizedMonitorSystem) processResults() {
	// Collect all available results
	results := make([]jobs.Result, 0, 10000)
	
	// Non-blocking collection
	for len(results) < cap(results) {
		select {
		case result := <-oms.resultChan:
			results = append(results, result)
		default:
			break
		}
	}
	
	if len(results) == 0 {
		return
	}
	
	// Group results by entity for bulk processing
	entityResults := make(map[ecs.Entity]jobs.Result, len(results))
	completedEntities := make([]ecs.Entity, 0, len(results))
	
	for _, result := range results {
		entity := result.Entity()
		if oms.world.Alive(entity) {
			entityResults[entity] = result
			completedEntities = append(completedEntities, entity)
		}
	}
	
	if len(completedEntities) == 0 {
		return
	}
	
	// Bulk update monitor states
	oms.updateCompletedMonitors(completedEntities, entityResults)
	
	// Bulk remove ActiveJob components - 9x faster than individual removes
	oms.activeJobs.RemoveBatch(completedEntities, nil)
	
	atomic.AddUint64(&oms.completedCount, uint64(len(completedEntities)))
}

// updateCompletedMonitors - Bulk update using Ark's batch operations
func (oms *OptimizedMonitorSystem) updateCompletedMonitors(
	entities []ecs.Entity,
	results map[ecs.Entity]jobs.Result) {
	
	// Use Ark's batch function for bulk updates
	oms.monitors.MapBatchFn(entities, func(entity ecs.Entity, monitor *Monitor) {
		result, exists := results[entity]
		if !exists {
			return
		}
		
		// Clear job ID
		monitor.JobID = 0
		
		// Update status based on result
		if result.Error() != nil {
			monitor.Status = MonitorStatusFailed
			monitor.ErrorMessage = result.Error().Error()
			monitor.RetryCount++
		} else {
			monitor.Status = MonitorStatusSuccess
			monitor.ErrorMessage = ""
			monitor.RetryCount = 0
		}
	})
}

// GetMetrics - Return current system metrics
func (oms *OptimizedMonitorSystem) GetMetrics() SystemMetrics {
	return SystemMetrics{
		ProcessedCount: atomic.LoadUint64(&oms.processedCount),
		EnqueuedCount:  atomic.LoadUint64(&oms.enqueuedCount),
		DroppedCount:   atomic.LoadUint64(&oms.droppedCount),
		CompletedCount: atomic.LoadUint64(&oms.completedCount),
	}
}

func (oms *OptimizedMonitorSystem) logMetrics(processingTime time.Duration) {
	metrics := oms.GetMetrics()
	
	var successRate float64
	if metrics.ProcessedCount > 0 {
		successRate = float64(metrics.EnqueuedCount) / float64(metrics.ProcessedCount) * 100
	}
	
	oms.logger.Info("Monitor system metrics: processed=%d, enqueued=%d, dropped=%d, completed=%d, success_rate=%.1f%%, process_time=%v",
		metrics.ProcessedCount,
		metrics.EnqueuedCount,
		metrics.DroppedCount,
		metrics.CompletedCount,
		successRate,
		processingTime)
}

// SetBatchSize - Allow runtime adjustment of batch size
func (oms *OptimizedMonitorSystem) SetBatchSize(size int) {
	if size > 0 {
		oms.batchSize = size
	}
}

// SetMaxJobsPerCycle - Limit jobs processed per update cycle
func (oms *OptimizedMonitorSystem) SetMaxJobsPerCycle(max int) {
	if max > 0 {
		oms.maxJobsPerCycle = max
	}
}

// Supporting types
type MonitorConfig struct {
	URL      string
	Interval time.Duration
}

type SystemMetrics struct {
	ProcessedCount uint64
	EnqueuedCount  uint64
	DroppedCount   uint64
	CompletedCount uint64
}

type SystemLogger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// TimeoutRecoverySystem - Recover stuck jobs using bulk operations
type TimeoutRecoverySystem struct {
	world      *ecs.World
	monitors   *ecs.Map1[Monitor]
	activeJobs *ecs.Map1[ActiveJob]
	timeout    time.Duration
	logger     SystemLogger
}

func NewTimeoutRecoverySystem(
	world *ecs.World,
	monitors *ecs.Map1[Monitor],
	activeJobs *ecs.Map1[ActiveJob],
	timeout time.Duration,
	logger SystemLogger) *TimeoutRecoverySystem {
	
	return &TimeoutRecoverySystem{
		world:      world,
		monitors:   monitors,
		activeJobs: activeJobs,
		timeout:    timeout,
		logger:     logger,
	}
}

func (trs *TimeoutRecoverySystem) Update(ctx context.Context) error {
	now := time.Now()
	timedOutEntities := make([]ecs.Entity, 0, 1000)
	
	// Find timed out jobs using Ark's fast iteration
	query := trs.activeJobs.Query(trs.world)
	defer query.Close()
	
	for query.Next() {
		entity := query.Entity()
		activeJob := query.Get()
		
		if now.Sub(activeJob.StartTime) > trs.timeout {
			timedOutEntities = append(timedOutEntities, entity)
		}
	}
	
	if len(timedOutEntities) == 0 {
		return nil
	}
	
	// Bulk recover timed out jobs
	trs.recoverTimedOutJobs(timedOutEntities)
	
	trs.logger.Warn("Recovered %d timed out jobs", len(timedOutEntities))
	return nil
}

func (trs *TimeoutRecoverySystem) recoverTimedOutJobs(entities []ecs.Entity) {
	// Bulk update monitor states
	trs.monitors.MapBatchFn(entities, func(entity ecs.Entity, monitor *Monitor) {
		monitor.JobID = 0
		monitor.Status = MonitorStatusTimeout
		monitor.ErrorMessage = "Job timed out"
	})
	
	// Bulk remove ActiveJob components
	trs.activeJobs.RemoveBatch(entities, nil)
}

