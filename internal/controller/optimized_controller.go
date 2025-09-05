package controller

import (
	"context"
	"cpra/internal/controller/systems"
	"cpra/internal/queue"
	"fmt"
	"time"

	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/loader/streaming"
	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// LoggerAdapter adapts the controller loggers to the  systems interface
type LoggerAdapter struct {
	logger interface {
		Info(format string, args ...interface{})
		Debug(format string, args ...interface{})
		Warn(format string, args ...interface{})
		Error(format string, args ...interface{})
		LogSystemPerformance(name string, duration time.Duration, count int)
		LogComponentState(entityID uint32, component string, action string)
	}
}

func (l *LoggerAdapter) Info(format string, args ...interface{}) {
	l.logger.Info(format, args...)
}

func (l *LoggerAdapter) Debug(format string, args ...interface{}) {
	l.logger.Debug(format, args...)
}

func (l *LoggerAdapter) Warn(format string, args ...interface{}) {
	l.logger.Warn(format, args...)
}

func (l *LoggerAdapter) Error(format string, args ...interface{}) {
	l.logger.Error(format, args...)
}

func (l *LoggerAdapter) LogSystemPerformance(name string, duration time.Duration, count int) {
	l.logger.LogSystemPerformance(name, duration, count)
}

func (l *LoggerAdapter) LogComponentState(entityID uint32, component string, action string) {
	l.logger.LogComponentState(entityID, component, action)
}

// Controller manages the  monitoring system using original queue approach
type Controller struct {
	// Core components
	world  *ecs.World
	mapper *entities.EntityManager

	// Queue system - use  components (NEVER blocks)
	queue          *queue.BoundedQueue
	batchProcessor *queue.BatchProcessor
	connPool       *queue.ConnectionPool
	workerPool     *queue.DynamicWorkerPool

	// ECS systems - same as original but with batching
	pulseScheduleSystem *systems.BatchPulseScheduleSystem
	pulseSystem         *systems.BatchPulseSystem
	interventionSystem  *systems.BatchInterventionSystem
	codeSystem          *systems.BatchCodeSystem

	// Result processing systems - read from original channels
	pulseResultSystem        *systems.BatchPulseResultSystem
	interventionResultSystem *systems.BatchInterventionResultSystem
	codeResultSystem         *systems.BatchCodeResultSystem

	// Configuration
	config Config

	// Metrics collection
	metricsAggregator *MetricsAggregator

	// State
	running bool
}

// Config holds all configuration for the  controller
type Config struct {
	// Streaming loader config
	StreamingConfig streaming.StreamingConfig

	// Queue config - use original QueueManager
	MonitorCount int // For calculating worker counts

	// System config - batching optimization
	BatchSize int

	// Performance config
	UpdateInterval time.Duration
	StatsInterval  time.Duration
}

// DefaultConfig returns  default configuration for 1M monitors
func DefaultConfig() Config {
	return Config{
		StreamingConfig: streaming.DefaultStreamingConfig(),

		// Use original queue manager that never blocks
		MonitorCount: 1000000,

		// Batch processing optimization - process more entities per system update
		BatchSize: 5000, // Process 5K entities per batch for speed

		UpdateInterval: 100 * time.Millisecond,
		StatsInterval:  10 * time.Second,
	}
}

// NewController creates a new  controller using the original queue approach
func NewController(config Config) *Controller {
	// Create ECS world using app tool like in main.go
	tool := app.New(1024).Seed(123)
	tool.TPS = 10000 // High TPS for 1M monitors

	// Initialize entity manager exactly like original systems
	mapper := entities.InitializeMappers(&tool.World)

	// Create  queue components - THESE NEVER BLOCK!

	ws := queue.WorldSummary{
		NumMonitors:                config.MonitorCount,
		AvgInterval:                10 * time.Second,
		MeanServicePulse:           120 * time.Millisecond,
		PulseFailProb:              0.02,
		MeanServiceIntervention:    300 * time.Millisecond,
		InterventionEscalationProb: 0.10,
		MeanServiceCode:            80 * time.Millisecond,
	}
	desiredWorkers, suggestedCap := queue.ComputeInitialSizingFromWorld(
		ws,
		0.75,
		1*time.Second,
		1, // minWorkers
		queue.DefaultWorkerPoolConfig().MaxWorkers,
		10_000,  // minQueue; avoid too small
		500_000, // maxQueue; cap to something sane
	)
	queueConfig := queue.BoundedQueueConfig{
		MaxSize:      suggestedCap,
		MaxBatch:     suggestedCap,
		BatchTimeout: 50 * time.Millisecond,
	}
	boundedQueue := queue.NewBoundedQueue(queueConfig)

	connPool := queue.NewConnectionPool(queue.DefaultPoolConfig())
	wpCfg := queue.DefaultWorkerPoolConfig()
	wpCfg.MinWorkers = desiredWorkers

	workerPool := queue.NewDynamicWorkerPool(wpCfg, WorkerPoolLogger)

	// Create result channels
	pulseResults := make(chan jobs.Result, suggestedCap)
	interventionResults := make(chan jobs.Result, suggestedCap)
	codeResults := make(chan jobs.Result, suggestedCap)

	batchProcessor := queue.NewBatchProcessor(boundedQueue, connPool, queue.ProcessorConfig{
		BatchSize:     suggestedCap,
		MaxConcurrent: 1000,
		Timeout:       30 * time.Second,
		RetryAttempts: 3,
		RetryDelay:    500 * time.Millisecond,
	}, WorkerPoolLogger, pulseResults, interventionResults, codeResults)

	// Create ECS systems using  components
	dispatchLogger := &LoggerAdapter{logger: DispatchLogger}
	schedulerLogger := &LoggerAdapter{logger: SchedulerLogger}
	pulseScheduleSystem := systems.NewBatchPulseScheduleSystem(&tool.World, mapper, config.BatchSize, schedulerLogger)
	pulseSystem := systems.NewBatchPulseSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)
	interventionSystem := systems.NewBatchInterventionSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)
	codeSystem := systems.NewBatchCodeSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)

	// Create result processing systems using result channels
	resultLogger := &LoggerAdapter{logger: ResultLogger}
	pulseResultSystem := systems.NewBatchPulseResultSystem(pulseResults, mapper, resultLogger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(interventionResults, mapper, resultLogger)
	codeResultSystem := systems.NewBatchCodeResultSystem(codeResults, mapper, resultLogger)

	// Initialize metrics aggregator
	metricsAggregator := NewMetricsAggregator()
	metricsAggregator.RegisterSystem("BatchPulseScheduleSystem")
	metricsAggregator.RegisterSystem("BatchPulseSystem")
	metricsAggregator.RegisterSystem("BatchInterventionSystem")
	metricsAggregator.RegisterSystem("BatchCodeSystem")
	metricsAggregator.RegisterSystem("BatchPulseResultSystem")
	metricsAggregator.RegisterSystem("BatchInterventionResultSystem")
	metricsAggregator.RegisterSystem("BatchCodeResultSystem")

	return &Controller{
		world:                    &tool.World,
		mapper:                   mapper,
		queue:                    boundedQueue,
		batchProcessor:           batchProcessor,
		connPool:                 connPool,
		workerPool:               workerPool,
		pulseScheduleSystem:      pulseScheduleSystem,
		pulseSystem:              pulseSystem,
		interventionSystem:       interventionSystem,
		codeSystem:               codeSystem,
		pulseResultSystem:        pulseResultSystem,
		interventionResultSystem: interventionResultSystem,
		codeResultSystem:         codeResultSystem,
		config:                   config,
		metricsAggregator:        metricsAggregator,
	}
}

