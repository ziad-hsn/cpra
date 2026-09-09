package controller

import (
	"context"
	"cpra/internal/alerts"
	"cpra/internal/controller/systems"
	"cpra/internal/queue"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/loader/streaming"
	"cpra/internal/web/snapshot"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// defaultServiceTime is the initial service-time estimate for worker sizing.
// Set CPRA_SIZING_TAU_MS to an estimate for the actual workload.
const defaultServiceTime = 4 * time.Millisecond

// LoggerAdapter adapts the controller loggers to the systems interface.
// It also forwards system performance samples to the MetricsAggregator so the
// web server can expose per-system metrics without each system having to know
// about the aggregator.
type LoggerAdapter struct {
	logger interface {
		Info(format string, args ...interface{})
		Debug(format string, args ...interface{})
		Warn(format string, args ...interface{})
		Error(format string, args ...interface{})
		LogSystemPerformance(name string, duration time.Duration, count int)
	}
	metrics *MetricsAggregator
}

func (l *LoggerAdapter) Info(format string, args ...interface{})  { l.logger.Info(format, args...) }
func (l *LoggerAdapter) Debug(format string, args ...interface{}) { l.logger.Debug(format, args...) }
func (l *LoggerAdapter) Warn(format string, args ...interface{})  { l.logger.Warn(format, args...) }
func (l *LoggerAdapter) Error(format string, args ...interface{}) { l.logger.Error(format, args...) }
func (l *LoggerAdapter) LogSystemPerformance(name string, duration time.Duration, count int) {
	l.logger.LogSystemPerformance(name, duration, count)
	if l.metrics != nil {
		l.metrics.RecordSystemUpdate(name, duration, int64(count), 0)
	}
}
func (l *LoggerAdapter) LogComponentState(entityID uint32, component string, action string) {
	l.logger.Debug("Entity[%d] component %s: %s", entityID, component, action)
}

// Controller manages the ECS world and its systems using ark-tools.
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
	lifecycleMu          sync.Mutex
	stopCh               chan struct{}
	doneCh               chan struct{}
	resultSystems        []app.System
	useAdaptiveQueue     bool

	// Observability surfaces for the web server.
	metricsAgg     *MetricsAggregator
	snapshotHolder *snapshot.Holder
}

// Config holds all configuration for the controller.
type Config struct {
	Debug           bool
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
	// AlertCooldown is the minimum interval between alerts of the same color
	// for a single monitor. Zero or negative falls back to a default.
	AlertCooldown time.Duration
	// RecoveryBypass allows recovery colors (green) to bypass the cooldown so
	// recovery notices are always dispatched immediately.
	RecoveryBypass bool
	// EntityCountThreshold, when > 0, selects AdaptiveQueue during loading
	// before Start when the entity count exceeds it. Zero disables selection.
	// Set via the CPRA_ENTITY_THRESHOLD env var or config.
	EntityCountThreshold int64
	// SnapshotInterval is how often the dashboard stats snapshot is rebuilt.
	// The snapshot is an O(N) scan of every monitor that runs inside the ECS
	// tick loop, so a too-small interval stalls the pipeline on large fleets.
	// Zero or negative falls back to 5s.
	SnapshotInterval time.Duration
	// TPS is the ark-tools tick rate (ticks/sec). Zero falls back to 10.
	TPS int
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		StreamingConfig:   streaming.DefaultStreamingConfig(),
		QueueCapacity:     65536, // Must be a power of 2
		WorkerConfig:      queue.DefaultWorkerPoolConfig(),
		BatchSize:         1000,
		SizingServiceTime: 0,
		SizingSLO:         0,
		SizingHeadroomPct: 0,
		AlertCooldown:     5 * time.Minute,
		RecoveryBypass:    true,
		SnapshotInterval:  5 * time.Second,
		TPS:               200,
	}
}

