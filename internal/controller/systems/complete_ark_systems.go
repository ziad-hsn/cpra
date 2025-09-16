// Package systems provides complete ECS systems using proper Ark batch operations
// Implements all required systems: Pulse, Intervention, Code, and Result processing
package systems

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/workers/workerspool"
)

// =============================================================================
// PULSE SYSTEM - Using proper Ark batch operations
// =============================================================================

// ArkBatchPulseScheduleSystem schedules pulse checks using Ark batch operations
type ArkBatchPulseScheduleSystem struct {
	world  *ecs.World
	queue  queue.QueueInterface
	
	// Cached filters (Ark performance optimization)
	readyFilter *generic.Filter1[components.MonitorState]
	
	// Performance tracking
	lastScheduled uint64
	totalDropped  uint64
}

// NewArkBatchPulseScheduleSystem creates a new Ark-based pulse schedule system
func NewArkBatchPulseScheduleSystem(world *ecs.World, queue queue.QueueInterface) *ArkBatchPulseScheduleSystem {
	return &ArkBatchPulseScheduleSystem{
		world: world,
		queue: queue,
	}
}

// Update schedules pulse checks using Ark's efficient batch operations
func (s *ArkBatchPulseScheduleSystem) Update() {
	now := time.Now()
	
	// Create cached filter if needed (Ark best practice)
	if s.readyFilter == nil {
		s.readyFilter = generic.NewFilter1[components.MonitorState](s.world).Register()
	}
	
	// Collect entities that need pulse checks
	var entitiesToSchedule []ecs.Entity
	var jobsToEnqueue []queue.Job
	
	query := s.readyFilter.Query()
	defer query.Close()
	
	for query.Next() {
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if ready for pulse and not already processing
		if monitor.IsReady() && now.After(monitor.NextCheck) {
			entitiesToSchedule = append(entitiesToSchedule, entity)
			
			job := queue.Job{
				EntityID: entity,
				URL:      monitor.URL,
				Method:   monitor.Method,
				Timeout:  int64(monitor.Timeout.Milliseconds()),
				JobType:  queue.JobTypePulse,
			}
			jobsToEnqueue = append(jobsToEnqueue, job)
		}
	}
	
	if len(entitiesToSchedule) == 0 {
		return
	}
	
	// Try to enqueue jobs first
	enqueued := s.queue.EnqueueBatch(jobsToEnqueue)
	
	if enqueued < len(jobsToEnqueue) {
		// Queue full - track dropped jobs
		dropped := uint64(len(jobsToEnqueue) - enqueued)
		atomic.AddUint64(&s.totalDropped, dropped)
		fmt.Printf("Warning: Pulse queue full, dropped %d jobs\n", dropped)
	}
	
	// CRITICAL: Only update entities whose jobs were successfully enqueued
	if enqueued > 0 {
		successfulEntities := entitiesToSchedule[:enqueued]
		
		// Use Ark's efficient batch update
		mapper := generic.NewMap1[components.MonitorState](s.world)
		mapper.MapBatchFn(successfulEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			monitor.SetPulsePending()
			monitor.LastCheck = now
		})
	}
	
	atomic.StoreUint64(&s.lastScheduled, uint64(enqueued))
}

// =============================================================================
// INTERVENTION SYSTEM - Using proper Ark batch operations
// =============================================================================

// ArkBatchInterventionSystem handles interventions using Ark batch operations
type ArkBatchInterventionSystem struct {
	world  *ecs.World
	queue  queue.QueueInterface
	
	// Cached filters
	failedFilter *generic.Filter1[components.MonitorState]
	
	// Performance tracking
	lastScheduled uint64
}

// NewArkBatchInterventionSystem creates a new intervention system
func NewArkBatchInterventionSystem(world *ecs.World, queue queue.QueueInterface) *ArkBatchInterventionSystem {
	return &ArkBatchInterventionSystem{
		world: world,
		queue: queue,
	}
}

// Update schedules interventions for failed monitors
func (s *ArkBatchInterventionSystem) Update() {
	// Create cached filter if needed
	if s.failedFilter == nil {
		s.failedFilter = generic.NewFilter1[components.MonitorState](s.world).Register()
	}
	
	var entitiesToIntervene []ecs.Entity
	var jobsToEnqueue []queue.Job
	
	query := s.failedFilter.Query()
	defer query.Close()
	
	for query.Next() {
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if intervention is needed and not already processing
		if monitor.IsFailed() && monitor.ShouldTriggerIntervention() && !monitor.IsInterventionPending() {
			entitiesToIntervene = append(entitiesToIntervene, entity)
			
			job := queue.Job{
				EntityID: entity,
				URL:      monitor.URL,
				Method:   monitor.Method,
				Timeout:  int64(monitor.Timeout.Milliseconds()),
				JobType:  queue.JobTypeIntervention,
			}
			jobsToEnqueue = append(jobsToEnqueue, job)
		}
	}
	
	if len(entitiesToIntervene) == 0 {
		return
	}
	
	// Enqueue intervention jobs
	enqueued := s.queue.EnqueueBatch(jobsToEnqueue)
	
	// Update entity states using Ark batch operations
	if enqueued > 0 {
		successfulEntities := entitiesToIntervene[:enqueued]
		
		mapper := generic.NewMap1[components.MonitorState](s.world)
		mapper.MapBatchFn(successfulEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			monitor.SetInterventionPending()
			monitor.LastIntervention = time.Now()
		})
	}
	
	atomic.StoreUint64(&s.lastScheduled, uint64(enqueued))
}

