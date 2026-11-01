package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/controller/systems"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/loader"

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
	durableSystem     *systems.DurableSystem
	pulseQueue        queue.Queue
	codeQueue         queue.Queue
	interventionQueue queue.Queue
	pulsePool         *queue.DynamicWorkerPool
	mapper            *entities.EntityManager
	world             *ecs.World
	app               *app.App
	interventionPool  *queue.DynamicWorkerPool
	codePool          *queue.DynamicWorkerPool
	config            Config
	running           bool
	lifecycleMu       sync.Mutex
	stopCh            chan struct{}
	wakeups           chan struct{}
	doneCh            chan struct{}
	resultSystems     []app.System
	initialized       chan struct{}
	initializedState  atomic.Bool
	lastProgress      atomic.Int64
	stoppingState     atomic.Bool

	// Observability surfaces for the web server.
	metricsAgg     *MetricsAggregator
	snapshotHolder *fleetview.Holder
}

// Config holds all configuration for the controller.
type Config struct {
	NotificationLog *jobs.NotificationLog
	Catalog         *management.Catalog
	Encryption      *secureconfig.Sealer
	Store           *persistence.Store
	Runtime         runtimeconfig.Config
	Debug           bool
	StreamingConfig loader.StreamingConfig
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

	// TPS is the ark-tools tick rate (ticks/sec). Zero falls back to 10.
	TPS int
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		Runtime:           runtimeconfig.Default(),
		StreamingConfig:   loader.DefaultStreamingConfig(),
		QueueCapacity:     65536, // Must be a power of 2
		WorkerConfig:      queue.DefaultWorkerPoolConfig(),
		BatchSize:         1000,
		SizingServiceTime: 0,
		SizingSLO:         0,
		SizingHeadroomPct: 0,
		AlertCooldown:     5 * time.Minute,
		RecoveryBypass:    true,
		TPS:               200,
	}
}

