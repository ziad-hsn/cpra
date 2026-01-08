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
// # GC Tuning for Large Deployments
//
// For deployments with 1M+ monitors, GC tuning can significantly impact performance:
//
//   - GOMEMLIMIT: Set to 70-80% of container memory limit (e.g., GOMEMLIMIT=3200MiB for 4GiB container)
//   - GOGC: Start with default (100), then tune based on workload:
//   - Low-latency requirements: Try GOGC=150-200 (fewer, longer collections)
//   - Memory-constrained: Try GOGC=50-75 (more frequent, shorter collections)
//
// Measure before tuning:
//
//	GODEBUG=gctrace=1 ./cpra  // GC trace output
//	go tool pprof http://localhost:6060/debug/pprof/heap  // Heap profile
//
// The controller already uses value-oriented programming to minimize GC pressure:
//   - Components are value types, not pointers
//   - Batch processing amortizes allocation overhead
//   - World.Shrink() reclaims memory after loading
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
	"cpra/internal/controller/commands"
	"cpra/internal/controller/systems"
	"cpra/internal/platform/loader"
	"cpra/internal/logger"
	"cpra/internal/runtime/queue"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"

	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
	"go.uber.org/zap"
)

// Controller manages the ECS world and its systems using ark-tools.
//
// Controller coordinates all aspects of monitor management, including
// entity lifecycle, job queuing, worker pool management, and system execution.
// It provides a high-level API for loading monitors, starting/stopping the system,
// and accessing the underlying ECS world for testing and debugging.
//
// The controller is thread-safe for concurrent access to read-only operations
// like World(). Start() and Stop() should be called from a single goroutine
// or with proper synchronization.
type Controller struct {
	world          *ecs.World
	app            *app.App
	mapper         *entities.EntityManager
	logger         *zap.SugaredLogger
	stateLogger    *systems.StateLogger
	terminationSys *systems.TerminationSystem // System to handle graceful shutdown

	// Managers encapsulate queue and pool operations (SRP improvement)
	queues *QueueManager
	pools  *PoolManager

	// API command channel for async command processing
	// Exposed via CommandChan() for external callers (e.g. Server)
	commandCh chan commands.Command

	runDone chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	config  Config
	mu      sync.Mutex // Protects state transitions during Start/Stop
	running atomic.Bool
}

