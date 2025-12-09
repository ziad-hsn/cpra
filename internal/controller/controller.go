// Package controller provides the core ECS-based controller for managing monitors
// in the CPRA (Cloud Platform Reliability Automation) system.
//
// The controller orchestrates the Entity Component System (ECS) architecture,
// managing monitor lifecycle, job queuing, worker pools, and system coordination.
// It is designed to handle large-scale deployments (1M+ monitors) efficiently
// through batch processing, adaptive queuing, and optimized memory management.
//
// # Architecture
//
// The controller uses the ark ECS library to manage monitor entities and their
// components. Key architectural decisions:
//
//   - Batch Processing: Systems process entities in batches to maximize throughput
//   - Queue Abstraction: Multiple queue implementations (Hybrid, Adaptive, Workiva)
//     can be used based on workload characteristics
//   - Worker Pools: Dynamic worker pools with automatic scaling for pulse, intervention,
//     and code alert processing
//   - Streaming Loader: Efficient YAML/JSON parsing for large monitor configurations
//
// # Components
//
// The controller manages three primary job types:
//
//   - Pulse: Health checks (HTTP, TCP, ICMP) performed at regular intervals
//   - Intervention: Automated recovery actions (e.g., Docker container restarts)
//   - Code: Alert notifications (Red, Yellow, Green, Cyan, Gray) sent via various channels
//
// # Systems
//
// The controller coordinates multiple ECS systems:
//
//   - BatchPulseScheduleSystem: Schedules pulse checks based on monitor intervals
//   - BatchPulseSystem: Enqueues pulse jobs for execution
//   - BatchPulseResultSystem: Processes pulse results and updates monitor state
//   - BatchInterventionSystem: Enqueues intervention jobs when thresholds are exceeded
//   - BatchInterventionResultSystem: Processes intervention results
//   - BatchCodeSystem: Enqueues code alert jobs
//   - BatchCodeResultSystem: Processes code alert results
//
// # Queue Management
//
// The controller supports dynamic queue switching based on entity count thresholds.
// When the entity count exceeds a configured threshold, the system can automatically
// switch from HybridQueue to AdaptiveQueue for better performance at scale.
//
// # Worker Sizing
//
// The controller includes pre-computation of optimal worker pool sizes based on:
//
//   - Arrival rate (λ): Computed from monitor pulse intervals
//   - Service time (τ): Expected job execution time
//   - SLO target (W): Maximum acceptable end-to-end latency
//
// This uses M/M/c queueing theory to determine minimum workers needed and applies
// a configurable headroom percentage for safety margins.
//
// # Example
//
//	config := controller.DefaultConfig()
//	config.Debug = true
//	config.BatchSize = 1000
//	oc := controller.NewController(config)
//
//	ctx := context.Background()
//	if err := oc.LoadMonitors(ctx, "monitors.yaml"); err != nil {
//		log.Fatal(err)
//	}
//
//	if err := oc.Start(); err != nil {
//		log.Fatal(err)
//	}
//	defer oc.Stop()
package controller

