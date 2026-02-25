package queue

import (
	"context"
	"cpra/internal/config"
	"cpra/internal/runtime/jobs"
	"cpra/internal/util"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
)

// DynamicWorkerPool manages a pool of workers that execute jobs from a queue.
// It can dynamically adjust the number of workers based on load.
type DynamicWorkerPool struct {
	resultBatchPool  sync.Pool
	ctx              context.Context
	queue            Queue
	cancel           context.CancelFunc
	antsPool         *ants.PoolWithFunc
	logger           *log.Logger
	resultChan       chan jobs.Result
	router           *ResultRouter
	config           WorkerPoolConfig
	envConfig        *config.EnvConfig // Centralized environment config
	wg               sync.WaitGroup
	stopCh           chan struct{}
	stopOnce         sync.Once
	resultsClosed    atomic.Bool
	resultsCloseOnce sync.Once
	tasksCompleted   atomic.Int64
	scalingEvents    atomic.Int64
	lastTarget       atomic.Int64
	lastScaleTime    atomic.Int64
	tasksSubmitted   atomic.Int64
	stopping         atomic.Int32
	droppedResults   atomic.Int64 // Count of results dropped during shutdown (logged summary)

	// M/M/c scaling infrastructure
	metrics           *ScalingMetrics // Multi-window metrics collector
	startTime         time.Time       // Pool start time for warmup period
	lastScaleUpTime   time.Time       // For asymmetric cooldowns
	lastScaleDownTime time.Time       // For asymmetric cooldowns

	// Drift logger infrastructure
	driftLogStopCh chan struct{}
}