// LoadMonitors loads monitors using the streaming loader
func (oc *Controller) LoadMonitors(ctx context.Context, filename string) error {
	// Initialize loggers if not already done
	if SystemLogger == nil {
		InitializeLoggers(true) // Enable debug for  loading
	}

	SystemLogger.Info("Loading monitors from %s using streaming loader", filename)

	loader := streaming.NewStreamingLoader(filename, oc.world, oc.config.StreamingConfig)
	stats, err := loader.Load(ctx)
	if err != nil {
		SystemLogger.Error("Failed to load monitors: %v", err)
		return fmt.Errorf("failed to load monitors: %w", err)
	}

	SystemLogger.Info("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.TotalEntities, stats.LoadingTime, stats.CreationRate)

	return nil
}

// Start starts the  controller
func (oc *Controller) Start(ctx context.Context) error {
	if oc.running {
		return fmt.Errorf("controller already running")
	}

	SystemLogger.Info("Starting controller")

	// Start  components - these never block
	if err := oc.workerPool.Start(ctx); err != nil {
		SystemLogger.Error("Failed to start worker pool: %v", err)
		return fmt.Errorf("failed to start worker pool: %w", err)
	}
	SystemLogger.Debug("Worker pool started successfully")

	if err := oc.batchProcessor.Start(ctx); err != nil {
		SystemLogger.Error("Failed to start batch processor: %v", err)
		return fmt.Errorf("failed to start batch processor: %w", err)
	}
	SystemLogger.Debug("Batch processor started successfully")

	// Start main loop
	go oc.mainLoop(ctx)
	go oc.statsLoop(ctx)

	oc.running = true
	SystemLogger.Info("controller started successfully")

	return nil
}

// Stop stops the  controller
func (oc *Controller) Stop() {
	if !oc.running {
		return
	}

	fmt.Println("Stopping controller...")

	oc.running = false
	oc.batchProcessor.Stop()
	oc.workerPool.Stop()
	oc.queue.Close()
	oc.connPool.Close()

	fmt.Println("controller stopped")
}

// mainLoop is the main processing loop
func (oc *Controller) mainLoop(ctx context.Context) {
	Ticker := time.NewTicker(oc.config.UpdateInterval)
	defer Ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-Ticker.C:
			if !oc.running {
				return
			}

			// Update ECS systems (single-threaded for Ark compatibility)
			// CRITICAL: Schedule FIRST to mark entities as needed!
			start := time.Now()
			if err := oc.pulseScheduleSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating pulse schedule system: %v", err)
			}
			oc.metricsAggregator.RecordSystemUpdate("BatchPulseScheduleSystem", time.Since(start), 0, 0)

			// Then dispatch the scheduled entities
			start = time.Now()
			if err := oc.pulseSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating pulse system: %v", err)
			}
			entities, batches := oc.pulseSystem.GetMetrics()
			oc.metricsAggregator.RecordSystemUpdate("BatchPulseSystem", time.Since(start), entities, batches)

			start = time.Now()
			if err := oc.interventionSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating intervention system: %v", err)
			}
			entities, batches = oc.interventionSystem.GetMetrics()
			oc.metricsAggregator.RecordSystemUpdate("BatchInterventionSystem", time.Since(start), entities, batches)

			start = time.Now()
			if err := oc.codeSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating code system: %v", err)
			}
			entities, batches = oc.codeSystem.GetMetrics()
			oc.metricsAggregator.RecordSystemUpdate("BatchCodeSystem", time.Since(start), entities, batches)

			// Update result processing systems using the original ECS world interface
			start = time.Now()
			oc.pulseResultSystem.Update(oc.world)
			oc.metricsAggregator.RecordSystemUpdate("BatchPulseResultSystem", time.Since(start), 0, 0)

			start = time.Now()
			oc.interventionResultSystem.Update(oc.world)
			oc.metricsAggregator.RecordSystemUpdate("BatchInterventionResultSystem", time.Since(start), 0, 0)

			start = time.Now()
			oc.codeResultSystem.Update(oc.world)
			oc.metricsAggregator.RecordSystemUpdate("BatchCodeResultSystem", time.Since(start), 0, 0)
		}
	}
}