import (
	"context"
	"cpra/internal/controller/systems"
	"cpra/internal/loader"
	"cpra/internal/queue"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// LoggerAdapter adapts the controller loggers to the systems interface.
type LoggerAdapter struct {
	logger interface {
		Info(format string, args ...any)
		Debug(format string, args ...any)
		Warn(format string, args ...any)
		Error(format string, args ...any)
		LogSystemPerformance(name string, duration time.Duration, count int)
	}
}

func (l *LoggerAdapter) Info(format string, args ...any)  { l.logger.Info(format, args...) }
func (l *LoggerAdapter) Debug(format string, args ...any) { l.logger.Debug(format, args...) }
func (l *LoggerAdapter) Warn(format string, args ...any)  { l.logger.Warn(format, args...) }
func (l *LoggerAdapter) Error(format string, args ...any) { l.logger.Error(format, args...) }
func (l *LoggerAdapter) LogSystemPerformance(name string, duration time.Duration, count int) {
	l.logger.LogSystemPerformance(name, duration, count)
}
func (l *LoggerAdapter) LogComponentState(entityID uint32, component string, action string) {
	l.logger.Debug("Entity[%d] component %s: %s", entityID, component, action)
}

// Controller manages the ECS world and its systems using ark-tools.
//
// Controller coordinates all aspects of monitor management including:
// entity lifecycle, job queuing, worker pool management, and system execution.
// It provides a high-level API for loading monitors, starting/stopping the system,
// and accessing the underlying ECS world for testing and debugging.
//
// The controller is thread-safe for concurrent access to read-only operations
// like GetWorld(). Start() and Stop() should be called from a single goroutine
// or with proper synchronization.
type Controller struct {
	stateLogger          *systems.StateLogger
	pulseQueue           queue.Queue
	codeQueue            queue.Queue
	interventionQueue    queue.Queue
	pulsePool            *queue.DynamicWorkerPool
	mapper               *entities.EntityManager
	world                *ecs.World
	app                  *app.App
	interventionPool     *queue.DynamicWorkerPool
	codePool             *queue.DynamicWorkerPool
	config               Config
	entityCountThreshold int64
	queueSwitchMutex     sync.RWMutex
	running              bool
	useAdaptiveQueue     bool
	logger               *Logger
}

// Config holds all configuration for the controller.
//
// Configuration can be set programmatically or via environment variables
// for sizing parameters (CPRA_SIZING_TAU_MS, CPRA_SIZING_SLO_MS, CPRA_SIZING_HEADROOM_PCT).
//
// Default values are optimized for large-scale deployments but can be adjusted
// based on workload characteristics and resource constraints.
type Config struct {
	Debug          bool
	PipelineConfig loader.PipelineConfig
	QueueCapacity  uint64
	WorkerConfig   queue.WorkerPoolConfig
	BatchSize      int
	UpdateInterval time.Duration
	// Optional pre-sizing parameters; can be overridden by env vars
	// CPRA_SIZING_TAU_MS and CPRA_SIZING_SLO_MS (milliseconds)
	SizingServiceTime time.Duration // τ
	SizingSLO         time.Duration // W target (end-to-end)
	// Optional safe headroom as a fraction (e.g., 0.15 = 15%); env override: CPRA_SIZING_HEADROOM_PCT
	SizingHeadroomPct float64
	Logger            *Logger
}

// DefaultConfig returns a default configuration optimized for large-scale deployments.
//
// The default configuration uses:
//   - Queue capacity of 65536 (must be power of 2)
//   - Batch size of 1000 entities per system update
//   - Default worker pool configuration
//   - Streaming loader defaults optimized for large files
//
// These defaults can be overridden based on specific deployment requirements.
func DefaultConfig() Config {
	return Config{
		PipelineConfig: loader.DefaultPipelineConfig(),
		QueueCapacity:  8192, // Reduced from 65536 to save ~25MB memory per queue instance
		WorkerConfig:   queue.DefaultWorkerPoolConfig(),
		BatchSize:      1000,
		// UpdateInterval removed - ark-tools TPS=100 controls all timing
		SizingServiceTime: 0,
		SizingSLO:         0,
		SizingHeadroomPct: 0,
	}
}

// NewController creates a new controller with the refactored systems using ark-tools.
//
// NewController initializes:
//   - ECS world with initial capacity
//   - Three queue instances (pulse, intervention, code) with HybridQueue by default
//   - Three dynamic worker pools for job execution
//   - All batch processing systems
//   - Entity mapper for monitor management
//
// The controller is created in a stopped state. Call Start() to begin processing.
//
// Returns an error if queue or worker pool creation fails.
func NewController(config Config) *Controller {
	// Create ark-tools app with initial capacity
	arkApp := app.New(1024)
	arkApp.TPS = 100 // High-frequency updates for 1s monitor intervals
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	// Default to Hybrid queues per queue class
	pulseCfg := queue.DefaultQueueConfig()
	pulseCfg.Name = "pulse"
	pulseCfg.HybridConfig.Name = "pulse"
	pulseCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
	pulseQueue, err := queue.NewQueue(pulseCfg)
	if err != nil {
		log.Fatalf("Failed to create pulse HybridQueue: %v", err)
	}
	interventionCfg := queue.DefaultQueueConfig()
	interventionCfg.Name = "intervention"
	interventionCfg.HybridConfig.Name = "intervention"
	interventionCfg.HybridConfig.DropPolicy = queue.DropPolicyDropOldest
	interventionQueue, err := queue.NewQueue(interventionCfg)
	if err != nil {
		log.Fatalf("Failed to create intervention HybridQueue: %v", err)
	}
	codeCfg := queue.DefaultQueueConfig()
	codeCfg.Name = "code"
	codeCfg.HybridConfig.Name = "code"
	codeCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
	codeQueue, err := queue.NewQueue(codeCfg)
	if err != nil {
		log.Fatalf("Failed to create code HybridQueue: %v", err)
	}

	pulseLogger := log.New(os.Stdout, "[PulsePool] ", log.LstdFlags)
	pulsePool, err := queue.NewDynamicWorkerPool(pulseQueue, config.WorkerConfig, pulseLogger)
	if err != nil {
		log.Fatalf("Failed to create pulse worker pool: %v", err)
	}
	interventionLogger := log.New(os.Stdout, "[InterventionPool] ", log.LstdFlags)
	interventionPool, err := queue.NewDynamicWorkerPool(interventionQueue, config.WorkerConfig, interventionLogger)
	if err != nil {
		log.Fatalf("Failed to create intervention worker pool: %v", err)
	}
	codeLogger := log.New(os.Stdout, "[CodePool] ", log.LstdFlags)
	codePool, err := queue.NewDynamicWorkerPool(codeQueue, config.WorkerConfig, codeLogger)
	if err != nil {
		log.Fatalf("Failed to create code worker pool: %v", err)
	}

	stateLogger := systems.NewStateLogger(config.Debug)

	// Use provided logger or fallback to SystemLogger
	ctrlLogger := config.Logger
	if ctrlLogger == nil {
		if SystemLogger == nil {
			InitializeLoggers(config.Debug)
		}
		ctrlLogger = SystemLogger
	}
	logger := &LoggerAdapter{logger: ctrlLogger}

	// Instantiate the refactored systems with dedicated queues and worker pools.
	pulseRouter := pulsePool.GetRouter()
	interventionRouter := interventionPool.GetRouter()
	codeRouter := codePool.GetRouter()

	pulseScheduleSystem := systems.NewBatchPulseScheduleSystem(world, logger, stateLogger)
	pulseSystem := systems.NewBatchPulseSystem(world, pulseQueue, config.BatchSize, logger, stateLogger)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, pulseRouter.PulseResultChan, logger, stateLogger)

	interventionSystem := systems.NewBatchInterventionSystem(world, interventionQueue, config.BatchSize, logger, stateLogger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, interventionRouter.InterventionResultChan, logger, stateLogger)

	codeSystem := systems.NewBatchCodeSystem(world, codeQueue, config.BatchSize, logger, stateLogger)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, codeRouter.CodeResultChan, logger, stateLogger)

	arkApp.AddSystem(pulseScheduleSystem)
	arkApp.AddSystem(pulseSystem)
	arkApp.AddSystem(interventionSystem)
	arkApp.AddSystem(codeSystem)
	arkApp.AddSystem(pulseResultSystem)
	arkApp.AddSystem(interventionResultSystem)
	arkApp.AddSystem(codeResultSystem)

	return &Controller{
		app:               arkApp,
		world:             world,
		mapper:            mapper,
		pulseQueue:        pulseQueue,
		interventionQueue: interventionQueue,
		codeQueue:         codeQueue,
		pulsePool:         pulsePool,
		interventionPool:  interventionPool,
		codePool:          codePool,
		config:            config,
		stateLogger:       stateLogger,
		logger:            ctrlLogger,
	}
}