// NewDynamicWorkerPoolWithEnvConfig creates a new worker pool with explicit environment config.
func NewDynamicWorkerPoolWithEnvConfig(ctx context.Context, q Queue, config WorkerPoolConfig, logger *log.Logger, envCfg *config.EnvConfig) (*DynamicWorkerPool, error) {
	var capInfo capComputation
	if config.MaxWorkers <= 0 {
		maxCap, info := ComputeDynamicMaxWorkersFromConfig(config.MinWorkers, envCfg)
		config.MaxWorkers = maxCap
		capInfo = info
	} else {
		_, info := ComputeDynamicMaxWorkersFromConfig(config.MinWorkers, envCfg)
		info.FinalCap = config.MaxWorkers
		capInfo = info
	}

	if config.MinWorkers <= 0 {
		config.MinWorkers = 1
	}
	if config.MaxWorkers < config.MinWorkers {
		config.MaxWorkers = config.MinWorkers
	}
	capInfo.FinalCap = config.MaxWorkers

	if config.ResultBatchSize <= 0 {
		config.ResultBatchSize = 256
	}
	// Use dynamic buffer sizing if not explicitly set
	if config.ResultChannelDepth <= 0 {
		config.ResultChannelDepth = optimalResultChannelDepth(config.MaxWorkers, config.MinWorkers, config.ResultBatchSize)
	}
	if config.TargetQueueLatency <= 0 {
		config.TargetQueueLatency = 100 * time.Millisecond
	}
	// M/M/c scaling defaults
	if config.ScaleUpCooldown <= 0 {
		config.ScaleUpCooldown = 30 * time.Second
	}
	if config.ScaleDownCooldown <= 0 {
		config.ScaleDownCooldown = 120 * time.Second
	}
	if config.ScaleUpThreshold <= 1.0 {
		config.ScaleUpThreshold = 1.10 // 10% above current
	}
	if config.ScaleDownThreshold <= 0 || config.ScaleDownThreshold >= 1.0 {
		config.ScaleDownThreshold = 0.80 // 20% below current
	}
	if config.WarmupDuration <= 0 {
		config.WarmupDuration = 60 * time.Second
	}

	if logger != nil {
		logger.Printf("[WorkerPool] caps: min=%d max=%d cpu_cap=%d mem_cap=%d per_core=%d headroom=%.2f budget_bytes=%d abs_cap=%d",
			config.MinWorkers, config.MaxWorkers, capInfo.CPUCap, capInfo.MemCap, capInfo.PerCoreMultiplier, capInfo.HeadroomPct, capInfo.GoroutineBudget, capInfo.AbsoluteCap)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	stopCh := make(chan struct{})

	now := time.Now()
	pool := &DynamicWorkerPool{
		queue:             q,
		logger:            logger,
		config:            config,
		envConfig:         envCfg,
		resultChan:        make(chan jobs.Result, config.ResultChannelDepth),
		router:            NewResultRouter(config, logger, stopCh),
		ctx:               ctx,
		cancel:            cancel,
		stopCh:            stopCh,
		metrics:           NewScalingMetrics(DefaultScalingMetricsConfig()),
		startTime:         now,
		lastScaleUpTime:   now,
		lastScaleDownTime: now,
	}
	pool.resultBatchPool.New = func() any {
		s := make([]jobs.Result, 0, pool.config.ResultBatchSize)
		return &s
	}

	workerFunc := func(job interface{}) {
		// Track service time for Allen-Cunneen variance calculation
		serviceStart := time.Now()
		defer func() {
			pool.metrics.RecordServiceTime(time.Since(serviceStart))
		}()

		j, ok := job.(jobs.Job)
		if !ok {
			if pool.logger != nil {
				pool.logger.Printf("Error: Invalid job type in worker pool: %T", job)
			}
			return
		}
		result := j.Execute(pool.ctx)
		pool.deliverResult(result)
	}

	antsOptions := []ants.Option{
		ants.WithPanicHandler(func(err interface{}) {
			if pool.logger != nil {
				pool.logger.Printf("Worker panic: %v", err)
			}
		}),
	}
	antsOptions = util.AppendIf(antsOptions, pool.logger != nil, ants.WithLogger(pool.logger))
	antsOptions = util.AppendIf(antsOptions, config.PreAlloc, ants.WithPreAlloc(true))
	antsOptions = util.AppendIf(antsOptions, config.NonBlocking, ants.WithNonblocking(true))
	antsOptions = util.AppendIf(antsOptions, config.MaxBlockingTasks > 0, ants.WithMaxBlockingTasks(config.MaxBlockingTasks))
	antsOptions = util.AppendIf(antsOptions, config.ExpiryDuration > 0, ants.WithExpiryDuration(config.ExpiryDuration))

	antsPool, err := ants.NewPoolWithFunc(config.MaxWorkers, workerFunc, antsOptions...)
	if err != nil {
		return nil, err
	}
	pool.antsPool = antsPool

	// Pre-scale to InitialWorkers for burst-ready startup (scales down after warmup if idle)
	initialCap := config.InitialWorkers
	if initialCap <= 0 {
		initialCap = config.MinWorkers
	}
	if initialCap > config.MaxWorkers {
		initialCap = config.MaxWorkers
	}
	pool.antsPool.Tune(initialCap)
	pool.lastTarget.Store(int64(initialCap))
	pool.lastScaleTime.Store(time.Now().UnixNano())

	if logger != nil {
		logger.Printf("[WorkerPool] pre-scaled to %d workers (min=%d, max=%d) for burst-ready startup",
			initialCap, config.MinWorkers, config.MaxWorkers)
	}

	return pool, nil
}

// SetContext overrides the worker pool context. Call before Start().
func (p *DynamicWorkerPool) SetContext(ctx context.Context) {
	if ctx == nil {
		return
	}
	// Cancel previous context to avoid leaks; safe before Start.
	p.cancel()
	p.ctx, p.cancel = context.WithCancel(ctx)
}

// Start begins the worker pool's operations.
func (p *DynamicWorkerPool) Start() {
	routineCount := 2
	if p.config.AdjustmentInterval > 0 {
		routineCount++
	}
	// Check if drift logging is enabled via centralized config
	driftLogEnabled := p.envConfig != nil && p.envConfig.QueueDriftLog
	if driftLogEnabled {
		routineCount++
	}
	p.wg.Add(routineCount)
	go p.dispatcher()
	go p.resultProcessor()
	if p.config.AdjustmentInterval > 0 {
		go p.autoScale()
	}
	if driftLogEnabled {
		p.driftLogStopCh = make(chan struct{})
		go p.driftLogger()
	}
	if p.logger != nil {
		p.logger.Println("DynamicWorkerPool started")
	}
}

// GetRouter returns the result router for accessing type-specific result channels.
func (p *DynamicWorkerPool) GetRouter() *ResultRouter {
	return p.router
}

// DrainAndStop waits for outstanding tasks to finish before stopping the worker pool.
func (p *DynamicWorkerPool) DrainAndStop() {
	startTime := time.Now()

	// Phase 1: Wait for queue to drain (dispatcher to invoke all jobs)
	// This must happen BEFORE setting stopping flag, otherwise dispatcher exits early
	drainDeadline := time.Now().Add(p.config.TargetQueueLatency * 10)
	if drainDeadline.Sub(startTime) < 2*time.Second {
		drainDeadline = startTime.Add(2 * time.Second)
	}
	for time.Now().Before(drainDeadline) {
		queueDepth := p.queue.Stats().QueueDepth
		if queueDepth == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Phase 2: Now set stopping flag (dispatcher will exit after current batch)
	if !p.stopping.CompareAndSwap(0, 1) {
		return
	}

	pending := p.tasksSubmitted.Load() - p.tasksCompleted.Load()

	if p.logger != nil {
		p.logger.Printf("Draining DynamicWorkerPool (pending=%d, running=%d)...",
			pending, p.antsPool.Running())
	}

	// Signal all goroutines to stop
	p.stopOnce.Do(func() { close(p.stopCh) })
	if p.driftLogStopCh != nil {
		close(p.driftLogStopCh)
	}
	p.cancel()

	// Wait for pending results with timeout
	drainWindow := p.config.TargetQueueLatency * 5
	if drainWindow < 2*time.Second {
		drainWindow = 2 * time.Second
	}
	p.waitForResultsUntil(time.Now().Add(drainWindow))

	// Wait for goroutines to exit
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	waitTimeout := p.config.TargetQueueLatency * 2
	if waitTimeout < 2*time.Second {
		waitTimeout = 2 * time.Second
	}

	select {
	case <-done:
		if p.logger != nil {
			finalPending := p.tasksSubmitted.Load() - p.tasksCompleted.Load()
			p.logger.Printf("Goroutines exited cleanly (remaining_pending=%d)", finalPending)
		}
	case <-time.After(waitTimeout):
		if p.logger != nil {
			finalPending := p.tasksSubmitted.Load() - p.tasksCompleted.Load()
			p.logger.Printf("Draining timed out after %v; forcing shutdown (dropping %d pending results)",
				waitTimeout, finalPending)
		}
	}

	// Close result router channels
	p.router.Close()

	// Release ants pool resources
	p.antsPool.Release()

	if p.logger != nil {
		completed := p.tasksCompleted.Load()
		dropped := p.droppedResults.Load()
		if dropped > 0 {
			p.logger.Printf("DynamicWorkerPool stopped in %v (completed=%d, dropped=%d)",
				time.Since(startTime), completed, dropped)
		} else {
			p.logger.Printf("DynamicWorkerPool stopped in %v (completed=%d)",
				time.Since(startTime), completed)
		}
	}
}

func (p *DynamicWorkerPool) waitForResultsUntil(deadline time.Time) {
	for time.Now().Before(deadline) {
		pending := p.tasksSubmitted.Load() - p.tasksCompleted.Load()
		if pending <= 0 && len(p.resultChan) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	p.resultsCloseOnce.Do(func() {
		p.resultsClosed.Store(true)
		if pending := p.tasksSubmitted.Load() - p.tasksCompleted.Load(); pending > 0 && p.logger != nil {
			p.logger.Printf("Dropping %d results that were still pending at shutdown deadline", pending)
		}
	})
}

// dispatcher fetches batches of jobs from the queue and submits them to the ants pool.
func (p *DynamicWorkerPool) dispatcher() {
	defer p.wg.Done()
	for {
		// 1. Determine batch size
		batchTarget := p.antsPool.Cap()
		if batchTarget <= 0 {
			batchTarget = p.config.MinWorkers
		}
		if batchTarget > p.config.ResultBatchSize {
			batchTarget = p.config.ResultBatchSize
		}
		if batchTarget <= 0 {
			batchTarget = 1
		}

		// 2. Try to dequeue
		batch, err := p.queue.DequeueBatch(batchTarget)
		if err != nil {
			if errors.Is(err, ErrQueueClosed) {
				return
			}
			if p.logger != nil {
				p.logger.Printf("Error dequeuing job batch: %v", err)
			}
			// On error, wait a bit to avoid tight loop
			select {
			case <-p.ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		// 3. If batch found, process it
		if len(batch) > 0 {
			// Check if we're stopping before processing - avoid invoking on closed pool
			if p.stopping.Load() != 0 {
				return
			}

			// Record arrival times for Allen-Cunneen variance calculation
			now := time.Now()
			var submitted int64
			for _, job := range batch {
				// Re-check stopping flag during batch processing to exit quickly
				if p.stopping.Load() != 0 {
					break
				}
				p.metrics.RecordArrival(now)
				if err := p.antsPool.Invoke(job); err != nil {
					// Don't log "pool closed" errors during shutdown - they're expected
					if p.stopping.Load() == 0 {
						p.logger.Printf("Error invoking job: %v", err)
					}
					continue
				}
				submitted++
			}
			if submitted > 0 {
				p.tasksSubmitted.Add(submitted)
			}
			// Immediately try to get next batch without waiting
			continue
		}

		// 4. If empty, wait for signal
		select {
		case <-p.ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-p.queue.Notify():
			// Signal received, loop back to dequeue
		}
	}
}

// resultProcessor collects individual results and routes them through the router in batches.
// Uses time.AfterFunc for timeout-based flushing instead of a ticker to avoid CPU burn when idle.
func (p *DynamicWorkerPool) resultProcessor() {
	defer p.wg.Done()

	batch := p.getResultBatch()
	var flushTimer *time.Timer
	flushCh := make(chan struct{}, 1) // Buffered to avoid blocking timer

	// Helper to start/reset the flush timer
	startTimer := func() {
		if flushTimer != nil {
			flushTimer.Stop()
		}
		flushTimer = time.AfterFunc(p.config.ResultBatchTimeout, func() {
			select {
			case flushCh <- struct{}{}:
			default: // Already pending
			}
		})
	}

	// Helper to stop timer
	stopTimer := func() {
		if flushTimer != nil {
			flushTimer.Stop()
			flushTimer = nil
		}
	}
	defer stopTimer()

	draining := false
	for {
		select {
		case <-p.stopCh:
			draining = true
		case <-p.ctx.Done():
			draining = true
		case result, ok := <-p.resultChan:
			if !ok { // resultChan was closed
				stopTimer()
				if len(*batch) > 0 {
					p.router.RouteResults(*batch)
					p.putResultBatch(batch)
				}
				return
			}
			p.tasksCompleted.Add(1)
			*batch = append(*batch, result)

			// Start timer on first item (efficient: no timer when empty)
			if len(*batch) == 1 {
				startTimer()
			}

			// Flush immediately when batch is full
			if len(*batch) >= p.config.ResultBatchSize {
				stopTimer()
				p.router.RouteResults(*batch)
				p.putResultBatch(batch)
				batch = p.getResultBatch()
			}
		case <-flushCh:
			// Timer fired - flush partial batch
			if len(*batch) > 0 {
				p.router.RouteResults(*batch)
				p.putResultBatch(batch)
				batch = p.getResultBatch()
			}
		}

		if draining {
			stopTimer()
			if len(*batch) > 0 && len(p.resultChan) == 0 {
				p.router.RouteResults(*batch)
				p.putResultBatch(batch)
				return
			}
			if len(*batch) == 0 && len(p.resultChan) == 0 {
				return
			}
		}
	}
}

// deliverResult attempts to send a result without blocking shutdown.
// Results are dropped when the pool is stopping to avoid deadlocks.
// Logging is rate-limited to avoid spam during shutdown with many in-flight jobs.
func (p *DynamicWorkerPool) deliverResult(result jobs.Result) {
	backoff := p.config.ResultBatchTimeout
	if backoff <= 0 {
		backoff = 10 * time.Millisecond
	}

	attempts := 0
	timer := time.NewTimer(backoff)
	defer timer.Stop()

	for {
		if p.resultsClosed.Load() {
			p.tasksCompleted.Add(1)
			return
		}

		select {
		case p.resultChan <- result:
			return
		default:
		}

		// Wait with reusable timer to avoid per-loop allocations
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(backoff)

		select {
		case p.resultChan <- result:
			return
		case <-p.stopCh:
			// Rate-limited logging: only log the first drop to avoid spam
			dropped := p.droppedResults.Add(1)
			if p.logger != nil && dropped == 1 {
				p.logger.Printf("Dropping results during shutdown (result channel stalled)")
			}
			p.tasksCompleted.Add(1)
			return
		case <-timer.C:
			if p.stopping.Load() == 0 {
				continue
			}
			attempts++
		}

		if attempts >= 5 {
			// Rate-limited logging for timeout drops
			dropped := p.droppedResults.Add(1)
			if p.logger != nil && dropped == 1 {
				p.logger.Printf("Dropping results during shutdown (send timeout)")
			}
			p.tasksCompleted.Add(1)
			return
		}
	}
}

func (p *DynamicWorkerPool) getResultBatch() *[]jobs.Result {
	if v := p.resultBatchPool.Get(); v != nil {
		ptr := v.(*[]jobs.Result)
		*ptr = (*ptr)[:0]
		return ptr
	}
	s := make([]jobs.Result, 0, p.config.ResultBatchSize)
	return &s
}

func (p *DynamicWorkerPool) putResultBatch(batch *[]jobs.Result) {
	if batch == nil {
		return
	}
	p.resultBatchPool.Put(batch)
}

// Stats returns runtime statistics for the worker pool.
func (p *DynamicWorkerPool) Stats() WorkerPoolStats {
	return WorkerPoolStats{
		MinWorkers:      p.config.MinWorkers,
		MaxWorkers:      p.config.MaxWorkers,
		CurrentCapacity: p.antsPool.Cap(),
		RunningWorkers:  p.antsPool.Running(),
		WaitingTasks:    p.antsPool.Waiting(),
		TargetWorkers:   int(p.lastTarget.Load()),
		TasksSubmitted:  p.tasksSubmitted.Load(),
		TasksCompleted:  p.tasksCompleted.Load(),
		ScalingEvents:   p.scalingEvents.Load(),
		LastScaleTime:   time.Unix(0, p.lastScaleTime.Load()),
		PendingResults:  len(p.resultChan),
	}
}

// Pause temporarily stops the worker pool from processing new tasks.
func (p *DynamicWorkerPool) Pause() {
	if p.logger != nil {
		p.logger.Println("Pause() is a no-op (queue replacement disabled)")
	}
}

// Resume resumes worker pool processing after a pause.
func (p *DynamicWorkerPool) Resume() {
	if p.logger != nil {
		p.logger.Println("Resume() is a no-op (queue replacement disabled)")
	}
}

// driftLogger periodically logs queue latency drift metrics.
// Logs every 10s during warmup, then every 60s after.
func (p *DynamicWorkerPool) driftLogger() {
	defer p.wg.Done()
	warmupEnd := p.startTime.Add(p.config.WarmupDuration)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	slowInterval := 60 * time.Second
	inWarmup := true

	for {
		select {
		case <-p.driftLogStopCh:
			return
		case <-p.ctx.Done():
			return
		case now := <-ticker.C:
			// Switch to slower interval after warmup
			if inWarmup && now.After(warmupEnd) {
				inWarmup = false
				ticker.Reset(slowInterval)
			}
			p.logDriftMetrics()
		}
	}
}

func (p *DynamicWorkerPool) logDriftMetrics() {
	if p.logger == nil {
		return
	}
	stats := p.queue.Stats()
	p.logger.Printf("[DriftLog] depth=%d ema_wait=%s rolling_p50=%s rolling_p95=%s bucket_avg=%s bucket_max=%s enq_rate=%.2f/s deq_rate=%.2f/s workers=%d",
		stats.QueueDepth,
		stats.EMAWait,
		stats.RollingP50Wait,
		stats.RollingP95Wait,
		stats.BucketAvg1m,
		stats.BucketMax1m,
		stats.EnqueueRate,
		stats.DequeueRate,
		p.antsPool.Running(),
	)
}