// statsLoop prints performance statistics
func (oc *Controller) statsLoop(ctx context.Context) {
	Ticker := time.NewTicker(oc.config.StatsInterval)
	defer Ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-Ticker.C:
			if !oc.running {
				return
			}

			oc.printStats()
		}
	}
}

// printStats prints current performance statistics
func (oc *Controller) printStats() {
	// Get stats from  components
	queueStats := oc.queue.Stats()
	processorStats := oc.batchProcessor.Stats()
	workerStats := oc.workerPool.Stats()

	SystemLogger.Info("=== PERFORMANCE STATISTICS ===")
	SystemLogger.Info("Queue: depth=%d, enqueued=%d, dequeued=%d, dropped=%d",
		queueStats.QueueDepth, queueStats.Enqueued, queueStats.Dequeued, queueStats.Dropped)

	SystemLogger.Info("Batch Processor: processed=%d, failed=%d, avg_time=%.2fms, throughput=%.1f/sec",
		processorStats.Processed, processorStats.Failed,
		processorStats.AverageTime.Seconds()*1000, processorStats.Throughput)

	SystemLogger.Info("Worker Pool: current=%d, target=%d, tasks_processed=%d",
		workerStats.CurrentWorkers, workerStats.TargetWorkers, workerStats.TasksProcessed)

	SystemLogger.Info("===============================")
}