// LoadMonitors loads monitors from a YAML file using the loader.
//
// LoadMonitors parses the file concurrently, creates ECS entities for each monitor,
// and initializes all required components. It supports YAML formats with optional
// gzip compression (.gz extension).
//
// After loading completes, the controller:
//   - Checks entity count and may switch to AdaptiveQueue if threshold exceeded
//   - Pre-computes optimal worker pool sizing for pulse jobs
//
// The context can be used to cancel the loading operation.
//
// Returns an error if file parsing or entity creation fails.
func (c *Controller) LoadMonitors(ctx context.Context, filename string) error {
	loader := loader.NewPipeline(c.world, c.mapper, c.config.PipelineConfig)
	stats, err := loader.Load(ctx, filename)
	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	c.logger.Info("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.EntitiesCreated, stats.LoadingTime, stats.CreationRate)

	// Shrink the world incrementally to reclaim over-allocated memory.
	// We use a small time budget per pass to allow context cancellation.
	shrinkPasses := 0
	for c.world.Shrink(10 * time.Millisecond) {
		shrinkPasses++
		if ctx.Err() != nil {
			return fmt.Errorf("loading cancelled during memory shrink: %w", ctx.Err())
		}
	}
	c.logger.Info("Shrunk world memory in %d passes", shrinkPasses+1)

	// Explicitly trigger GC and release memory to OS to clear fragmentation from loading/shrinking
	runtime.GC()
	debug.FreeOSMemory()

	// Check if we need to switch to AdaptiveQueue due to high entity count
	c.CheckEntityCountAndSwitchQueue()

	// Pre-calculate worker sizing from initial configuration/world (Pulse only)
	c.precomputeSizingFromConfig()
	return nil
}

// precomputeSizingFromConfig computes a recommended worker count from initial world contents
// and configured (or env) service time and latency SLO. It currently targets the Pulse pool only.
func (c *Controller) precomputeSizingFromConfig() {
	// Determine τ (service time) and W_slo from env or config; fallback to sane defaults
	tau := c.config.SizingServiceTime
	if v := os.Getenv("CPRA_SIZING_TAU_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			tau = time.Duration(ms) * time.Millisecond
		}
	}
	if tau <= 0 {
		tau = 20 * time.Millisecond
	}
	wSLO := c.config.SizingSLO
	if v := os.Getenv("CPRA_SIZING_SLO_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			wSLO = time.Duration(ms) * time.Millisecond
		}
	}
	if wSLO <= 0 {
		wSLO = 200 * time.Millisecond
	}

	// Compute λ for Pulse from world: sum over active monitors of 1/Interval
	lambda := computePulseLambda(c.world)
	if lambda <= 0 {
		c.logger.Warn("[Pre-Sizing] No active pulse workload detected; skipping sizing")
		return
	}

	cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 0, 0, 0)
	if err != nil {
		c.logger.Warn("[Pre-Sizing] Could not compute Pulse workers: %v", err)
		return
	}
	// Determine safe headroom: env CPRA_SIZING_HEADROOM_PCT (e.g., 0.15 or 15), or config, default 0.15
	headroom := c.config.SizingHeadroomPct
	if v := os.Getenv("CPRA_SIZING_HEADROOM_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			// Accept both 0.xx and percentage like 15 or 15.0
			if f > 1.0 {
				headroom = f / 100.0
			} else {
				headroom = f
			}
		}
	}
	if headroom <= 0 {
		headroom = 0.15
	} // default 15%%
	// Compute a safe recommended c with headroom
	cSafe := int(math.Ceil(float64(cMin) * (1.0 + headroom)))
	if cSafe <= cMin {
		cSafe = cMin + 1
	}
	// Predict W for cSafe (informational)
	mu := 1.0 / tau.Seconds()
	_, wSafe, errSafe := queue.MmcWait(lambda, mu, cSafe, 0, 0)
	if errSafe != nil {
		wSafe = w
	} // fallback
	c.logger.Info("[Pre-Sizing] Pulse: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
		lambda, tau.Seconds(), wSLO.Seconds(), cMin, w, cSafe, headroom*100.0, wSafe)
}