// NewController creates a new controller with its queues, workers and ECS systems.
// Store must be initialized even in memory mode. A nil Store is a caller error
// and panics before queues or workers are allocated.
func NewController(config Config) *Controller {
	if config.Store == nil {
		panic("controller requires an initialized store")
	}
	// Create ark-tools app with initial capacity
	arkApp := app.New(1024)
	tps := config.TPS
	if tps <= 0 {
		tps = 10
	}
	arkApp.TPS = float64(tps) // High-frequency updates for 1s monitor intervals
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	// All local queues have bounded fair admission. Action destinations execute
	// serially; durable admission/start guards remain the authority for effects.
	pulseCfg := queue.DefaultQueueConfig()
	pulseCfg.Name = "pulse"
	pulseCfg.HybridConfig.Name = "pulse"
	pulseCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
	var pulseQueue queue.Queue
	pulseQueue, err := queue.NewFairQueue(pulseCfg.HybridConfig.RingCapacity + pulseCfg.HybridConfig.OverflowCapacity)
	if err != nil {
		SystemLogger.Fatal("Failed to create pulse HybridQueue: %v", err)
	}
	interventionCfg := queue.DefaultQueueConfig()
	interventionCfg.Name = "intervention"
	interventionCfg.HybridConfig.Name = "intervention"
	interventionCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
	var interventionQueue queue.Queue
	interventionQueue, err = queue.NewActionQueue(interventionCfg.HybridConfig.RingCapacity+interventionCfg.HybridConfig.OverflowCapacity, "recovery")
	if err != nil {
		SystemLogger.Fatal("Failed to create intervention HybridQueue: %v", err)
	}
	codeCfg := queue.DefaultQueueConfig()
	codeCfg.Name = "code"
	codeCfg.HybridConfig.Name = "code"
	codeCfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
	var codeQueue queue.Queue
	codeQueue, err = queue.NewActionQueue(codeCfg.HybridConfig.RingCapacity+codeCfg.HybridConfig.OverflowCapacity, "notification")
	if err != nil {
		SystemLogger.Fatal("Failed to create code HybridQueue: %v", err)
	}

	if err := codeQueue.(*queue.FairQueue).SetNotificationConcurrency(config.Runtime.Notifications.Concurrency); err != nil {
		SystemLogger.Fatal("Invalid notification dispatch configuration: %v", err)
	}
	pulseLogger := WorkerPoolLogger.WithContext("pulse")
	pulseWorkerConfig := config.WorkerConfig
	pulseWorkerConfig.FairDispatch = true
	pulsePool, err := queue.NewDynamicWorkerPool(queue.NewHandle(pulseQueue), pulseWorkerConfig, pulseLogger)
	if err != nil {
		SystemLogger.Fatal("Failed to create pulse worker pool: %v", err)
	}
	interventionLogger := WorkerPoolLogger.WithContext("intervention")
	actionWorkerConfig := config.WorkerConfig
	actionWorkerConfig.FairDispatch = true
	interventionPool, err := queue.NewDynamicWorkerPool(queue.NewHandle(interventionQueue), actionWorkerConfig, interventionLogger)
	if err != nil {
		SystemLogger.Fatal("Failed to create intervention worker pool: %v", err)
	}
	codeLogger := WorkerPoolLogger.WithContext("code")
	codePool, err := queue.NewDynamicWorkerPool(queue.NewHandle(codeQueue), actionWorkerConfig, codeLogger)
	if err != nil {
		SystemLogger.Fatal("Failed to create code worker pool: %v", err)
	}

	if config.Store != nil {
		pulsePool.SetSLOFeedback(config.Store.SLO(), config.Runtime.SLO)
	}

	pulseQueue = pulsePool.Queue()
	interventionQueue = interventionPool.Queue()
	codeQueue = codePool.Queue()

	metricsAgg := NewMetricsAggregator()
	snapshotHolder := fleetview.NewHolder()
	if config.Catalog != nil {
		config.Catalog.SetObservations(snapshotHolder)
	}
	logger := &LoggerAdapter{logger: SystemLogger.withCallerSkip(1), metrics: metricsAgg}

	alertCooldown := config.AlertCooldown
	if alertCooldown <= 0 {
		alertCooldown = 5 * time.Minute
	}
	pulseRouter := pulsePool.GetRouter()
	interventionRouter := interventionPool.GetRouter()
	codeRouter := codePool.GetRouter()
	wakeups := make(chan struct{}, 1)
	durableSystem := systems.NewDurableSystem(world, config.Store, config.Runtime, snapshotHolder, logger, pulseQueue, interventionQueue, codeQueue, []<-chan []jobs.Result{pulseRouter.PulseResultChan, interventionRouter.InterventionResultChan, codeRouter.CodeResultChan}, alertCooldown, config.RecoveryBypass)
	durableSystem.SetNotificationLog(config.NotificationLog)
	durableSystem.SetWakeup(wakeups)
	for _, router := range []*queue.ResultRouter{pulseRouter, interventionRouter, codeRouter} {
		router.SetWakeup(wakeups)
	}
	arkApp.AddSystem(durableSystem)

	controller := &Controller{
		durableSystem:     durableSystem,
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
		metricsAgg:        metricsAgg,
		snapshotHolder:    snapshotHolder,
		resultSystems:     []app.System{durableSystem},
		wakeups:           wakeups,
	}
	snapshotHolder.SetProgress(controller.processReady)
	return controller
}