// PrintShutdownMetrics prints comprehensive shutdown metrics
func (oc *Controller) PrintShutdownMetrics() {
	SystemLogger.Info("=== SHUTDOWN METRICS REPORT ===")

	// Queue statistics with latency data
	queueStats := oc.queue.Stats()
	SystemLogger.Info("=== QUEUE PERFORMANCE ===")
	SystemLogger.Info("Total Jobs: enqueued=%d, dequeued=%d, dropped=%d",
		queueStats.Enqueued, queueStats.Dequeued, queueStats.Dropped)
	SystemLogger.Info("Queue Latency: max=%v, avg=%v",
		queueStats.MaxQueueTime, queueStats.AvgQueueTime)
	SystemLogger.Info("Job Execution: max_latency=%v, avg_latency=%v",
		queueStats.MaxJobLatency, queueStats.AvgJobLatency)

	// Batch processor statistics
	processorStats := oc.batchProcessor.Stats()
	SystemLogger.Info("=== BATCH PROCESSOR ===")
	SystemLogger.Info("Jobs: processed=%d, failed=%d, success_rate=%.2f%%",
		processorStats.Processed, processorStats.Failed,
		float64(processorStats.Processed-processorStats.Failed)/float64(processorStats.Processed)*100)
	SystemLogger.Info("Performance: avg_time=%.2fms, throughput=%.1f/sec",
		processorStats.AverageTime.Seconds()*1000, processorStats.Throughput)

	// Worker pool statistics
	workerStats := oc.workerPool.Stats()
	SystemLogger.Info("=== WORKER POOL ===")
	SystemLogger.Info("Workers: current=%d, target=%d, created=%d, destroyed=%d",
		workerStats.CurrentWorkers, workerStats.TargetWorkers,
		workerStats.WorkersCreated, workerStats.WorkersDestroyed)
	SystemLogger.Info("Tasks: processed=%d, queued=%d, queue_depth=%d",
		workerStats.TasksProcessed, workerStats.TasksQueued, workerStats.QueueDepth)

	// System performance metrics
	SystemLogger.Info("=== SYSTEM PERFORMANCE ===")
	aggregateMetrics := oc.metricsAggregator.GetAggregateMetrics()
	SystemLogger.Info("Runtime: %v", time.Since(aggregateMetrics.StartTime))
	SystemLogger.Info("Systems: %d active systems", aggregateMetrics.SystemCount)
	SystemLogger.Info("Updates: total=%d, rate=%.1f/sec",
		aggregateMetrics.TotalUpdates, aggregateMetrics.UpdatesPerSecond)
	SystemLogger.Info("Entities: processed=%d, rate=%.1f/sec, avg_per_update=%.1f",
		aggregateMetrics.TotalEntitiesProcessed, aggregateMetrics.EntitiesPerSecond, aggregateMetrics.AvgEntitiesPerUpdate)
	SystemLogger.Info("Batches: created=%d, avg_per_update=%.1f",
		aggregateMetrics.TotalBatchesCreated, aggregateMetrics.AvgBatchesPerUpdate)
	SystemLogger.Info("Update Duration: max=%v, min=%v, avg=%v",
		aggregateMetrics.MaxUpdateDuration, aggregateMetrics.MinUpdateDuration, aggregateMetrics.AvgUpdateDuration)

	// Per-system breakdown
	SystemLogger.Info("=== INDIVIDUAL SYSTEM METRICS ===")
	allSystemMetrics := oc.metricsAggregator.GetAllMetrics()
	for systemName, metrics := range allSystemMetrics {
		if metrics.TotalUpdates > 0 {
			avgDuration := metrics.TotalDuration / time.Duration(metrics.TotalUpdates)
			avgEntitiesPerUpdate := float64(metrics.TotalEntitiesProcessed) / float64(metrics.TotalUpdates)

			SystemLogger.Info("%s: updates=%d, entities=%d (%.1f/update), batches=%d, avg_duration=%v",
				systemName, metrics.TotalUpdates, metrics.TotalEntitiesProcessed,
				avgEntitiesPerUpdate, metrics.TotalBatchesCreated, avgDuration)
		}
	}

	// Memory and resource summary
	SystemLogger.Info("=== RESOURCE SUMMARY ===")
	runtime := time.Since(aggregateMetrics.StartTime)
	if runtime > 0 {
		totalThroughput := float64(aggregateMetrics.TotalEntitiesProcessed) / runtime.Seconds()
		SystemLogger.Info("Overall Throughput: %.1f entities/sec over %v", totalThroughput, runtime)
	}

	SystemLogger.Info("==== END SHUTDOWN METRICS ====")
}

// GetWorld returns the ECS world for external access
func (oc *Controller) GetWorld() *ecs.World {
	return oc.world
}
