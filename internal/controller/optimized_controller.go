package controller

import (
	"context"
	"fmt"
	"time"
	
	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark-tools/app"
	"cpra/internal/loader/streaming"
	"cpra/internal/queue/optimized"
	optimizedSystems "cpra/internal/controller/systems/optimized"
)

// OptimizedController manages the entire optimized monitoring system
type OptimizedController struct {
	// Core components
	world          *ecs.World
	
	// Queue components
	queue          *optimized.BoundedQueue
	batchCollector *optimized.BatchCollector
	connPool       *optimized.ConnectionPool
	workerPool     *optimized.DynamicWorkerPool
	batchProcessor *optimized.BatchProcessor
	
	// ECS systems
	pulseSystem        *optimizedSystems.BatchPulseSystem
	interventionSystem *optimizedSystems.BatchInterventionSystem
	codeSystem         *optimizedSystems.BatchCodeSystem
	memorySystem       *optimizedSystems.MemoryEfficientSystem
	
	// Configuration
	config         OptimizedConfig
	
	// State
	running        bool
}

// OptimizedConfig holds all configuration for the optimized controller
type OptimizedConfig struct {
	// Streaming loader config
	StreamingConfig streaming.StreamingConfig
	
	// Queue config
	QueueConfig     optimized.QueueConfig
	PoolConfig      optimized.PoolConfig
	WorkerConfig    optimized.WorkerPoolConfig
	ProcessorConfig optimized.ProcessorConfig
	
	// System config
	SystemConfig    optimizedSystems.SystemConfig
	MemoryConfig    optimizedSystems.MemoryConfig
	
	// Performance config
	UpdateInterval  time.Duration
	StatsInterval   time.Duration
}

// DefaultOptimizedConfig returns optimized default configuration for 1M monitors
func DefaultOptimizedConfig() OptimizedConfig {
	return OptimizedConfig{
		StreamingConfig: streaming.DefaultStreamingConfig(),
		
		QueueConfig: optimized.QueueConfig{
			MaxSize:      50000,  // 50k batches max for 1M monitors
			MaxBatch:     500,    // 500 jobs per batch for higher throughput
			BatchTimeout: 50 * time.Millisecond, // Faster batching
		},
		
		PoolConfig: optimized.DefaultPoolConfig(),
		WorkerConfig: optimized.DefaultWorkerPoolConfig(),
		
		ProcessorConfig: optimized.ProcessorConfig{
			BatchSize:     500,  // Match batch size
			MaxConcurrent: 200,  // 200 concurrent batches for 1M monitors
			Timeout:       30 * time.Second,
			RetryAttempts: 3,
			RetryDelay:    500 * time.Millisecond, // Faster retries
		},
		
		SystemConfig: optimizedSystems.SystemConfig{
			BatchSize:       5000, // Process 5K entities per batch for speed
			BufferSize:      100000, // Buffer for 100K entities
			ProcessInterval: 500 * time.Millisecond, // Faster processing
		},
		
		MemoryConfig: optimizedSystems.MemoryConfig{
			GCInterval:      10 * time.Second,
			MemoryThreshold: 1024 * 1024 * 1024, // 1GB
			EnableProfiling: true,
		},
		
		UpdateInterval: 1 * time.Second,
		StatsInterval:  10 * time.Second,
	}
}

