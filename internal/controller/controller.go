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
	"cpra/internal/controller/systems"
	"cpra/internal/loader"
	"cpra/internal/logger"
	"cpra/internal/queue"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark-tools/resource"
	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/ecs/stats"
	"go.uber.org/zap"
)

const (
	// by defaultECSCapacity is the initial entity capacity for the ECS world.
	defaultECSCapacity = 1024

	// by defaultTPS is the ticks per second for the ark-tools scheduler.
	defaultTPS = 10

	// defaultServiceTime is the assumed job execution time for worker sizing.
	defaultServiceTime = 20 * time.Millisecond

	// by defaultSLO is the target end-to-end latency for worker sizing.
	defaultSLO = 200 * time.Millisecond

	// defaultHeadroom is the safety margin percentage for worker sizing.
	defaultHeadroom = 0.15

	// interventionPoolRatio is the fraction of pulse pool size for intervention workers.
	interventionPoolRatio = 0.25

	// codePoolRatio is the fraction of pulse pool size for code workers.
	codePoolRatio = 0.125

	// shutdownTimeout is the maximum wait time for a graceful shutdown.
	shutdownTimeout = 5 * time.Second

	// shrinkBudget is the time budget per incremental memory shrink pass.
	shrinkBudget = 10 * time.Millisecond
)

// keepConstantsReferenced guards against staticcheck false positives on const usage.
var (
	_ = defaultServiceTime
	_ = defaultSLO
	_ = defaultHeadroom
	_ = interventionPoolRatio
	_ = codePoolRatio
	_ = shutdownTimeout
)

// calculateShardSlots determines shard slots based on TPS and desired sweep duration,
// unless an explicit override is provided.
func calculateShardSlots(tps float64, targetSweep time.Duration, override int) int {
	if override > 0 {
		return override
	}

	// Default sweep of the 10s if unset or non-positive
	if targetSweep <= 0 {
		targetSweep = 10 * time.Second
	}

	slots := int(math.Ceil(tps * targetSweep.Seconds()))
	if slots < 1 {
		slots = 1
	}

	// Clamp to a reasonable upper bound to prevent runaway slot counts.
	const maxSlots = 20000
	if slots > maxSlots {
		slots = maxSlots
	}
	return slots
}

// createWorkerPool creates a dynamic worker pool for the given queue.
func createWorkerPool(name string, q queue.Queue, config queue.WorkerPoolConfig) (*queue.DynamicWorkerPool, error) {
	logger := log.New(os.Stdout, fmt.Sprintf("[%sPool] ", name), log.LstdFlags)
	pool, err := queue.NewDynamicWorkerPool(context.Background(), q, config, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s worker pool: %w", name, err)
	}
	return pool, nil
}

// createQueue creates a named hybrid queue with the specified drop policy.
func createQueue(name string, dropPolicy queue.DropPolicy) (queue.Queue, error) {
	cfg := queue.DefaultQueueConfig()
	cfg.Name = name
	cfg.HybridConfig.Name = name
	cfg.HybridConfig.DropPolicy = dropPolicy
	q, err := queue.NewQueue(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s HybridQueue: %w", name, err)
	}
	return q, nil
}

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
	world             *ecs.World
	app               *app.App
	mapper            *entities.EntityManager
	logger            *zap.SugaredLogger
	stateLogger       *systems.StateLogger
	pulsePool         *queue.DynamicWorkerPool
	interventionPool  *queue.DynamicWorkerPool
	codePool          *queue.DynamicWorkerPool
	pulseQueue        queue.Queue
	interventionQueue queue.Queue
	codeQueue         queue.Queue
	runDone           chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	config            Config
	running           atomic.Bool
}

// Stats ControllerStats aggregates runtime statistics for queues, worker pools, and the ECS world.
type Stats struct {
	PulseQueue          queue.Stats           `json:"pulse_queue"`
	InterventionQueue   queue.Stats           `json:"intervention_queue"`
	CodeQueue           queue.Stats           `json:"code_queue"`
	PulseWorkers        queue.WorkerPoolStats `json:"pulse_workers"`
	InterventionWorkers queue.WorkerPoolStats `json:"intervention_workers"`
	CodeWorkers         queue.WorkerPoolStats `json:"code_workers"`
	World               *stats.World          `json:"world"`
}

// Stats return a snapshot of controller runtime statistics.
func (c *Controller) Stats() Stats {
	return Stats{
		PulseQueue:          c.pulseQueue.Stats(),
		InterventionQueue:   c.interventionQueue.Stats(),
		CodeQueue:           c.codeQueue.Stats(),
		PulseWorkers:        c.pulsePool.Stats(),
		InterventionWorkers: c.interventionPool.Stats(),
		CodeWorkers:         c.codePool.Stats(),
		World:               c.world.Stats(),
	}
}

