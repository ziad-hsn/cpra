package controller

import (
    "context"
    "cpra/internal/controller/systems"
    "cpra/internal/queue"
    "fmt"
    "math"
    "log"
    "os"
    "strconv"
    "sync"
    "time"

	"cpra/internal/controller/entities"
	"cpra/internal/controller/components"
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
}

// Config holds all configuration for the controller.
type Config struct {
    StreamingConfig streaming.StreamingConfig
    QueueCapacity   uint64
    WorkerConfig    queue.WorkerPoolConfig
    BatchSize       int
    UpdateInterval  time.Duration
    // Optional pre-sizing parameters; can be overridden by env vars
    // CPRA_SIZING_TAU_MS and CPRA_SIZING_SLO_MS (milliseconds)
    SizingServiceTime time.Duration // τ
    SizingSLO         time.Duration // W target (end-to-end)
    // Optional safe headroom as a fraction (e.g., 0.15 = 15%); env override: CPRA_SIZING_HEADROOM_PCT
    SizingHeadroomPct float64
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
    return Config{
        StreamingConfig: streaming.DefaultStreamingConfig(),
        QueueCapacity:   65536, // Must be a power of 2
        WorkerConfig:    queue.DefaultWorkerPoolConfig(),
        BatchSize:       1000,
        // UpdateInterval removed - ark-tools TPS=100 controls all timing
        SizingServiceTime: 0,
        SizingSLO:         0,
        SizingHeadroomPct: 0,
    }
}

// NewOptimizedController creates a new controller with the refactored systems using ark-tools.
func NewOptimizedController(config Config) *OptimizedController {
	// Create ark-tools app with initial capacity
	arkApp := app.New(1024)
	arkApp.TPS = 100 // High-frequency updates for 1s monitor intervals
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	// Default to Adaptive queues
	pulseQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		log.Fatalf("Failed to create pulse AdaptiveQueue: %v", err)
	}
	interventionQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		log.Fatalf("Failed to create intervention AdaptiveQueue: %v", err)
	}
	codeQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		log.Fatalf("Failed to create code AdaptiveQueue: %v", err)
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

	logger := &LoggerAdapter{logger: SystemLogger}

	// Instantiate the refactored systems with dedicated queues and worker pools.
	pulseRouter := pulsePool.GetRouter()
	interventionRouter := interventionPool.GetRouter()
	codeRouter := codePool.GetRouter()

	pulseScheduleSystem := systems.NewBatchPulseScheduleSystem(world, logger)
	pulseSystem := systems.NewBatchPulseSystem(world, pulseQueue, config.BatchSize, logger)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, pulseRouter.PulseResultChan, logger)

	interventionSystem := systems.NewBatchInterventionSystem(world, interventionQueue, config.BatchSize, logger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, interventionRouter.InterventionResultChan, logger)

	codeSystem := systems.NewBatchCodeSystem(world, codeQueue, config.BatchSize, logger)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, codeRouter.CodeResultChan, logger)

	arkApp.AddSystem(pulseScheduleSystem)
	arkApp.AddSystem(pulseSystem)
	arkApp.AddSystem(interventionSystem)
	arkApp.AddSystem(codeSystem)
	arkApp.AddSystem(pulseResultSystem)
	arkApp.AddSystem(interventionResultSystem)
	arkApp.AddSystem(codeResultSystem)

	return &OptimizedController{
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
	// UpdateInterval logic removed - ark-tools TPS=100 handles all timing

    // Check if we need to switch to AdaptiveQueue due to high entity count
    c.CheckEntityCountAndSwitchQueue()

    // Pre-calculate worker sizing from initial configuration/world (Pulse only)
    c.precomputeSizingFromConfig()
    return nil
}