// =============================================================================
// CODE/ALERT SYSTEM - Using proper Ark batch operations
// =============================================================================

// ArkBatchCodeSystem handles alert notifications using Ark batch operations
type ArkBatchCodeSystem struct {
	world  *ecs.World
	queue  queue.QueueInterface
	
	// Cached filters
	alertFilter *generic.Filter1[components.MonitorState]
	
	// Performance tracking
	lastScheduled uint64
}

// NewArkBatchCodeSystem creates a new code/alert system
func NewArkBatchCodeSystem(world *ecs.World, queue queue.QueueInterface) *ArkBatchCodeSystem {
	return &ArkBatchCodeSystem{
		world: world,
		queue: queue,
	}
}

// Update schedules alert notifications
func (s *ArkBatchCodeSystem) Update() {
	// Create cached filter if needed
	if s.alertFilter == nil {
		s.alertFilter = generic.NewFilter1[components.MonitorState](s.world).Register()
	}
	
	var entitiesToAlert []ecs.Entity
	var jobsToEnqueue []queue.Job
	
	query := s.alertFilter.Query()
	defer query.Close()
	
	for query.Next() {
		entity := query.Entity()
		monitor := query.Get()
		
		// Check if alert is needed and not already processing
		if monitor.IsFailed() && monitor.ShouldTriggerAlert() && !monitor.IsCodePending() {
			entitiesToAlert = append(entitiesToAlert, entity)
			
			job := queue.Job{
				EntityID: entity,
				URL:      monitor.URL,
				Method:   monitor.Method,
				Timeout:  int64(monitor.Timeout.Milliseconds()),
				JobType:  queue.JobTypeCode,
			}
			jobsToEnqueue = append(jobsToEnqueue, job)
		}
	}
	
	if len(entitiesToAlert) == 0 {
		return
	}
	
	// Enqueue alert jobs
	enqueued := s.queue.EnqueueBatch(jobsToEnqueue)
	
	// Update entity states using Ark batch operations
	if enqueued > 0 {
		successfulEntities := entitiesToAlert[:enqueued]
		
		mapper := generic.NewMap1[components.MonitorState](s.world)
		mapper.MapBatchFn(successfulEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			monitor.SetCodePending()
			monitor.LastAlert = time.Now()
		})
	}
	
	atomic.StoreUint64(&s.lastScheduled, uint64(enqueued))
}

// =============================================================================
// RESULT PROCESSING SYSTEM - Using proper Ark batch operations
// =============================================================================

// JobResult represents the result of a completed job
type JobResult struct {
	EntityID     ecs.Entity
	JobType      queue.JobType
	StatusCode   int
	ResponseTime time.Duration
	Success      bool
	Error        error
}

// ArkBatchResultSystem processes job results using Ark batch operations
type ArkBatchResultSystem struct {
	world      *ecs.World
	workerPool *workerspool.WorkersPool
	
	// Result processing
	resultsChan chan []JobResult
	
	// Performance tracking
	lastProcessed uint64
}

// NewArkBatchResultSystem creates a new result processing system
func NewArkBatchResultSystem(world *ecs.World, workerPool *workerspool.WorkersPool) *ArkBatchResultSystem {
	return &ArkBatchResultSystem{
		world:       world,
		workerPool:  workerPool,
		resultsChan: make(chan []JobResult, 100), // Buffered for burst handling
	}
}

// Update processes completed job results
func (s *ArkBatchResultSystem) Update() {
	// Non-blocking result collection
	select {
	case results := <-s.resultsChan:
		s.processResults(results)
		atomic.StoreUint64(&s.lastProcessed, uint64(len(results)))
	default:
		// No results available
	}
}