// NewOptimizedController creates a new optimized controller
func NewOptimizedController(config OptimizedConfig) *OptimizedController {
	// Create ECS world using app tool like in main.go
	tool := app.New(1024).Seed(123)
	tool.TPS = 10000 // High TPS for 1M monitors
	
	// Create queue components
	queue := optimized.NewBoundedQueue(config.QueueConfig)
	batchCollector := optimized.NewBatchCollector(queue, config.QueueConfig.MaxBatch, config.QueueConfig.BatchTimeout)
	connPool := optimized.NewConnectionPool(config.PoolConfig)
	workerPool := optimized.NewDynamicWorkerPool(config.WorkerConfig, WorkerPoolLogger)
	batchProcessor := optimized.NewBatchProcessor(queue, connPool, config.ProcessorConfig, WorkerPoolLogger)
	
	// Create ECS systems with proper logging
	pulseSystem := optimizedSystems.NewBatchPulseSystem(&tool.World, batchCollector, config.SystemConfig)
	interventionSystem := optimizedSystems.NewBatchInterventionSystem(&tool.World, batchCollector, config.SystemConfig, DispatchLogger)
	codeSystem := optimizedSystems.NewBatchCodeSystem(&tool.World, batchCollector, config.SystemConfig, DispatchLogger)
	memorySystem := optimizedSystems.NewMemoryEfficientSystem(&tool.World, config.MemoryConfig)
	
	return &OptimizedController{
		world:              &tool.World,
		queue:              queue,
		batchCollector:     batchCollector,
		connPool:           connPool,
		workerPool:         workerPool,
		batchProcessor:     batchProcessor,
		pulseSystem:        pulseSystem,
		interventionSystem: interventionSystem,
		codeSystem:         codeSystem,
		memorySystem:       memorySystem,
		config:             config,
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
	
	// Start queue components
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
	oc.batchCollector.Close()
	oc.queue.Close()
	oc.connPool.Close()
	
	fmt.Println("Optimized controller stopped")
}

// mainLoop is the main processing loop
func (oc *OptimizedController) mainLoop(ctx context.Context) {
	ticker := time.NewTicker(oc.config.UpdateInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !oc.running {
				return
			}
			
			// Update ECS systems (single-threaded for Ark compatibility)
			if err := oc.pulseSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating pulse system: %v", err)
			}
			
			if err := oc.interventionSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating intervention system: %v", err)
			}
			
			if err := oc.codeSystem.Update(ctx); err != nil {
				SystemLogger.Error("Error updating code system: %v", err)
			}
			
			oc.memorySystem.Update()
		}
	}
}

// statsLoop prints performance statistics
func (oc *OptimizedController) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(oc.config.StatsInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !oc.running {
				return
			}
			
			oc.printStats()
		}
	}
}

// printStats prints current performance statistics
func (oc *OptimizedController) printStats() {
	// Queue stats
	queueStats := oc.queue.Stats()
	
	// Processor stats
	processorStats := oc.batchProcessor.Stats()
	
	// Worker pool stats
	workerStats := oc.workerPool.Stats()
	
	// System stats
	pulseStats := oc.pulseSystem.GetStats()
	interventionStats := oc.interventionSystem.GetStats()
	codeStats := oc.codeSystem.GetStats()
	
	// Memory stats
	memoryStats := oc.memorySystem.GetMemoryStats()
	
	SystemLogger.Info("=== PERFORMANCE STATISTICS ===")
	SystemLogger.Info("Queue: %d enqueued, %d processed, %d dropped, depth: %d",
		queueStats.Enqueued, queueStats.Dequeued, queueStats.Dropped, queueStats.QueueDepth)
	
	SystemLogger.Info("Processor: %d processed, %d failed, %.0f jobs/sec",
		processorStats.Processed, processorStats.Failed, processorStats.Throughput)
	
	SystemLogger.Info("Workers: %d current, %d target, %d tasks processed",
		workerStats.CurrentWorkers, workerStats.TargetWorkers, workerStats.TasksProcessed)
	
	SystemLogger.Info("Pulse ECS: %d entities processed, %d batches created",
		pulseStats.EntitiesProcessed, pulseStats.BatchesCreated)
	
	SystemLogger.Info("Intervention ECS: %d entities processed, %d batches created, %d jobs dispatched",
		interventionStats.EntitiesProcessed, interventionStats.BatchesCreated, interventionStats.JobsDispatched)
	
	SystemLogger.Info("Code ECS: %d entities processed, %d batches created, %d jobs dispatched",
		codeStats.EntitiesProcessed, codeStats.BatchesCreated, codeStats.JobsDispatched)
	
	SystemLogger.Info("Memory: %d MB allocated, %d GCs",
		memoryStats.Alloc/1024/1024, memoryStats.GCCount)
	
	SystemLogger.Info("===============================")
}

// GetWorld returns the ECS world for external access
func (oc *OptimizedController) GetWorld() *ecs.World {
	return oc.world
}