// precomputeSizingFromConfig computes a recommended worker count from initial world contents
// and configured (or env) service time and latency SLO. It currently targets the Pulse pool only.
func (c *OptimizedController) precomputeSizingFromConfig() {
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
        SystemLogger.Warn("[Pre-Sizing] No active pulse workload detected; skipping sizing")
        return
    }

    cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 0, 0, 0)
    if err != nil {
        SystemLogger.Warn("[Pre-Sizing] Could not compute Pulse workers: %v", err)
        return
    }
    // Determine safe headroom: env CPRA_SIZING_HEADROOM_PCT (e.g., 0.15 or 15), or config, default 0.15
    headroom := c.config.SizingHeadroomPct
    if v := os.Getenv("CPRA_SIZING_HEADROOM_PCT"); v != "" {
        if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
            // Accept both 0.xx and percentage like 15 or 15.0
            if f > 1.0 { headroom = f / 100.0 } else { headroom = f }
        }
    }
    if headroom <= 0 { headroom = 0.15 } // default 15%%
    // Compute a safe recommended c with headroom
    cSafe := int(math.Ceil(float64(cMin) * (1.0 + headroom)))
    if cSafe <= cMin { cSafe = cMin + 1 }
    // Predict W for cSafe (informational)
    mu := 1.0 / tau.Seconds()
    _, wSafe, errSafe := queue.MmcWait(lambda, mu, cSafe, 0, 0)
    if errSafe != nil { wSafe = w } // fallback
    SystemLogger.Info("[Pre-Sizing] Pulse: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
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
func (c *OptimizedController) Start() error {
	if c.running {
		return fmt.Errorf("controller already running")
	}
	c.pulsePool.Start()
	c.interventionPool.Start()
	c.codePool.Start()
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
	c.pulsePool.DrainAndStop()
	c.interventionPool.DrainAndStop()
	c.codePool.DrainAndStop()
	c.PrintShutdownMetrics()
	c.pulseQueue.Close()
	c.interventionQueue.Close()
	c.codeQueue.Close()
	SystemLogger.Info("Controller stopped")
}

// PrintShutdownMetrics logs queue, worker pool, and world statistics at shutdown.
func (c *OptimizedController) PrintShutdownMetrics() {
	logQueue := func(label string, stats queue.Stats) {
		SystemLogger.Info("%s Queue: depth=%d/%d enqueued=%d dequeued=%d dropped=%d", label, stats.QueueDepth, stats.Capacity, stats.Enqueued, stats.Dequeued, stats.Dropped)
		SystemLogger.Info("%s Queue timings: avg_wait=%v max_wait=%v window=%v", label, stats.AvgQueueTime, stats.MaxQueueTime, stats.SampleWindow)
		SystemLogger.Info("%s Queue rates: arrival=%.2f/s service=%.2f/s last_enqueue=%v last_dequeue=%v", label, stats.EnqueueRate, stats.DequeueRate, stats.LastEnqueue, stats.LastDequeue)
	}
	logWorkers := func(label string, stats queue.WorkerPoolStats) {
		SystemLogger.Info("%s Workers: running=%d capacity=%d target=%d min=%d max=%d waiting=%d", label, stats.RunningWorkers, stats.CurrentCapacity, stats.TargetWorkers, stats.MinWorkers, stats.MaxWorkers, stats.WaitingTasks)
		SystemLogger.Info("%s Tasks: submitted=%d completed=%d pending_results=%d scaling_events=%d last_scale=%v", label, stats.TasksSubmitted, stats.TasksCompleted, stats.PendingResults, stats.ScalingEvents, stats.LastScaleTime)
	}

	SystemLogger.Info("=== SHUTDOWN METRICS ===")

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
	SystemLogger.Info("World: entities_used=%d recycled=%d total=%d archetypes=%d components=%d filters=%d locked=%t",
		worldStats.Entities.Used, worldStats.Entities.Recycled, worldStats.Entities.Total,
		len(worldStats.Archetypes), len(worldStats.ComponentTypes), worldStats.CachedFilters, worldStats.Locked)
	SystemLogger.Info("World memory: reserved=%dB used=%dB", worldStats.Memory, worldStats.MemoryUsed)
	SystemLogger.Info("=========================")
}

// GetWorld returns the ECS world for external access (e.g., testing, debugging).
func (c *OptimizedController) GetWorld() *ecs.World {
	return c.world
}

// switchToAdaptiveQueues drains current queues and switches to AdaptiveQueue implementation.
func (c *OptimizedController) switchToAdaptiveQueues() {
	c.queueSwitchMutex.Lock()
	defer c.queueSwitchMutex.Unlock()

	if c.useAdaptiveQueue {
		SystemLogger.Info("Already using AdaptiveQueue, no switch needed")
		return
	}

	SystemLogger.Info("Switching to AdaptiveQueue due to high entity count...")

	// Pause worker pools
	c.pulsePool.Pause()
	c.interventionPool.Pause()
	c.codePool.Pause()

	// Drain current queues
	c.drainQueue("Pulse", c.pulseQueue)
	c.drainQueue("Intervention", c.interventionQueue)
	c.drainQueue("Code", c.codeQueue)

	// Create new AdaptiveQueues
	newPulseQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		SystemLogger.Error("Failed to create new pulse AdaptiveQueue: %v", err)
		return
	}
	newInterventionQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		SystemLogger.Error("Failed to create new intervention AdaptiveQueue: %v", err)
		return
	}
	newCodeQueue, err := queue.NewQueue(queue.DefaultQueueConfig())
	if err != nil {
		SystemLogger.Error("Failed to create new code AdaptiveQueue: %v", err)
		return
	}

	// Replace queues in worker pools
	if err := c.pulsePool.ReplaceQueue(newPulseQueue); err != nil {
		SystemLogger.Error("Failed to replace pulse queue: %v", err)
		return
	}
	if err := c.interventionPool.ReplaceQueue(newInterventionQueue); err != nil {
		SystemLogger.Error("Failed to replace intervention queue: %v", err)
		return
	}
	if err := c.codePool.ReplaceQueue(newCodeQueue); err != nil {
		SystemLogger.Error("Failed to replace code queue: %v", err)
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

	SystemLogger.Info("Successfully switched to AdaptiveQueue")
}

// drainQueue empties a queue and logs the drained items count.
func (c *OptimizedController) drainQueue(name string, q queue.Queue) {
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
		SystemLogger.Info("Drained %d items from %s queue", drainedCount, name)
	}
}

// CheckEntityCountAndSwitchQueue monitors entity count and switches queues if threshold exceeded.
func (c *OptimizedController) CheckEntityCountAndSwitchQueue() {
	if c.entityCountThreshold <= 0 {
		return // No threshold set
	}

	worldStats := c.world.Stats()
	entityCount := int64(worldStats.Entities.Used)

	c.queueSwitchMutex.RLock()
	alreadyAdaptive := c.useAdaptiveQueue
	c.queueSwitchMutex.RUnlock()

	if !alreadyAdaptive && entityCount > c.entityCountThreshold {
		SystemLogger.Info("Entity count (%d) exceeded threshold (%d), switching to AdaptiveQueue",
			entityCount, c.entityCountThreshold)
		c.switchToAdaptiveQueues()
	}
}
