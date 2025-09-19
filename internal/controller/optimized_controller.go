package controller

import (
	"context"
	"cpra/internal/controller/systems"
	"cpra/internal/queue"
	"fmt"
	"log"
	"os"
	"time"

	"cpra/internal/controller/entities"
	"cpra/internal/loader/streaming"
	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// LoggerAdapter adapts the controller loggers to the systems interface.
type LoggerAdapter struct {
	logger interface {
		Info(format string, args ...interface{})
		Debug(format string, args ...interface{})
		Warn(format string, args ...interface{})
		Error(format string, args ...interface{})
		LogSystemPerformance(name string, duration time.Duration, count int)
	}
}

func (l *LoggerAdapter) Info(format string, args ...interface{})  { l.logger.Info(format, args...) }
func (l *LoggerAdapter) Debug(format string, args ...interface{}) { l.logger.Debug(format, args...) }
func (l *LoggerAdapter) Warn(format string, args ...interface{})  { l.logger.Warn(format, args...) }
func (l *LoggerAdapter) Error(format string, args ...interface{}) { l.logger.Error(format, args...) }
func (l *LoggerAdapter) LogSystemPerformance(name string, duration time.Duration, count int) {
	l.logger.LogSystemPerformance(name, duration, count)
}
func (l *LoggerAdapter) LogComponentState(entityID uint32, component string, action string) {
	l.logger.Debug("Entity[%d] component %s: %s", entityID, component, action)
}

// OptimizedController manages the ECS world and its systems using ark-tools.
type OptimizedController struct {
	app        *app.App
	world      *ecs.World
	mapper     *entities.EntityManager
	queue      queue.Queue
	workerPool *queue.DynamicWorkerPool

	// ECS Systems
	pulseScheduleSystem      *systems.BatchPulseScheduleSystem
	pulseSystem              *systems.BatchPulseSystem
	pulseResultSystem        *systems.BatchPulseResultSystem
	interventionSystem       *systems.BatchInterventionSystem
	interventionResultSystem *systems.BatchInterventionResultSystem
	codeSystem               *systems.BatchCodeSystem
	codeResultSystem         *systems.BatchCodeResultSystem

	config  Config
	running bool
}

// Config holds all configuration for the controller.
type Config struct {
	StreamingConfig streaming.StreamingConfig
	QueueCapacity   uint64
	WorkerConfig    queue.WorkerPoolConfig
	BatchSize       int
	UpdateInterval  time.Duration
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		StreamingConfig: streaming.DefaultStreamingConfig(),
		QueueCapacity:   65536, // Must be a power of 2
		WorkerConfig:    queue.DefaultWorkerPoolConfig(),
		BatchSize:       1000,
		UpdateInterval:  100 * time.Millisecond,
	}
}

// NewOptimizedController creates a new controller with the refactored systems using ark-tools.
func NewOptimizedController(config Config) *OptimizedController {
	// Create ark-tools app with initial capacity
	arkApp := app.New(1024)
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	// Instantiate the new adaptive queue and dynamic worker pool.
	adaptiveQueue, err := queue.NewAdaptiveQueue(config.QueueCapacity)
	if err != nil {
		log.Fatalf("Failed to create adaptive queue: %v", err)
	}

	// A simple logger for the worker pool
	workerLogger := log.New(os.Stdout, "[WorkerPool] ", log.LstdFlags)

	dynamicPool, err := queue.NewDynamicWorkerPool(adaptiveQueue, config.WorkerConfig, workerLogger)
	if err != nil {
		log.Fatalf("Failed to create dynamic worker pool: %v", err)
	}

	logger := &LoggerAdapter{logger: SystemLogger}

	// Instantiate the refactored systems.
	router := dynamicPool.GetRouter()
	pulseScheduleSystem := systems.NewBatchPulseScheduleSystem(world, logger)
	pulseSystem := systems.NewBatchPulseSystem(world, adaptiveQueue, config.BatchSize, logger)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, router.PulseResultChan, logger)

	interventionSystem := systems.NewBatchInterventionSystem(world, adaptiveQueue, config.BatchSize, logger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, router.InterventionResultChan, logger)

	codeSystem := systems.NewBatchCodeSystem(world, adaptiveQueue, config.BatchSize, logger)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, router.CodeResultChan, logger)

	arkApp.AddSystem(pulseScheduleSystem)
	arkApp.AddSystem(pulseSystem)
	arkApp.AddSystem(interventionSystem)
	arkApp.AddSystem(codeSystem)
	arkApp.AddSystem(pulseResultSystem)
	arkApp.AddSystem(interventionResultSystem)
	arkApp.AddSystem(codeResultSystem)

	return &OptimizedController{
		app:                      arkApp,
		world:                    world,
		mapper:                   mapper,
		queue:                    adaptiveQueue,
		workerPool:               dynamicPool,
		pulseScheduleSystem:      pulseScheduleSystem,
		pulseSystem:              pulseSystem,
		pulseResultSystem:        pulseResultSystem,
		interventionSystem:       interventionSystem,
		interventionResultSystem: interventionResultSystem,
		codeSystem:               codeSystem,
		codeResultSystem:         codeResultSystem,
		config:                   config,
	}
}

// LoadMonitors loads monitors using the streaming loader.
func (c *OptimizedController) LoadMonitors(ctx context.Context, filename string) error {
	loader := streaming.NewStreamingLoader(filename, c.world, c.config.StreamingConfig)
	stats, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	SystemLogger.Info("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.TotalEntities, stats.LoadingTime, stats.CreationRate)
	return nil
}

// Start begins the main processing loop of the controller.
func (c *OptimizedController) Start(ctx context.Context) error {
	if c.running {
		return fmt.Errorf("controller already running")
	}
	c.workerPool.Start()
	c.running = true
	go c.app.Run()
	SystemLogger.Info("Optimized controller started successfully")
	return nil
}

// Stop gracefully shuts down the controller.
func (c *OptimizedController) Stop() {
	if !c.running {
		return
	}
	SystemLogger.Info("Stopping controller...")
	c.app.Finalize()
	c.running = false
	c.workerPool.Stop()
	c.queue.Close()
	SystemLogger.Info("Controller stopped")
}

// GetWorld returns the ECS world for external access (e.g., testing, debugging).
func (c *OptimizedController) GetWorld() *ecs.World {
	return c.world
}