// Config holds all configuration for the controller.
//
// Configuration can be set programmatically or via environment variables
// for sizing parameters (CPRA_SIZING_TAU_MS, CPRA_SIZING_SLO_MS, CPRA_SIZING_HEADROOM_PCT).
//
// Default values are optimized for large-scale deployments but can be adjusted
// based on workload characteristics and resource constraints.
type Config struct {
	Logger            *zap.SugaredLogger
	WorkerConfig      queue.WorkerPoolConfig
	PipelineConfig    loader.PipelineConfig
	QueueCapacity     uint64
	BatchSize         int
	UpdateInterval    time.Duration
	SizingServiceTime time.Duration
	SizingSLO         time.Duration
	SizingHeadroomPct float64
	Debug             bool

	// Shard tuning
	ShardSlots       int           // Explicit shard slot count; if <=0, auto-calculated
	ShardTargetSweep time.Duration // Desired full sweep duration across all shards; used when ShardSlots <= 0
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
		ShardSlots:        0,
		ShardTargetSweep:  10 * time.Second, // aim for ~10s sweep by default
	}
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

	// Default to Hybrid queues per queue class
	pulseQueue, err := createQueue("pulse", queue.DropPolicyDropNewest)
	if err != nil {
		return nil, err
	}
	interventionQueue, err := createQueue("intervention", queue.DropPolicyDropOldest)
	if err != nil {
		return nil, err
	}
	codeQueue, err := createQueue("code", queue.DropPolicyDropNewest)
	if err != nil {
		return nil, err
	}

	pulsePool, err := createWorkerPool("Pulse", pulseQueue, config.WorkerConfig)
	if err != nil {
		return nil, err
	}
	interventionPool, err := createWorkerPool("Intervention", interventionQueue, config.WorkerConfig)
	if err != nil {
		return nil, err
	}
	codePool, err := createWorkerPool("Code", codeQueue, config.WorkerConfig)
	if err != nil {
		return nil, err
	}

	stateLogger := systems.NewStateLogger(config.Debug)

	// Use the provided logger or create one for CONTROLLER component
	ctrlLogger := config.Logger
	if ctrlLogger == nil {
		cfg := logger.DefaultConfig()
		if config.Debug {
			cfg = logger.DevelopmentConfig()
		}
		var err error
		ctrlLogger, err = logger.NewSugaredLoggerWithComponent("CONTROLLER", cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create controller logger: %w", err)
		}
	}

	// Instantiate the refactored systems with dedicated queues and worker pools.
	pulseRouter := pulsePool.GetRouter()
	interventionRouter := interventionPool.GetRouter()
	codeRouter := codePool.GetRouter()

	pulseSystem := systems.NewBatchPulseSystem(world, pulseQueue, config.BatchSize, ctrlLogger, stateLogger, shardSlots)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, pulseRouter.PulseResultChan, ctrlLogger, stateLogger)

	interventionSystem := systems.NewBatchInterventionSystem(world, interventionQueue, config.BatchSize, ctrlLogger, stateLogger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, interventionRouter.InterventionResultChan, ctrlLogger, stateLogger)

	codeSystem := systems.NewBatchCodeSystem(world, codeQueue, config.BatchSize, ctrlLogger, stateLogger)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, codeRouter.CodeResultChan, ctrlLogger, stateLogger)

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
		tau = defaultServiceTime
	}
	wSLO := c.config.SizingSLO
	if v := os.Getenv("CPRA_SIZING_SLO_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			wSLO = time.Duration(ms) * time.Millisecond
		}
	}
	if wSLO <= 0 {
		wSLO = defaultSLO
	}

	// Compute λ for Pulse from world: sum over active monitors of 1/Interval
	lambda := computePulseLambda(c.world)
	if lambda <= 0 {
		c.logger.Warnf("[Pre-Sizing] No active pulse workload detected; skipping sizing")
		return
	}

	cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 0, 0, 0)
	if err != nil {
		c.logger.Warnf("[Pre-Sizing] Could not compute Pulse workers: %v", err)
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
		headroom = defaultHeadroom
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
	c.logger.Infof("[Pre-Sizing] Pulse: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
		lambda, tau.Seconds(), wSLO.Seconds(), cMin, w, cSafe, headroom*100.0, wSafe)

	// APPLY the calculated sizing to worker pools (not just log!)
	// Only tune if calculated size exceeds current minimum
	if cSafe > c.config.WorkerConfig.MinWorkers {
		c.pulsePool.Tune(cSafe)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Pulse pool", cSafe)

		// Scale Intervention and Code pools proportionally (typically lower volume)
		// Use ratio of pulse pool as baseline - these handle triggered actions
		interventionSize := int(math.Ceil(float64(cSafe) * interventionPoolRatio))
		if interventionSize < c.config.WorkerConfig.MinWorkers {
			interventionSize = c.config.WorkerConfig.MinWorkers
		}
		c.interventionPool.Tune(interventionSize)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Intervention pool (25%% of pulse)", interventionSize)

		// Code evaluations are even less frequent
		codeSize := int(math.Ceil(float64(cSafe) * codePoolRatio))
		if codeSize < c.config.WorkerConfig.MinWorkers {
			codeSize = c.config.WorkerConfig.MinWorkers
		}
		c.codePool.Tune(codeSize)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Code pool (12.5%% of pulse)", codeSize)
	}
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
// The controller runs at 10 TPS (ticks per second) for updates, combined with shard
// scheduling to achieve sub-second precision for monitor intervals.
//
// This method is idempotent - calling Start() multiple times returns an error
// if the controller is already running.
//
// Returns an error if the controller is already running or if startup fails.
func (c *Controller) Start(ctx context.Context) error {
	if c.running.Swap(true) {
		return fmt.Errorf("controller already running")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.runDone = make(chan struct{})
	c.pulsePool.SetContext(c.ctx)
	c.interventionPool.SetContext(c.ctx)
	c.codePool.SetContext(c.ctx)
	c.pulsePool.Start()
	c.interventionPool.Start()
	c.codePool.Start()
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
//   - Finalizes the ark-tools app (stops all systems)
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
	c.logger.Infof("Stopping controller...")
	if c.cancel != nil {
		c.cancel()
	}

	// Signal ark app to terminate and wait for the run loop to exit
	termination := ecs.GetResource[resource.Termination](c.world)
	termination.Terminate = true

	runFinalized := false
	if done := c.runDone; done != nil {
		select {
		case <-done:
			runFinalized = true
		case <-time.After(shutdownTimeout):
			c.logger.Warnf("Run goroutine did not exit within timeout")
		}
	}

	// Step 1: Stop ECS systems (stops scheduling new work) if not already finalized
	if !runFinalized {
		c.logger.Infof("  [1/4] Finalizing ECS systems...")
		c.app.Finalize()
	} else {
		c.logger.Infof("  [1/4] ECS systems already finalized")
	}

	// Step 2: Drain worker pools (wait for in-flight jobs to complete)
	// Order: pulse -> intervention -> code (follows dependency chain)
	c.logger.Infof("  [2/4] Draining worker pools...")
	c.logger.Infof("    - Draining pulse pool...")
	c.pulsePool.DrainAndStop()
	c.logger.Infof("    - Draining intervention pool...")
	c.interventionPool.DrainAndStop()
	c.logger.Infof("    - Draining code pool...")
	c.codePool.DrainAndStop()

	// Step 3: Close queues (no more enqueue/dequeue operations)
	c.logger.Infof("  [3/4] Closing queues...")
	c.pulseQueue.Close()
	c.interventionQueue.Close()
	c.codeQueue.Close()

	// Step 4: Print final metrics (after everything is stopped for accurate stats)
	c.logger.Infof("  [4/4] Collecting final metrics...")
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
		c.logger.Infof("%s Queue timings: avg_wait=%s max_wait=%s window=%s", label, formatDur(stats.AvgQueueTime), formatDur(stats.MaxQueueTime), formatDur(stats.SampleWindow))
		c.logger.Infof("%s Queue rates: arrival=%.2f/s service=%.2f/s last_enqueue=%s last_dequeue=%s", label, stats.EnqueueRate, stats.DequeueRate, formatTS(stats.LastEnqueue), formatTS(stats.LastDequeue))
	}
	logWorkers := func(label string, stats queue.WorkerPoolStats) {
		c.logger.Infof("%s Workers: running=%d capacity=%d target=%d min=%d max=%d waiting=%d", label, stats.RunningWorkers, stats.CurrentCapacity, stats.TargetWorkers, stats.MinWorkers, stats.MaxWorkers, stats.WaitingTasks)
		c.logger.Infof("%s Tasks: submitted=%d completed=%d pending_results=%d scaling_events=%d last_scale=%s", label, stats.TasksSubmitted, stats.TasksCompleted, stats.PendingResults, stats.ScalingEvents, formatTS(stats.LastScaleTime))
	}

	c.logger.Infof("=== SHUTDOWN METRICS ===")

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

	worldStats := c.world.Stats()
	c.logger.Infof("World: entities_used=%d recycled=%d total=%d archetypes=%d components=%d filters=%d locked=%t",
		worldStats.Entities.Used, worldStats.Entities.Recycled, worldStats.Entities.Total,
		len(worldStats.Archetypes), len(worldStats.ComponentTypes), worldStats.CachedFilters, worldStats.Locked)
	c.logger.Infof("World memory: reserved=%dB used=%dB", worldStats.Memory, worldStats.MemoryUsed)
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