// NewController creates a new controller with its queues, workers and ECS systems.
func NewController(config Config) *Controller {
	// Create ark-tools app with initial capacity
	arkApp := app.New(1024)
	tps := config.TPS
	if tps <= 0 {
		tps = 10
	}
	arkApp.TPS = float64(tps) // High-frequency updates for 1s monitor intervals
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
	interventionCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
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
	pulsePool, err := queue.NewDynamicWorkerPool(queue.NewHandle(pulseQueue), config.WorkerConfig, pulseLogger)
	if err != nil {
		log.Fatalf("Failed to create pulse worker pool: %v", err)
	}
	interventionLogger := log.New(os.Stdout, "[InterventionPool] ", log.LstdFlags)
	interventionPool, err := queue.NewDynamicWorkerPool(queue.NewHandle(interventionQueue), config.WorkerConfig, interventionLogger)
	if err != nil {
		log.Fatalf("Failed to create intervention worker pool: %v", err)
	}
	codeLogger := log.New(os.Stdout, "[CodePool] ", log.LstdFlags)
	codePool, err := queue.NewDynamicWorkerPool(queue.NewHandle(codeQueue), config.WorkerConfig, codeLogger)
	if err != nil {
		log.Fatalf("Failed to create code worker pool: %v", err)
	}

	pulseQueue = pulsePool.Queue()
	interventionQueue = interventionPool.Queue()
	codeQueue = codePool.Queue()

	stateLogger := systems.NewStateLogger(config.Debug)
	metricsAgg := NewMetricsAggregator()
	snapshotHolder := snapshot.NewHolder()
	logger := &LoggerAdapter{logger: SystemLogger, metrics: metricsAgg}

	// Construct the alert manager once and share it across the systems that
	// trigger, enqueue, and observe code alerts.
	alertCooldown := config.AlertCooldown
	if alertCooldown <= 0 {
		alertCooldown = 5 * time.Minute
	}
	alertPolicy := alerts.NewDefaultPolicy(world, config.RecoveryBypass)
	alertMgr := alerts.NewManager(world, alertPolicy, alertCooldown)

	// Connect systems to their queues and worker pools.
	pulseRouter := pulsePool.GetRouter()
	interventionRouter := interventionPool.GetRouter()
	codeRouter := codePool.GetRouter()

	pulseSched := systems.NewPulseScheduler()
	interventionReady := &systems.ReadyQueue{}
	codeSched := systems.NewCodeScheduler()

	pulseScheduleSystem := systems.NewBatchPulseScheduleSystem(world, pulseSched, logger, stateLogger)
	pulseSystem := systems.NewBatchPulseSystem(world, pulseQueue, pulseSched, config.BatchSize, logger, stateLogger)
	pulseResultSystem := systems.NewBatchPulseResultSystem(world, pulseRouter.PulseResultChan, pulseSched, interventionReady, codeSched, logger, stateLogger, alertMgr)

	interventionSystem := systems.NewBatchInterventionSystem(world, interventionQueue, interventionReady, config.BatchSize, logger, stateLogger)
	interventionResultSystem := systems.NewBatchInterventionResultSystem(world, interventionRouter.InterventionResultChan, codeSched, logger, stateLogger, alertMgr)

	codeScheduleSystem := systems.NewBatchCodeScheduleSystem(world, codeSched, logger, stateLogger)
	codeSystem := systems.NewBatchCodeSystem(world, codeQueue, codeSched, config.BatchSize, logger, stateLogger, alertMgr)
	codeResultSystem := systems.NewBatchCodeResultSystem(world, codeRouter.CodeResultChan, codeSched, logger, stateLogger, alertMgr)

	arkApp.AddSystem(pulseScheduleSystem)
	arkApp.AddSystem(pulseSystem)
	arkApp.AddSystem(interventionSystem)
	arkApp.AddSystem(codeScheduleSystem)
	arkApp.AddSystem(codeSystem)
	arkApp.AddSystem(pulseResultSystem)
	arkApp.AddSystem(interventionResultSystem)
	arkApp.AddSystem(codeResultSystem)

	// Snapshot system publishes a read-only fleet projection for the web
	// server. It reads the world only inside its tick, throttled to
	// SnapshotInterval (default 5s) because the O(N) scan stalls the pipeline
	// on large fleets.
	snapshotInterval := config.SnapshotInterval
	if snapshotInterval <= 0 {
		snapshotInterval = 5 * time.Second
	}
	statsSnapshotSystem := systems.NewBatchStatsSnapshotSystem(world, logger, snapshotHolder, snapshotInterval, 1_000_000)
	arkApp.AddSystem(statsSnapshotSystem)

	// Drain pulse results a second time at the END of the tick. The result
	// system runs once per tick by default, so a result is applied on the NEXT
	// tick (~1 tick of round-trip latency). Draining again here applies the
	// results that arrived since the first drain in the SAME tick, shrinking
	// the round-trip below one tick.
	arkApp.AddSystem(pulseResultSystem)

	return &Controller{
		app:                  arkApp,
		world:                world,
		mapper:               mapper,
		pulseQueue:           pulseQueue,
		interventionQueue:    interventionQueue,
		codeQueue:            codeQueue,
		pulsePool:            pulsePool,
		interventionPool:     interventionPool,
		codePool:             codePool,
		config:               config,
		entityCountThreshold: config.EntityCountThreshold,
		stateLogger:          stateLogger,
		metricsAgg:           metricsAgg,
		snapshotHolder:       snapshotHolder,
		resultSystems:        []app.System{pulseResultSystem, interventionResultSystem, codeResultSystem, statsSnapshotSystem},
	}
}