// LoadMonitors loads monitors using the streaming loader.
func (c *Controller) LoadMonitors(ctx context.Context, filename string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.doneCh != nil {
		return fmt.Errorf("monitors must be loaded before starting the controller")
	}
	if c.config.Catalog != nil {
		return errors.New("managed configuration must load from its durable catalog")
	}
	if c.durableSystem != nil {
		if err := c.durableSystem.LoadAgentConnections(ctx, c.config.Encryption); err != nil {
			return fmt.Errorf("recover agent connections: %w", err)
		}
	}
	loader := loader.NewStreamingLoader(filename, c.world, c.config.StreamingConfig)
	stats, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	SystemLogger.Info("Successfully loaded %d monitors in %v (%.0f monitors/sec)",
		stats.TotalEntities, stats.LoadingTime, stats.CreationRate)

	if c.durableSystem != nil {
		if err := c.durableSystem.Load(ctx); err != nil {
			return fmt.Errorf("reconcile durable monitors: %w", err)
		}
	}

	// Check if we need to switch to AdaptiveQueue due to high entity count

	// Pre-calculate worker sizing from initial configuration/world (Pulse only)
	c.precomputeSizingFromConfig()
	return nil
}

// LoadCatalog initializes the authoritative managed configuration before the
// owner loop starts. Subsequent commits are reconciled incrementally by that
// owner; HTTP handlers and background preparation never share the Ark world.
func (c *Controller) LoadCatalog(ctx context.Context) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.doneCh != nil || c.durableSystem == nil || c.config.Catalog == nil {
		return errors.New("catalog must be loaded into a durable controller before start")
	}
	if err := c.durableSystem.LoadCatalog(ctx, c.config.Catalog, c.mapper); err != nil {
		return err
	}
	c.precomputeSizingFromConfig()
	return nil
}

