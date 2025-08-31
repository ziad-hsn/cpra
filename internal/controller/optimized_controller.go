package controller

import (
	"context"
	"fmt"
	"time"

	optimizedSystems "cpra/internal/controller/systems/optimized"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/loader/streaming"
	"cpra/internal/queue/optimized"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// LoggerAdapter adapts the controller loggers to the optimized systems interface
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

// OptimizedController manages the optimized monitoring system using original queue approach
type OptimizedController struct {
	// Core components
	world  *ecs.World
	mapper *entities.EntityManager

	// Queue system - use optimized components (NEVER blocks)
	queue                    *optimized.BoundedQueue
	batchProcessor           *optimized.BatchProcessor
	connPool                 *optimized.ConnectionPool
	workerPool               *optimized.DynamicWorkerPool

	// ECS systems - same as original but with batching
	pulseScheduleSystem    *optimizedSystems.BatchPulseScheduleSystem
	pulseSystem            *optimizedSystems.BatchPulseSystem
	interventionSystem     *optimizedSystems.BatchInterventionSystem
	codeSystem             *optimizedSystems.BatchCodeSystem

	// Result processing systems - read from original channels
	pulseResultSystem        *optimizedSystems.BatchPulseResultSystem
	interventionResultSystem *optimizedSystems.BatchInterventionResultSystem
	codeResultSystem         *optimizedSystems.BatchCodeResultSystem

	// Configuration
	config OptimizedConfig

	// State
	running bool
}

// OptimizedConfig holds all configuration for the optimized controller
type OptimizedConfig struct {
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

// DefaultOptimizedConfig returns optimized default configuration for 1M monitors
func DefaultOptimizedConfig() OptimizedConfig {
	return OptimizedConfig{
		StreamingConfig: streaming.DefaultStreamingConfig(),

		// Use original queue manager that never blocks
		MonitorCount: 1000000,

		// Batch processing optimization - process more entities per system update
		BatchSize: 5000, // Process 5K entities per batch for speed

		UpdateInterval: 1 * time.Second,
		StatsInterval:  10 * time.Second,
	}
}

// NewOptimizedController creates a new optimized controller using the original queue approach
func NewOptimizedController(config OptimizedConfig) *OptimizedController {
	// Create ECS world using app tool like in main.go
	tool := app.New(1024).Seed(123)
	tool.TPS = 10000 // High TPS for 1M monitors

	// Initialize entity manager exactly like original systems
	mapper := entities.InitializeMappers(&tool.World)

	// Create optimized queue components - THESE NEVER BLOCK!
	queueConfig := optimized.QueueConfig{
		MaxSize:      50000,
		MaxBatch:     500,
		BatchTimeout: 50 * time.Millisecond,
	}
	boundedQueue := optimized.NewBoundedQueue(queueConfig)
	
	connPool := optimized.NewConnectionPool(optimized.DefaultPoolConfig())
	workerPool := optimized.NewDynamicWorkerPool(optimized.DefaultWorkerPoolConfig(), WorkerPoolLogger)
	
	// Create result channels
	pulseResults := make(chan jobs.Result, 10000)
	interventionResults := make(chan jobs.Result, 5000)
	codeResults := make(chan jobs.Result, 5000)
	
	batchProcessor := optimized.NewBatchProcessor(boundedQueue, connPool, optimized.ProcessorConfig{
		BatchSize:     500,
		MaxConcurrent: 200,
		Timeout:       30 * time.Second,
		RetryAttempts: 3,
		RetryDelay:    500 * time.Millisecond,
	}, WorkerPoolLogger, pulseResults, interventionResults, codeResults)

	// Create ECS systems using optimized components
	dispatchLogger := &LoggerAdapter{logger: DispatchLogger}
	schedulerLogger := &LoggerAdapter{logger: SchedulerLogger}
	pulseScheduleSystem := optimizedSystems.NewBatchPulseScheduleSystem(&tool.World, mapper, config.BatchSize, schedulerLogger)
	pulseSystem := optimizedSystems.NewBatchPulseSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)
	interventionSystem := optimizedSystems.NewBatchInterventionSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)
	codeSystem := optimizedSystems.NewBatchCodeSystem(&tool.World, mapper, boundedQueue, config.BatchSize, dispatchLogger)

	// Create result processing systems using result channels
	resultLogger := &LoggerAdapter{logger: ResultLogger}
	pulseResultSystem := optimizedSystems.NewBatchPulseResultSystem(pulseResults, mapper, resultLogger)
	interventionResultSystem := optimizedSystems.NewBatchInterventionResultSystem(interventionResults, mapper, resultLogger)
	codeResultSystem := optimizedSystems.NewBatchCodeResultSystem(codeResults, mapper, resultLogger)

	return &OptimizedController{
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
	}
}

// LoadMonitors loads monitors using the streaming loader
func (oc *OptimizedController) LoadMonitors(ctx context.Context, filename string) error {
	// Initialize loggers if not already done
	if SystemLogger == nil {
		InitializeLoggers(true) // Enable debug for optimized loading
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

// Start starts the optimized controller
func (oc *OptimizedController) Start(ctx context.Context) error {
	if oc.running {
		return fmt.Errorf("controller already running")
	}

	SystemLogger.Info("Starting optimized controller")

	// Start optimized components - these never block
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
	SystemLogger.Info("Optimized controller started successfully")

	return nil
}

// Stop stops the optimized controller
func (oc *OptimizedController) Stop() {
	if !oc.running {
		return
	}

	fmt.Println("Stopping optimized controller...")

	oc.running = false
	oc.batchProcessor.Stop()
	oc.workerPool.Stop()
	oc.queue.Close()
	oc.connPool.Close()

	fmt.Println("Optimized controller stopped")
}

// mainLoop is the main processing loop
func (oc *OptimizedController) mainLoop(ctx context.Context) {
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
			if err := oc.pulseScheduleSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating pulse schedule system: %v", err)
			}
			
			// Then dispatch the scheduled entities
			if err := oc.pulseSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating pulse system: %v", err)
			}

			if err := oc.interventionSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating intervention system: %v", err)
			}

			if err := oc.codeSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating code system: %v", err)
			}

			// Update result processing systems using the original ECS world interface
			oc.pulseResultSystem.Update(oc.world)
			oc.interventionResultSystem.Update(oc.world)
			oc.codeResultSystem.Update(oc.world)
		}
	}
}

// statsLoop prints performance statistics
func (oc *OptimizedController) statsLoop(ctx context.Context) {
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
func (oc *OptimizedController) printStats() {
	// Get stats from optimized components
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

// GetWorld returns the ECS world for external access
func (oc *OptimizedController) GetWorld() *ecs.World {
	return oc.world
}