// LoadMonitors loads monitors using the streaming loader.
func (c *Controller) LoadMonitors(ctx context.Context, filename string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.doneCh != nil {
		return fmt.Errorf("monitors must be loaded before starting the controller")
	}
	loader := streaming.NewStreamingLoader(filename, c.world, c.config.StreamingConfig)
	stats, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	SystemLogger.Info("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.TotalEntities, stats.LoadingTime, stats.CreationRate)

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
		tau = defaultServiceTime
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
	if headroom <= 0 || math.IsNaN(headroom) || math.IsInf(headroom, 0) {
		headroom = 0.15
	} // default 15%%
	c.pulsePool.SetSizingPolicy(wSLO, headroom)
	cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 1, 1, c.pulsePool.Stats().MaxWorkers)
	if err != nil {
		SystemLogger.Warn("[Pre-Sizing] Could not compute Pulse workers: %v", err)
		if errors.Is(err, queue.ErrNoFeasibleCapacity) {
			target := float64(c.pulsePool.Stats().MaxWorkers)
			if wSLO <= tau {
				target = math.Min(target, math.Ceil(lambda*tau.Seconds()*(1+headroom)))
			}
			c.pulsePool.SetTargetWorkers(int(target))
		}
		return
	}
	// Compute a safe recommended c with headroom
	cSafe := int(math.Min(float64(c.pulsePool.Stats().MaxWorkers), math.Ceil(float64(cMin)*(1+headroom))))
	// Predict W for cSafe (informational)
	mu := 1.0 / tau.Seconds()
	_, wSafe, errSafe := queue.MmcWait(lambda, mu, cSafe, 0, 0)
	if errSafe != nil {
		wSafe = w
	} // fallback
	SystemLogger.Info("[Pre-Sizing] Pulse: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
		lambda, tau.Seconds(), wSLO.Seconds(), cMin, w, cSafe, headroom*100.0, wSafe)

	// Apply the M/M/c-derived sizing to the Pulse pool so the Erlang-C model
	// drives the initial worker count. Runtime adjustments add measured
	// interarrival and execution-time variability.
	if c.pulsePool != nil {
		c.pulsePool.SetTargetWorkers(cSafe)
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

// sizingTau returns the configured (or env-overridden) per-job service time
// used until execution samples are available, defaulting to 4ms.
func (c *Controller) sizingTau() time.Duration {
	tau := c.config.SizingServiceTime
	if v := os.Getenv("CPRA_SIZING_TAU_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			tau = time.Duration(ms) * time.Millisecond
		}
	}
	if tau <= 0 {
		tau = defaultServiceTime
	}
	return tau
}

// feedArrivalRate computes the pulse arrival rate (lambda) from the world and
// feeds it (plus the service time tau) to the pulse pool's autoscaler, so the
// pool can size itself from demand even when the queue is empty. The dispatch
// systems throttle to match the worker rate, so queue depth alone cannot reveal
// latent demand; lambda can.
func (c *Controller) feedArrivalRate() {
	lambda := computePulseLambda(c.world)
	if lambda <= 0 || c.pulsePool == nil {
		return
	}
	tau := c.sizingTau().Seconds()
	c.pulsePool.SetArrivalRate(lambda)
	c.pulsePool.SetServiceTime(tau)
	// Preserve the latency-aware capacity selected while loading monitors.
}

// Start begins the main processing loop of the controller.
func (c *Controller) Start() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.doneCh != nil {
		return fmt.Errorf("controller already started; create a new controller to restart")
	}
	c.feedArrivalRate()
	c.stopCh, c.doneCh = make(chan struct{}), make(chan struct{})
	c.running = true
	c.pulsePool.Start()
	c.interventionPool.Start()
	c.codePool.Start()
	go c.run()
	return nil
}