// NewController creates a new controller with the refactored systems using ark-tools.
//
// NewController initializes:
//   - an ECS world with initial capacity
//   - Three queue instances (pulse, intervention, code) with HybridQueue by default
//   - Three dynamic worker pools for job execution
//   - All batch processing systems
//   - Entity mapper for monitor management
//
// The controller is created in a stopped state. Call Start() to begin processing.
//
// Returns an error if queue or worker pool creation fails.
func NewController(config Config) (*Controller, error) {
	// Create ark-tools app with initial capacity
	arkApp := app.New(defaultECSCapacity)
	arkApp.TPS = defaultTPS // Reduced to lower CPU utilization; shard scheduling keeps precision
	world := &arkApp.World
	shardSlots := calculateShardSlots(arkApp.TPS, config.ShardTargetSweep, config.ShardSlots)
	mapper := entities.NewEntityManager(world)
	mapper.SetShardSlots(shardSlots)

	// Default to Hybrid queues per queue class, using configured capacity
	pulseQueue, err := createQueue("pulse", queue.DropPolicyDropNewest, config.QueueCapacity)
	if err != nil {
		return nil, err
	}
	interventionQueue, err := createQueue("intervention", queue.DropPolicyDropOldest, config.QueueCapacity)
	if err != nil {
		return nil, err
	}
	codeQueue, err := createQueue("code", queue.DropPolicyDropNewest, config.QueueCapacity)
	if err != nil {
		return nil, err
	}

	pulsePool, err := createWorkerPool("Pulse", pulseQueue, config.WorkerConfig, config.EnvConfig)
	if err != nil {
		return nil, err
	}
	interventionPool, err := createWorkerPool("Intervention", interventionQueue, config.WorkerConfig, config.EnvConfig)
	if err != nil {
		return nil, err
	}
	codePool, err := createWorkerPool("Code", codeQueue, config.WorkerConfig, config.EnvConfig)
	if err != nil {
		return nil, err
	}

	// Use the provided logger or create one for CONTROLLER component using centralized config
	ctrlLogger := config.Logger
	if ctrlLogger == nil {
		var err error
		ctrlLogger, err = logger.NewSugaredLoggerWithComponentFromConfig(config.EnvConfig, "CONTROLLER")
		if err != nil {
			return nil, fmt.Errorf("failed to create controller logger: %w", err)
		}
	}

	stateLogger := systems.NewStateLogger(config.Debug)

	// Create managers to encapsulate queue and pool operations
	queues := NewQueueManager(pulseQueue, interventionQueue, codeQueue)
	pools := NewPoolManager(pulsePool, interventionPool, codePool)

	// Instantiate the refactored systems with dedicated queues and worker pools.
	pulseRouter, interventionRouter, codeRouter := pools.GetRouters()

	pulseSystem := systems.NewBatchPulseSystem(world, pulseQueue, config.BatchSize, ctrlLogger, stateLogger, shardSlots)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, pulseRouter.PulseResultChan, ctrlLogger, stateLogger)

	interventionSystem := systems.NewBatchInterventionSystem(world, interventionQueue, config.BatchSize, ctrlLogger, stateLogger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, interventionRouter.InterventionResultChan, ctrlLogger, stateLogger)

	codeSystem := systems.NewBatchCodeSystem(world, codeQueue, config.BatchSize, ctrlLogger, stateLogger)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, codeRouter.CodeResultChan, ctrlLogger, stateLogger)
	pendingUpdateSystem := systems.NewPendingUpdateSystem(world, mapper, ctrlLogger)

	// TerminationSystem monitors the context and signals termination from within the ECS loop
	// This avoids race conditions with external writers to the Termination resource
	terminationSystem := systems.NewTerminationSystem(nil) // Context set in Start()

	// Create command channel for API-to-ECS communication
	cmdCapacity := config.APICommandCapacity
	if cmdCapacity <= 0 {
		cmdCapacity = 1000
	}
	commandCh := make(chan commands.Command, cmdCapacity)

	// APICommandSystem processes API commands during the ECS tick loop
	apiCmdSystem := systems.NewAPICommandSystem(commandCh, mapper, config.APICommandLimit, ctrlLogger)

	arkApp.AddSystem(terminationSystem) // Add first so it runs early in the tick
	arkApp.AddSystem(apiCmdSystem)      // Process API commands before job systems
	arkApp.AddSystem(pulseSystem)
	arkApp.AddSystem(interventionSystem)
	arkApp.AddSystem(codeSystem)
	arkApp.AddSystem(pulseResultSystem)
	arkApp.AddSystem(interventionResultSystem)
	arkApp.AddSystem(codeResultSystem)
	arkApp.AddSystem(pendingUpdateSystem)

	return &Controller{
		app:            arkApp,
		world:          world,
		mapper:         mapper,
		terminationSys: terminationSystem,
		queues:         queues,
		pools:          pools,
		commandCh:      commandCh,
		config:         config,
		stateLogger:    stateLogger,
		logger:         ctrlLogger,
	}, nil
}