// processResults updates entity states based on job results using Ark batch operations
func (s *ArkBatchResultSystem) processResults(results []JobResult) {
	if len(results) == 0 {
		return
	}
	
	// Group results by job type for efficient processing
	pulseResults := make(map[ecs.Entity]JobResult)
	interventionResults := make(map[ecs.Entity]JobResult)
	codeResults := make(map[ecs.Entity]JobResult)
	
	var pulseEntities []ecs.Entity
	var interventionEntities []ecs.Entity
	var codeEntities []ecs.Entity
	
	for _, result := range results {
		switch result.JobType {
		case queue.JobTypePulse:
			pulseResults[result.EntityID] = result
			pulseEntities = append(pulseEntities, result.EntityID)
		case queue.JobTypeIntervention:
			interventionResults[result.EntityID] = result
			interventionEntities = append(interventionEntities, result.EntityID)
		case queue.JobTypeCode:
			codeResults[result.EntityID] = result
			codeEntities = append(codeEntities, result.EntityID)
		}
	}
	
	mapper := generic.NewMap1[components.MonitorState](s.world)
	
	// Process pulse results using Ark batch operations
	if len(pulseEntities) > 0 {
		mapper.MapBatchFn(pulseEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			if result, exists := pulseResults[entity]; exists {
				monitor.UpdatePulseResult(result.StatusCode, result.ResponseTime, result.Error)
			}
		})
	}
	
	// Process intervention results using Ark batch operations
	if len(interventionEntities) > 0 {
		mapper.MapBatchFn(interventionEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			if result, exists := interventionResults[entity]; exists {
				monitor.UpdateInterventionResult(result.Success)
			}
		})
	}
	
	// Process code/alert results using Ark batch operations
	if len(codeEntities) > 0 {
		mapper.MapBatchFn(codeEntities, func(entity ecs.Entity, monitor *components.MonitorState) {
			if result, exists := codeResults[entity]; exists {
				monitor.UpdateAlertResult(result.Success)
			}
		})
	}
}

// SubmitResults allows workers to submit completed job results
func (s *ArkBatchResultSystem) SubmitResults(results []JobResult) {
	select {
	case s.resultsChan <- results:
		// Results submitted successfully
	default:
		// Channel full, log warning
		fmt.Printf("Warning: Result channel full, dropping %d results\n", len(results))
	}
}

// =============================================================================
// COMPLETE SYSTEM ORCHESTRATOR
// =============================================================================

// ArkSystemOrchestrator manages all systems using proper Ark patterns
type ArkSystemOrchestrator struct {
	world *ecs.World
	
	// All systems
	pulseSchedule  *ArkBatchPulseScheduleSystem
	intervention   *ArkBatchInterventionSystem
	code           *ArkBatchCodeSystem
	result         *ArkBatchResultSystem
	
	// Configuration
	updateInterval time.Duration
	
	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewArkSystemOrchestrator creates a complete system orchestrator
func NewArkSystemOrchestrator(world *ecs.World, queue queue.QueueInterface, workerPool *workerspool.WorkersPool) *ArkSystemOrchestrator {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &ArkSystemOrchestrator{
		world:          world,
		pulseSchedule:  NewArkBatchPulseScheduleSystem(world, queue),
		intervention:   NewArkBatchInterventionSystem(world, queue),
		code:           NewArkBatchCodeSystem(world, queue),
		result:         NewArkBatchResultSystem(world, workerPool),
		updateInterval: time.Millisecond * 100, // 10Hz update rate
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Start begins the system update loop
func (o *ArkSystemOrchestrator) Start() {
	o.wg.Add(1)
	go o.updateLoop()
}

// updateLoop runs the main system update cycle
func (o *ArkSystemOrchestrator) updateLoop() {
	defer o.wg.Done()
	
	ticker := time.NewTicker(o.updateInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			// Update systems in the correct order (following CLAUDE.md)
			o.pulseSchedule.Update()
			o.intervention.Update()
			o.code.Update()
			o.result.Update()
		}
	}
}

// Stop gracefully shuts down the orchestrator
func (o *ArkSystemOrchestrator) Stop() {
	o.cancel()
	o.wg.Wait()
}

// Stats returns performance statistics from all systems
func (o *ArkSystemOrchestrator) Stats() SystemStats {
	return SystemStats{
		PulseScheduled:       atomic.LoadUint64(&o.pulseSchedule.lastScheduled),
		PulseDropped:         atomic.LoadUint64(&o.pulseSchedule.totalDropped),
		InterventionScheduled: atomic.LoadUint64(&o.intervention.lastScheduled),
		CodeScheduled:        atomic.LoadUint64(&o.code.lastScheduled),
		ResultsProcessed:     atomic.LoadUint64(&o.result.lastProcessed),
	}
}

// SystemStats provides comprehensive system performance metrics
type SystemStats struct {
	PulseScheduled        uint64 `json:"pulse_scheduled"`
	PulseDropped          uint64 `json:"pulse_dropped"`
	InterventionScheduled uint64 `json:"intervention_scheduled"`
	CodeScheduled         uint64 `json:"code_scheduled"`
	ResultsProcessed      uint64 `json:"results_processed"`
}