// run is the sole owner of world initialization, updates and finalization.
func (c *Controller) run() {
	defer close(c.doneCh)
	c.app.Initialize()
	ticker := time.NewTicker(time.Second / time.Duration(c.app.TPS))
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			// Stop admitting new operations. Continue applying accepted results while
			// all pools drain, so backpressure cannot deadlock shutdown.
			drained := make(chan struct{})
			go func() {
				var wg sync.WaitGroup
				for _, pool := range []*queue.DynamicWorkerPool{c.pulsePool, c.interventionPool, c.codePool} {
					wg.Add(1)
					go func(p *queue.DynamicWorkerPool) { defer wg.Done(); p.DrainAndStop() }(pool)
				}
				wg.Wait()
				close(drained)
			}()
			for {
				for _, s := range c.resultSystems {
					s.Update(c.world)
				}
				select {
				case <-drained:
					for _, s := range c.resultSystems {
						s.Update(c.world)
					}
					c.app.Finalize()
					c.PrintShutdownMetrics()
					c.pulseQueue.Close()
					c.interventionQueue.Close()
					c.codeQueue.Close()
					return
				case <-ticker.C:
				}
			}
		case <-ticker.C:
			c.app.Update()
		}
	}
}

func (c *Controller) Stop() {
	c.lifecycleMu.Lock()
	if c.doneCh == nil {
		c.doneCh = make(chan struct{})
		done := c.doneCh
		c.lifecycleMu.Unlock()
		c.pulsePool.DrainAndStop()
		c.interventionPool.DrainAndStop()
		c.codePool.DrainAndStop()
		c.pulseQueue.Close()
		c.interventionQueue.Close()
		c.codeQueue.Close()
		close(done)
		return
	}
	if c.running {
		c.running = false
		close(c.stopCh)
	}
	done := c.doneCh
	c.lifecycleMu.Unlock()
	<-done
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
		SystemLogger.Info("%s Queue: depth=%d/%d enqueued=%d dequeued=%d dropped=%d", label, stats.QueueDepth, stats.Capacity, stats.Enqueued, stats.Dequeued, stats.Dropped)
		SystemLogger.Info("%s Queue timings: avg_wait=%s max_wait=%s window=%s", label, formatDur(stats.AvgQueueTime), formatDur(stats.MaxQueueTime), formatDur(stats.SampleWindow))
		SystemLogger.Info("%s Queue rates: arrival=%.2f/s service=%.2f/s last_enqueue=%s last_dequeue=%s", label, stats.EnqueueRate, stats.DequeueRate, formatTS(stats.LastEnqueue), formatTS(stats.LastDequeue))
	}
	logWorkers := func(label string, stats queue.WorkerPoolStats) {
		SystemLogger.Info("%s Workers: running=%d capacity=%d target=%d min=%d max=%d waiting=%d", label, stats.RunningWorkers, stats.CurrentCapacity, stats.TargetWorkers, stats.MinWorkers, stats.MaxWorkers, stats.WaitingTasks)
		SystemLogger.Info("%s Tasks: submitted=%d completed=%d pending_results=%d scaling_events=%d last_scale=%s", label, stats.TasksSubmitted, stats.TasksCompleted, stats.PendingResults, stats.ScalingEvents, formatTS(stats.LastScaleTime))
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

	worldStats := c.world.Stats()
	SystemLogger.Info("World: entities_used=%d recycled=%d total=%d archetypes=%d components=%d filters=%d locked=%t",
		worldStats.Entities.Used, worldStats.Entities.Recycled, worldStats.Entities.Total,
		len(worldStats.Archetypes), len(worldStats.ComponentTypes), worldStats.CachedFilters, worldStats.Locked)
	SystemLogger.Info("World memory: reserved=%dB used=%dB", worldStats.Memory, worldStats.MemoryUsed)
	SystemLogger.Info("=========================")
}