// computePulseLambda estimates arrival rate (jobs/sec) from Pulse intervals of enabled monitors.
func computePulseLambda(world *ecs.World) float64 {
	f := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
		Without(ecs.C[components.Disabled]())
	q := f.Query()
	sum := 0.0
	for q.Next() {
		_, cfg := q.Get()
		if cfg == nil || cfg.Interval <= 0 {
			continue
		}
		sum += 1.0 / cfg.Interval.Seconds()
	}
	return sum
}

// Start begins the main processing loop of the controller.
//
// Start initializes all worker pools and begins the ark-tools app execution loop.
// The controller runs at 100 TPS (ticks per second) for high-frequency updates
// required for 1-second monitor intervals.
//
// This method is idempotent - calling Start() multiple times returns an error
// if the controller is already running.
//
// Returns an error if the controller is already running or if startup fails.
func (c *Controller) Start() error {
	if c.running {
		return fmt.Errorf("controller already running")
	}
	c.pulsePool.Start()
	c.interventionPool.Start()
	c.codePool.Start()
	c.running = true
	go c.app.Run()
	c.logger.Info("Controller started successfully")
	return nil
}

// Stop gracefully shuts down the controller.
//
// Stop performs a graceful shutdown sequence:
//   - Finalizes the ark-tools app (stops all systems)
//   - Drains and stops all worker pools
//   - Closes all queues
//   - Logs shutdown metrics
//
// This method is idempotent - calling Stop() multiple times is safe.
// After Stop() completes, the controller cannot be restarted; create a new
// controller instance if needed.
func (c *Controller) Stop() {
	if !c.running {
		return
	}
	c.logger.Info("Stopping controller...")
	c.app.Finalize()
	c.running = false
	c.pulsePool.DrainAndStop()
	c.interventionPool.DrainAndStop()
	c.codePool.DrainAndStop()
	c.PrintShutdownMetrics()
	c.pulseQueue.Close()
	c.interventionQueue.Close()
	c.codeQueue.Close()
	c.logger.Info("Controller stopped")
}