// precomputeSizingFromConfig computes a recommended worker count from initial world contents
// and configured (or env) service time and latency SLO. It currently targets the Pulse pool only.
func (c *Controller) precomputeSizingFromConfig() {
	demand := initialPulseDemand(c.world)
	if err := c.pulsePool.SetDriverDemand(demand); err != nil {
		panic(err)
	}
	// Determine τ (service time) and W_slo from env or config; fallback to sane defaults
	tau := c.config.SizingServiceTime
	if v := os.Getenv("CPRA_SIZING_TAU_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			tau = time.Duration(ms) * time.Millisecond
		}
	}
	if tau <= 0 {
		tau = initialPulseServiceTime(c.world)
	}
	wSLO := c.config.SizingSLO
	if v := os.Getenv("CPRA_SIZING_SLO_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			wSLO = time.Duration(ms) * time.Millisecond
		}
	}
	if wSLO <= 0 {
		wSLO = tau + 250*time.Millisecond
	}

	// Compute λ for Pulse from world: sum over active monitors of 1/Interval
	lambda := computePulseLambda(c.world)

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
	runtimeTarget := wSLO
	if c.config.SizingSLO <= 0 && os.Getenv("CPRA_SIZING_SLO_MS") == "" {
		runtimeTarget = 0
	}
	c.pulsePool.SetSizingPolicy(runtimeTarget, headroom)
	if lambda <= 0 {
		c.pulsePool.SetModelWorkers(c.config.WorkerConfig.MinWorkers)
		return
	}
	cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 1, 1, c.pulsePool.Stats().MaxWorkers)
	if err != nil {
		SystemLogger.Debug("Pulse startup model unavailable: %v", err)
		if errors.Is(err, queue.ErrNoFeasibleCapacity) {
			target := float64(c.pulsePool.Stats().MaxWorkers)
			if wSLO <= tau {
				target = math.Min(target, math.Ceil(lambda*tau.Seconds()*(1+headroom)))
			}
			c.pulsePool.SetModelWorkers(int(target))
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
	SystemLogger.Debug("Pulse startup sizing: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
		lambda, tau.Seconds(), wSLO.Seconds(), cMin, w, cSafe, headroom*100.0, wSafe)

	// Apply the M/M/c-derived sizing to the Pulse pool so the Erlang-C model
	// drives the initial worker count. Runtime adjustments add measured
	// interarrival and execution-time variability.
	if c.pulsePool != nil {
		c.pulsePool.SetModelWorkers(cSafe)
	}
}

// computePulseLambda estimates arrival rate (jobs/sec) from Pulse intervals of enabled monitors.
func computePulseLambda(world *ecs.World) float64 {
	storage := ecs.NewMap1[components.JobStorage](world)
	f := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
		Without(ecs.C[components.Disabled]())
	q := f.Query()
	sum := 0.0
	for q.Next() {
		_, cfg := q.Get()
		if !storage.Get(q.Entity()).LocalCheck() {
			continue
		}
		if cfg == nil || cfg.Interval <= 0 {
			continue
		}
		sum += 1.0 / cfg.Interval.Seconds()
	}
	return sum
}

// initialPulseServiceTime budgets cold checks against their configured timeout.
// Before the first observation, a four-millisecond assumption cannot distinguish
// a fast target from one that never replies. No monitor overlaps its own checks,
// so each contributes at most one occupied worker. Explicit sizing overrides
// remain available; measured service times take over after startup.
func initialPulseServiceTime(world *ecs.World) time.Duration {
	storage := ecs.NewMap1[components.JobStorage](world)
	query := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
		Without(ecs.C[components.Disabled]()).Query()
	var arrivals, occupied float64
	for query.Next() {
		_, check := query.Get()
		if !storage.Get(query.Entity()).LocalCheck() {
			continue
		}
		if check.Interval <= 0 {
			continue
		}
		rate := 1 / check.Interval.Seconds()
		arrivals += rate
		occupied += min(1, max(defaultServiceTime, check.Timeout).Seconds()*rate)
	}
	if arrivals == 0 {
		return defaultServiceTime
	}
	return max(defaultServiceTime, time.Duration(occupied/arrivals*float64(time.Second)))
}

func initialPulseDemand(world *ecs.World) []queue.DriverDemand {
	storage := ecs.NewMap1[components.JobStorage](world)
	query := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).Without(ecs.C[components.Disabled]()).Query()
	byDriver := make(map[string]queue.DriverDemand)
	for query.Next() {
		_, check := query.Get()
		if !storage.Get(query.Entity()).LocalCheck() || check.Interval <= 0 {
			continue
		}
		d := byDriver[check.Type]
		d.Driver = check.Type
		d.Monitors++
		d.ArrivalRate += 1 / check.Interval.Seconds()
		d.Occupancy += min(1, max(defaultServiceTime, check.Timeout).Seconds()/check.Interval.Seconds())
		byDriver[check.Type] = d
	}
	out := make([]queue.DriverDemand, 0, len(byDriver))
	for _, d := range byDriver {
		out = append(out, d)
	}
	return out
}