// GetWorld returns the ECS world for external access (e.g., testing, debugging).
func (c *Controller) GetWorld() *ecs.World {
	return c.world
}

// CheckEntityCountAndSwitchQueue selects the queue implementation before
// Start. Runtime world mutation and queue migration are intentionally rejected.
func (c *Controller) CheckEntityCountAndSwitchQueue() {
	if c.entityCountThreshold <= 0 || c.doneCh != nil {
		return
	}
	c.queueSwitchMutex.Lock()
	defer c.queueSwitchMutex.Unlock()
	if c.useAdaptiveQueue || int64(c.world.Stats().Entities.Used) <= c.entityCountThreshold {
		return
	}
	for _, entry := range []struct {
		name string
		q    queue.Queue
	}{
		{"pulse", c.pulseQueue}, {"intervention", c.interventionQueue}, {"code", c.codeQueue},
	} {
		cfg := queue.DefaultQueueConfig()
		cfg.Type = queue.QueueTypeAdaptive
		cfg.Name = entry.name
		q, err := queue.NewQueue(cfg)
		if err != nil {
			panic(err)
		}
		if err := entry.q.(*queue.Handle).ReplaceEmpty(q); err != nil {
			q.Close()
			panic(err)
		}
	}
	c.useAdaptiveQueue = true
}

// --- Accessors for the web server (read-only consumers) ---

// Metrics returns the aggregator collecting per-system performance samples.
func (c *Controller) Metrics() *MetricsAggregator { return c.metricsAgg }

// SnapshotHolder returns the holder publishing fleet snapshots for the dashboard.
func (c *Controller) SnapshotHolder() *snapshot.Holder { return c.snapshotHolder }

// PulseQueue returns the pulse job queue.
func (c *Controller) PulseQueue() queue.Queue { return c.pulseQueue }

// InterventionQueue returns the intervention job queue.
func (c *Controller) InterventionQueue() queue.Queue { return c.interventionQueue }

// CodeQueue returns the code/alert job queue.
func (c *Controller) CodeQueue() queue.Queue { return c.codeQueue }

// PulsePool returns the pulse worker pool.
func (c *Controller) PulsePool() *queue.DynamicWorkerPool { return c.pulsePool }

// InterventionPool returns the intervention worker pool.
func (c *Controller) InterventionPool() *queue.DynamicWorkerPool { return c.interventionPool }

// CodePool returns the code worker pool.
func (c *Controller) CodePool() *queue.DynamicWorkerPool { return c.codePool }

// UseAdaptiveQueue reports whether the controller has switched to Adaptive queues.
func (c *Controller) UseAdaptiveQueue() bool {
	c.queueSwitchMutex.RLock()
	defer c.queueSwitchMutex.RUnlock()
	return c.useAdaptiveQueue
}