// PrintShutdownMetrics logs queue, worker pool, and world statistics at shutdown.
func (c *Controller) PrintShutdownMetrics() {
	// Helpers for friendly placeholders on unset values
	formatTS := func(t time.Time) string {
		if t.IsZero() || t.Unix() == 0 {
			return "never"
		}
		return t.Format(time.RFC3339)
	}
	formatDur := func(d time.Duration) string {
		if d <= 0 {
			return "N/A"
		}
		return d.String()
	}

	logQueue := func(label string, stats queue.Stats) {
		c.logger.Info("%s Queue: depth=%d/%d enqueued=%d dequeued=%d dropped=%d", label, stats.QueueDepth, stats.Capacity, stats.Enqueued, stats.Dequeued, stats.Dropped)
		c.logger.Info("%s Queue timings: avg_wait=%s max_wait=%s window=%s", label, formatDur(stats.AvgQueueTime), formatDur(stats.MaxQueueTime), formatDur(stats.SampleWindow))
		c.logger.Info("%s Queue rates: arrival=%.2f/s service=%.2f/s last_enqueue=%s last_dequeue=%s", label, stats.EnqueueRate, stats.DequeueRate, formatTS(stats.LastEnqueue), formatTS(stats.LastDequeue))
	}
	logWorkers := func(label string, stats queue.WorkerPoolStats) {
		c.logger.Info("%s Workers: running=%d capacity=%d target=%d min=%d max=%d waiting=%d", label, stats.RunningWorkers, stats.CurrentCapacity, stats.TargetWorkers, stats.MinWorkers, stats.MaxWorkers, stats.WaitingTasks)
		c.logger.Info("%s Tasks: submitted=%d completed=%d pending_results=%d scaling_events=%d last_scale=%s", label, stats.TasksSubmitted, stats.TasksCompleted, stats.PendingResults, stats.ScalingEvents, formatTS(stats.LastScaleTime))
	}

	c.logger.Info("=== SHUTDOWN METRICS ===")

	pulseQ := c.pulseQueue.Stats()
	intQ := c.interventionQueue.Stats()
	codeQ := c.codeQueue.Stats()
	logQueue("Pulse", pulseQ)
	logQueue("Intervention", intQ)
	logQueue("Code", codeQ)

	pulseWP := c.pulsePool.Stats()
	intWP := c.interventionPool.Stats()
	codeWP := c.codePool.Stats()
	logWorkers("Pulse", pulseWP)
	logWorkers("Intervention", intWP)
	logWorkers("Code", codeWP)

	// Sizing recommendations temporarily disabled
	// Enable later by wrapping with a feature flag or env check
	// sizingRecommendationsEnabled := false
	// if sizingRecommendationsEnabled {
	//     ca, cs := 0.0, 0.0 // default to exponential; set >0 from telemetry if available
	//     wqTarget := c.config.WorkerConfig.TargetQueueLatency
	//     if wqTarget <= 0 {
	//         wqTarget = 100 * time.Millisecond
	//     }
	//     if rec, w, err := queue.RecommendCFromObserved(pulseQ, pulseWP, wqTarget, ca, cs); err == nil {
	//         SystemLogger.Info("[Sizing] Pulse recommended workers: %d (predicted W≈%.3fs)", rec, w)
	//     } else {
	//         SystemLogger.Warn("[Sizing] Pulse sizing unavailable: %v", err)
	//     }
	//     if rec, w, err := queue.RecommendCFromObserved(intQ, intWP, wqTarget, ca, cs); err == nil {
	//         SystemLogger.Info("[Sizing] Intervention recommended workers: %d (predicted W≈%.3fs)", rec, w)
	//     } else {
	//         SystemLogger.Warn("[Sizing] Intervention sizing unavailable: %v", err)
	//     }
	//     if rec, w, err := queue.RecommendCFromObserved(codeQ, codeWP, wqTarget, ca, cs); err == nil {
	//         SystemLogger.Info("[Sizing] Code recommended workers: %d (predicted W≈%.3fs)", rec, w)
	//     } else {
	//         SystemLogger.Warn("[Sizing] Code sizing unavailable: %v", err)
	//     }
	// }

	worldStats := c.world.Stats()
	c.logger.Info("World: entities_used=%d recycled=%d total=%d archetypes=%d components=%d filters=%d locked=%t",
		worldStats.Entities.Used, worldStats.Entities.Recycled, worldStats.Entities.Total,
		len(worldStats.Archetypes), len(worldStats.ComponentTypes), worldStats.CachedFilters, worldStats.Locked)
	c.logger.Info("World memory: reserved=%dB used=%dB", worldStats.Memory, worldStats.MemoryUsed)
	c.logger.Info("=========================")
}