// sizingTau returns the configured (or env-overridden) per-job service time
// used until execution samples are available, using timeout-weighted demand
// when no explicit estimate was supplied.
func (c *Controller) sizingTau() time.Duration {
	tau := c.config.SizingServiceTime
	if v := os.Getenv("CPRA_SIZING_TAU_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			tau = time.Duration(ms) * time.Millisecond
		}
	}
	if tau <= 0 {
		tau = initialPulseServiceTime(c.world)
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
	if c.pulsePool == nil {
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
	c.initialized = make(chan struct{})
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
	var demandVersion uint64
	updateDemand := func() {
		if c.durableSystem == nil {
			return
		}
		c.pulsePool.SetArrivalRate(c.durableSystem.PulseArrivalRate())
		version, demand := c.durableSystem.PulseDriverDemand(demandVersion)
		if version != demandVersion {
			if err := c.pulsePool.SetDriverDemand(demand); err != nil {
				panic(err)
			}
			demandVersion = version
		}
	}
	c.app.Initialize()
	updateDemand()
	c.pulsePool.SetModelWorkers(c.pulsePool.Stats().TargetWorkers)
	c.lastProgress.Store(time.Now().UnixNano())
	c.initializedState.Store(true)
	close(c.initialized)
	interval := time.Second / time.Duration(c.app.TPS)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	nextAllowed := time.Now().Add(interval)
	var committed <-chan struct{}
	if c.config.Store != nil {
		committed = c.config.Store.ControllerWakeups()
	}
	for {
		select {
		case <-c.stopCh:
			timer.Stop()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			if c.durableSystem != nil {
				c.durableSystem.StopAdmission()
			}
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
		case <-timer.C:
		case <-c.wakeups:
		case <-committed:
		}
		now := time.Now()
		if remaining := nextAllowed.Sub(now); remaining > 0 {
			// Repeated hints cannot move the fixed earliest update boundary or
			// turn sustained completions into an unbounded update loop.
			timer.Reset(remaining)
			continue
		}
		c.app.Update()
		updateDemand()
		now = time.Now()
		c.lastProgress.Store(now.UnixNano())
		nextAllowed = now.Add(interval)
		wait := interval
		if c.durableSystem != nil {
			wait = c.durableSystem.NextUpdate(now, interval)
		}
		timer.Reset(wait)
	}
}

// Ready reports process admission readiness without reading mutable ECS state.
func (c *Controller) Ready() bool {
	return c.ReadyContext(context.Background())
}

// ReadyContext also bounds the storage observation by the caller's lifetime.
func (c *Controller) ReadyContext(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil || !c.processReady() {
		return false
	}
	if c.config.Store != nil {
		status, err := c.config.Store.StatusContext(ctx)
		if err != nil || !status.Ready {
			return false
		}
	}
	return ctx.Err() == nil && c.processReady()
}

func (c *Controller) processReady() bool {
	return c.initializedState.Load() && !c.stoppingState.Load() && (c.durableSystem == nil || c.durableSystem.Progressing()) && time.Since(time.Unix(0, c.lastProgress.Load())) < 30*time.Second
}

func (c *Controller) WaitReady(ctx context.Context) error {
	c.lifecycleMu.Lock()
	initialized := c.initialized
	c.lifecycleMu.Unlock()
	if initialized == nil {
		return fmt.Errorf("controller has not started")
	}
	select {
	case <-initialized:
		if !c.ReadyContext(ctx) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("controller unavailable after initialization")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BeginStop rejects admission immediately and asks the owner loop to drain.
func (c *Controller) BeginStop() {
	c.stoppingState.Store(true)
	if c.durableSystem != nil {
		c.durableSystem.StopAdmission()
	}
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
	c.lifecycleMu.Unlock()
}

// StopContext bounds the caller's wait. On expiry it cancels cooperative jobs;
// the controller still owns cleanup until Done closes. Callers must not close
// its store or loggers early. The executable exits on an uncooperative timeout.
func (c *Controller) StopContext(ctx context.Context) error {
	c.BeginStop()
	c.lifecycleMu.Lock()
	done := c.doneCh
	c.lifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		c.CancelWork()
		return ctx.Err()
	}
}

func (c *Controller) CancelWork() {
	c.pulsePool.CancelWork()
	c.interventionPool.CancelWork()
	c.codePool.CancelWork()
}

func (c *Controller) Stop() { _ = c.StopContext(context.Background()) }

// PrintShutdownMetrics logs queue, worker pool, and world statistics at shutdown.
func (c *Controller) PrintShutdownMetrics() {
	// Helpers for friendly placeholders on unset values
	formatTS := func(t time.Time) string {
		if t.IsZero() || t.Unix() == 0 {
			return "never"
		}
		return t.UTC().Format(time.RFC3339)
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

// --- Accessors for the web server (read-only consumers) ---

// Metrics returns the aggregator collecting per-system performance samples.
func (c *Controller) Metrics() *MetricsAggregator { return c.metricsAgg }

// SnapshotHolder returns the holder publishing fleet snapshots for the dashboard.
func (c *Controller) SnapshotHolder() *fleetview.Holder { return c.snapshotHolder }

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