// LoadMonitors loads monitors from a YAML file using the loader.
//
// LoadMonitors parses the file concurrently, creates ECS entities for each monitor,
// and initializes all required components. It supports YAML formats with optional
// gzip compression (.gz extension).
//
// After loading completes, the controller:
//   - Pre-computes optimal worker pool sizing for pulse jobs
//
// The context can be used to cancel the loading operation.
//
// Returns an error if file parsing or entity creation fails.
func (c *Controller) LoadMonitors(ctx context.Context, filename string) error {
	// Get file size for progress reporting
	var totalBytes int64
	if stat, err := os.Stat(filename); err == nil {
		totalBytes = stat.Size()
	}

	// Set up progress reporting to stderr
	pipelineConfig := c.config.PipelineConfig
	progressCallback, progressComplete := loader.DefaultProgressCallback(os.Stderr, totalBytes)
	pipelineConfig.ProgressCallback = progressCallback

	pipeline := loader.NewPipeline(c.world, c.mapper, pipelineConfig)
	stats, err := pipeline.Load(ctx, filename)

	// Complete the progress bar
	progressComplete()

	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	c.logger.Infof("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.EntitiesCreated, stats.LoadingTime, stats.CreationRate)

	// Shrink the world incrementally to reclaim over-allocated memory.
	// We use a small time budget per pass to allow context cancellation.
	shrinkPasses := 0
	for c.world.Shrink(shrinkBudget) {
		shrinkPasses++
		if ctx.Err() != nil {
			return fmt.Errorf("loading cancelled during memory shrink: %w", ctx.Err())
		}
	}
	c.logger.Infof("Shrunk world memory in %d passes", shrinkPasses+1)

	// Explicitly trigger GC and release memory to OS to clear fragmentation from loading/shrinking
	runtime.GC()
	debug.FreeOSMemory()

	// Log archetype stats for reflect.New analysis
	worldStats := c.world.Stats()
	c.logger.Infof("ECS Archetypes: %d (more archetypes = more reflect.New)", len(worldStats.Archetypes))
	for i, arch := range worldStats.Archetypes {
		c.logger.Infof("  Archetype[%d]: entities=%d components=%v", i, arch.Size, arch.ComponentTypeNames)
	}

	// Pre-calculate worker sizing from initial configuration/world (Pulse only)
	c.precomputeSizingFromConfig()
	return nil
}

// Start begins the main processing loop of the controller.
//
// Start initializes all worker pools and begins the ark-tools app execution loop.
// The controller runs at 10 TPS (ticks per second) for updates, combined with shard
// scheduling to achieve sub-second precision for monitor intervals.
//
// This method is idempotent - calling Start() multiple times returns an error
// if the controller is already running.
//
// Returns an error if the controller is already running or if startup fails.
func (c *Controller) Start(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return fmt.Errorf("controller already running")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.runDone = make(chan struct{})

	// Set context on termination system - it will signal termination from within the ECS loop
	c.terminationSys.SetContext(c.ctx)

	// Start all worker pools using the pool manager
	c.pools.SetContext(c.ctx)
	c.pools.StartAll()

	go func() {
		defer close(c.runDone)
		c.app.Run()
	}()
	c.logger.Infof("Controller started successfully")
	return nil
}

// Stop gracefully shuts down the controller.
//
// Stop performs a graceful shutdown sequence:
//   - Signals termination and waits for the ECS app goroutine to exit
//   - Drains and stops all worker pools
//   - Closes all queues
//   - Logs shutdown metrics
//
// This method is idempotent - calling Stop() multiple times is safe.
// After Stop() completes, the controller cannot be restarted; create a new
// controller instance if needed.
func (c *Controller) Stop() {
	if !c.running.Swap(false) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Infof("Stopping controller...")

	// Step 1: Cancel context (signals worker pools and TerminationSystem)
	// The TerminationSystem will set Termination.Terminate from within the ECS loop,
	// avoiding a data race with the app's read of that flag.
	if c.cancel != nil {
		c.cancel()
	}

	// Step 2: Wait for the app.Run() goroutine to exit
	// This ensures no concurrent access to ECS resources after this point
	runFinalized := false
	if done := c.runDone; done != nil {
		select {
		case <-done:
			runFinalized = true
			c.logger.Infof("  [1/5] ECS app exited cleanly")
		case <-time.After(shutdownTimeout):
			c.logger.Warnf("  [1/5] ECS app did not exit within timeout, forcing finalize")
		}
	}

	// Step 3: Finalize ECS systems if app didn't exit cleanly
	// Now safe to call - app goroutine is either done or we timed out
	if !runFinalized {
		c.app.Finalize()
	}

	// Step 4: Drain worker pools (wait for in-flight jobs to complete)
	// Order: pulse -> intervention -> code (follows dependency chain)
	c.logger.Infof("  [2/5] Draining worker pools...")
	c.pools.DrainAll()

	// Step 4.5: Log pending jobs that will be dropped on close
	queueStats := c.queues.Stats()
	if queueStats.TotalDepth > 0 {
		c.logger.Warnf("Shutdown: dropping %d pending jobs (pulse=%d, intervention=%d, code=%d)",
			queueStats.TotalDepth, queueStats.Pulse.QueueDepth, queueStats.Intervention.QueueDepth, queueStats.Code.QueueDepth)
	}

	// Step 5: Drain queues to drop pending jobs before close
	c.logger.Infof("  [3/5] Draining queues...")
	c.queues.DrainAll()

	// Step 6: Close queues (no more enqueue/dequeue operations)
	c.logger.Infof("  [4/5] Closing queues...")
	c.queues.CloseAll()

	// Step 7: Print final metrics (after everything is stopped for accurate stats)
	c.logger.Infof("  [5/5] Collecting final metrics...")
	c.PrintShutdownMetrics()

	c.logger.Infof("Controller stopped successfully")
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
		c.logger.Infof("%s Queue: depth=%d/%d enqueued=%d dequeued=%d dropped=%d", label, stats.QueueDepth, stats.Capacity, stats.Enqueued, stats.Dequeued, stats.Dropped)
		// Recent metrics first (more representative of current state)
		if stats.RollingSamples > 0 {
			c.logger.Infof("%s Queue recent: rolling_avg=%s p50=%s p95=%s ema=%s samples=%d", label, formatDur(stats.RollingAvgWait), formatDur(stats.RollingP50Wait), formatDur(stats.RollingP95Wait), formatDur(stats.EMAWait), stats.RollingSamples)
		} else {
			c.logger.Infof("%s Queue recent: no samples yet", label)
		}
		c.logger.Infof("%s Queue last_minute: avg=%s max=%s samples=%d", label, formatDur(stats.BucketAvg1m), formatDur(stats.BucketMax1m), stats.BucketCnt1m)
		// Lifetime averages (can be skewed by startup/shutdown)
		c.logger.Infof("%s Queue lifetime: avg_wait=%s max_wait=%s window=%s", label, formatDur(stats.AvgQueueTime), formatDur(stats.MaxQueueTime), formatDur(stats.SampleWindow))
		c.logger.Infof("%s Queue rates: arrival=%.2f/s service=%.2f/s last_enqueue=%s last_dequeue=%s", label, stats.EnqueueRate, stats.DequeueRate, formatTS(stats.LastEnqueue), formatTS(stats.LastDequeue))
	}
	logWorkers := func(label string, stats queue.WorkerPoolStats) {
		c.logger.Infof("%s Workers: running=%d capacity=%d target=%d min=%d max=%d waiting=%d", label, stats.RunningWorkers, stats.CurrentCapacity, stats.TargetWorkers, stats.MinWorkers, stats.MaxWorkers, stats.WaitingTasks)
		c.logger.Infof("%s Tasks: submitted=%d completed=%d pending_results=%d scaling_events=%d last_scale=%s", label, stats.TasksSubmitted, stats.TasksCompleted, stats.PendingResults, stats.ScalingEvents, formatTS(stats.LastScaleTime))
	}

	c.logger.Infof("=== SHUTDOWN METRICS ===")

	qStats := c.queues.Stats()
	logQueue("Pulse", qStats.Pulse)
	logQueue("Intervention", qStats.Intervention)
	logQueue("Code", qStats.Code)

	pStats := c.pools.Stats()
	logWorkers("Pulse", pStats.Pulse)
	logWorkers("Intervention", pStats.Intervention)
	logWorkers("Code", pStats.Code)

	worldStats := c.world.Stats()
	c.logger.Infof("World: entities_used=%d recycled=%d total=%d archetypes=%d components=%d filters=%d locked=%t",
		worldStats.Entities.Used, worldStats.Entities.Recycled, worldStats.Entities.Total,
		len(worldStats.Archetypes), len(worldStats.ComponentTypes), worldStats.CachedFilters, worldStats.Locked)
	c.logger.Infof("World memory: reserved=%s used=%s", humanize.IBytes(uint64(worldStats.Memory)), humanize.IBytes(uint64(worldStats.MemoryUsed)))
	c.logger.Infof("=========================")
}

// World returns the ECS world for external access (e.g., testing, debugging).
//
// World provides direct access to the underlying ECS world. This is useful for:
//   - Testing: Inspecting entities and components in tests
//   - Debugging: Querying entity state during development
//   - Metrics: Accessing world statistics
//
// The returned world should not be modified directly while the controller is running,
// as this may cause race conditions with the ECS systems.
func (c *Controller) World() *ecs.World {
	return c.world
}

// CommandChan returns a send-only channel for API commands.
//
// This channel is used by external components (e.g., Server/API handlers) to
// send commands to the ECS loop. Commands are processed by APICommandSystem
// during each tick.
//
// The channel is buffered (default: 1000). Callers should handle the case
// where the channel is full by implementing appropriate backpressure.
//
// Each command has a result channel that callers can wait on for synchronous
// UX (e.g., CLI) or ignore for fire-and-forget semantics (e.g., high-throughput).
func (c *Controller) CommandChan() chan<- commands.Command {
	return c.commandCh
}