// GetWorld returns the ECS world for external access (e.g., testing, debugging).
//
// GetWorld provides direct access to the underlying ECS world. This is useful for:
//   - Testing: Inspecting entities and components in tests
//   - Debugging: Querying entity state during development
//   - Metrics: Accessing world statistics
//
// The returned world should not be modified directly while the controller is running,
// as this may cause race conditions with the ECS systems.
func (c *Controller) GetWorld() *ecs.World {
	return c.world
}

// switchToAdaptiveQueues drains current queues and switches to AdaptiveQueue implementation.
func (c *Controller) switchToAdaptiveQueues() {
	c.queueSwitchMutex.Lock()
	defer c.queueSwitchMutex.Unlock()

	if c.useAdaptiveQueue {
		c.logger.Info("Already using AdaptiveQueue, no switch needed")
		return
	}

	c.logger.Info("Switching to AdaptiveQueue due to high entity count...")

	// Pause worker pools
	c.pulsePool.Pause()
	c.interventionPool.Pause()
	c.codePool.Pause()

	// Drain current queues
	c.drainQueue("Pulse", c.pulseQueue)
	c.drainQueue("Intervention", c.interventionQueue)
	c.drainQueue("Code", c.codeQueue)

	// Create new AdaptiveQueues
	newPulseCfg := queue.DefaultQueueConfig()
	newPulseCfg.Type = queue.QueueTypeAdaptive
	newPulseCfg.Name = "pulse"
	newPulseQueue, err := queue.NewQueue(newPulseCfg)
	if err != nil {
		c.logger.Error("Failed to create new pulse AdaptiveQueue: %v", err)
		return
	}
	newInterventionCfg := queue.DefaultQueueConfig()
	newInterventionCfg.Type = queue.QueueTypeAdaptive
	newInterventionCfg.Name = "intervention"
	newInterventionQueue, err := queue.NewQueue(newInterventionCfg)
	if err != nil {
		c.logger.Error("Failed to create new intervention AdaptiveQueue: %v", err)
		return
	}
	newCodeCfg := queue.DefaultQueueConfig()
	newCodeCfg.Type = queue.QueueTypeAdaptive
	newCodeCfg.Name = "code"
	newCodeQueue, err := queue.NewQueue(newCodeCfg)
	if err != nil {
		c.logger.Error("Failed to create new code AdaptiveQueue: %v", err)
		return
	}

	// Replace queues in worker pools
	if err := c.pulsePool.ReplaceQueue(newPulseQueue); err != nil {
		c.logger.Error("Failed to replace pulse queue: %v", err)
		return
	}
	if err := c.interventionPool.ReplaceQueue(newInterventionQueue); err != nil {
		c.logger.Error("Failed to replace intervention queue: %v", err)
		return
	}
	if err := c.codePool.ReplaceQueue(newCodeQueue); err != nil {
		c.logger.Error("Failed to replace code queue: %v", err)
		return
	}

	// Close old queues and update references
	c.pulseQueue.Close()
	c.interventionQueue.Close()
	c.codeQueue.Close()

	c.pulseQueue = newPulseQueue
	c.interventionQueue = newInterventionQueue
	c.codeQueue = newCodeQueue

	c.useAdaptiveQueue = true

	// Resume worker pools
	c.codePool.Resume()
	c.interventionPool.Resume()
	c.pulsePool.Resume()

	c.logger.Info("Successfully switched to AdaptiveQueue")
}

// drainQueue empties a queue and logs the drained items count.
func (c *Controller) drainQueue(name string, q queue.Queue) {
	drainedCount := 0
	for {
		items, err := q.DequeueBatch(1000)
		if err != nil {
			break
		}
		if len(items) == 0 {
			break
		}
		drainedCount += len(items)
	}
	if drainedCount > 0 {
		c.logger.Info("Drained %d items from %s queue", drainedCount, name)
	}
}

// CheckEntityCountAndSwitchQueue monitors entity count and switches queues if threshold exceeded.
func (c *Controller) CheckEntityCountAndSwitchQueue() {
	if c.entityCountThreshold <= 0 {
		return // No threshold set
	}

	worldStats := c.world.Stats()
	entityCount := int64(worldStats.Entities.Used)

	c.queueSwitchMutex.RLock()
	alreadyAdaptive := c.useAdaptiveQueue
	c.queueSwitchMutex.RUnlock()

	if !alreadyAdaptive && entityCount > c.entityCountThreshold {
		c.logger.Info("Entity count (%d) exceeded threshold (%d), switching to AdaptiveQueue",
			entityCount, c.entityCountThreshold)
		c.switchToAdaptiveQueues()
	}
}
