package queue

import (
	"context"
	"cpra/internal/jobs"
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
)

// batchCollector uses sync.Cond for efficient batch collection with immediate
// signaling when batch is full. This reduces CPU burn compared to ticker-based polling.
type batchCollector struct {
	mu      sync.Mutex
	cond    *sync.Cond
	batch   []jobs.Result
	maxSize int
	ready   bool          // true when batch is full or timeout fired
	closed  bool          // true when collector is shut down
	timer   *time.Timer   // for timeout-based flushing
	timeout time.Duration // batch flush timeout
	onFlush func()        // called after flush signals are sent
}

// newBatchCollector creates a batch collector with sync.Cond signaling.
func newBatchCollector(maxSize int, timeout time.Duration) *batchCollector {
	c := &batchCollector{
		batch:   make([]jobs.Result, 0, maxSize),
		maxSize: maxSize,
		timeout: timeout,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Add adds a result to the batch. Signals when batch is full.
func (c *batchCollector) Add(r jobs.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	c.batch = append(c.batch, r)

	// Start timer on first item
	if len(c.batch) == 1 && c.timeout > 0 {
		c.startTimerLocked()
	}

	// Signal immediately when batch is full
	if len(c.batch) >= c.maxSize {
		c.ready = true
		c.stopTimerLocked()
		c.cond.Signal()
	}
}

// startTimerLocked starts the timeout timer (must hold lock).
func (c *batchCollector) startTimerLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(c.timeout, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.batch) > 0 && !c.closed {
			c.ready = true
			c.cond.Signal()
		}
	})
}

// stopTimerLocked stops the timeout timer (must hold lock).
func (c *batchCollector) stopTimerLocked() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// Wait blocks until a batch is ready (full or timeout). Returns the batch.
// Returns nil if closed with no pending items.
func (c *batchCollector) Wait() []jobs.Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Wait for ready signal
	for !c.ready && !c.closed {
		c.cond.Wait()
	}

	// Return nil if closed with empty batch
	if c.closed && len(c.batch) == 0 {
		return nil
	}

	// Swap out the batch
	result := c.batch
	c.batch = make([]jobs.Result, 0, c.maxSize)
	c.ready = false
	c.stopTimerLocked()

	return result
}

// Flush forces a flush of any pending items.
func (c *batchCollector) Flush() []jobs.Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stopTimerLocked()
	result := c.batch
	c.batch = make([]jobs.Result, 0, c.maxSize)
	c.ready = false
	return result
}

// Close shuts down the collector and wakes any waiting goroutine.
func (c *batchCollector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	c.stopTimerLocked()
	c.cond.Broadcast() // Wake all waiters
}

// Len returns the current batch size.
func (c *batchCollector) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batch)
}

// ResultRouter handles routing of job results to type-specific channels.
// This enables decoupling result processing from the main worker pool.
type ResultRouter struct {
	PulseResultChan        chan []jobs.Result
	InterventionResultChan chan []jobs.Result
	CodeResultChan         chan []jobs.Result
	logger                 *log.Logger
	config                 WorkerPoolConfig
	stopCh                 <-chan struct{}
	closed                 atomic.Bool
}

// WorkerPoolStats exposes runtime metrics for the dynamic worker pool.
type WorkerPoolStats struct {
	LastScaleTime   time.Time
	MinWorkers      int
	MaxWorkers      int
	CurrentCapacity int
	RunningWorkers  int
	WaitingTasks    int
	TargetWorkers   int
	TasksSubmitted  int64
	TasksCompleted  int64
	ScalingEvents   int64
	PendingResults  int
}

// NewResultRouter creates a new result router with buffered channels.
func NewResultRouter(config WorkerPoolConfig, logger *log.Logger, stopCh <-chan struct{}) *ResultRouter {
	bufferSize := config.ResultChannelDepth
	return &ResultRouter{
		PulseResultChan:        make(chan []jobs.Result, bufferSize),
		InterventionResultChan: make(chan []jobs.Result, bufferSize),
		CodeResultChan:         make(chan []jobs.Result, bufferSize),
		config:                 config,
		logger:                 logger,
		stopCh:                 stopCh,
	}
}

// RouteResults takes a batch of mixed results and routes them to appropriate channels.
func (r *ResultRouter) RouteResults(results []jobs.Result) {
	if len(results) == 0 || r.closed.Load() {
		return
	}

	// Group results by type
	pulseResults := make([]jobs.Result, 0, len(results))
	interventionResults := make([]jobs.Result, 0, len(results))
	codeResults := make([]jobs.Result, 0, len(results))

	for _, result := range results {
		switch result.Payload["type"] {
		case "pulse":
			pulseResults = append(pulseResults, result)
		case "intervention":
			interventionResults = append(interventionResults, result)
		case "code":
			codeResults = append(codeResults, result)
		default:
			if r.logger != nil {
				r.logger.Printf("Unknown job type in result: %v", result.Payload["type"])
			}
		}
	}

	// Send to appropriate channels with backpressure logging
	if len(pulseResults) > 0 {
		r.sendWithBackpressure(r.PulseResultChan, pulseResults, "pulse")
	}
	if len(interventionResults) > 0 {
		r.sendWithBackpressure(r.InterventionResultChan, interventionResults, "intervention")
	}
	if len(codeResults) > 0 {
		r.sendWithBackpressure(r.CodeResultChan, codeResults, "code")
	}
}

// sendWithBackpressure attempts to send a batch to a channel with exponential backoff.
// Uses time.After instead of a ticker to reduce CPU wakeups when channel is full.
func (r *ResultRouter) sendWithBackpressure(ch chan []jobs.Result, batch []jobs.Result, label string) {
	if r.closed.Load() {
		return
	}

	// Fast path: try immediate send
		select {
		case ch <- batch:
			return
		default:
		}

	// Channel is full - enter backpressure mode with exponential backoff
	baseBackoff := r.config.ResultBatchTimeout
	if baseBackoff <= 0 {
		baseBackoff = 50 * time.Millisecond
	}

	const maxAttempts = 10
	backoff := baseBackoff

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Use time.After for each retry instead of continuous ticker
		// This is more efficient: no timer allocation when channel has space
		select {
		case ch <- batch:
			return
		case <-r.stopCh:
			if r.logger != nil {
				r.logger.Printf("Dropping %s results during shutdown (%d jobs waiting)", label, len(batch))
			}
			return
		case <-time.After(backoff):
				if r.logger != nil {
				r.logger.Printf("Backpressure: %s results stalled (%d jobs, attempt %d/%d)", label, len(batch), attempt+1, maxAttempts)
				}
			// Exponential backoff capped at 500ms
			backoff = backoff * 2
			if backoff > 500*time.Millisecond {
				backoff = 500 * time.Millisecond
			}
			}
		}

	// Max attempts reached - drop results
	if r.logger != nil {
		r.logger.Printf("Dropping %s results after %d stalled sends", label, maxAttempts)
	}
}

// Close closes all result channels.
func (r *ResultRouter) Close() {
	if r.closed.Swap(true) {
		return
	}
	close(r.PulseResultChan)
	close(r.InterventionResultChan)
	close(r.CodeResultChan)
}

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
}

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
	MinWorkers         int
	MaxWorkers         int
	AdjustmentInterval time.Duration
	ResultBatchSize    int
	ResultBatchTimeout time.Duration
	ResultChannelDepth int
	TargetQueueLatency time.Duration

	// M/M/c scaling parameters
	// Asymmetric cooldowns - fast up, slow down
	ScaleUpCooldown   time.Duration // Minimum time between scale-up events (default 30s)
	ScaleDownCooldown time.Duration // Minimum time between scale-down events (default 120s)

	// Hysteresis thresholds to prevent oscillation
	ScaleUpThreshold   float64 // Ratio above current to trigger scale-up (default 1.1 = 10% above)
	ScaleDownThreshold float64 // Ratio below current to trigger scale-down (default 0.8 = 20% below)

	// Warm-up period during which no scaling occurs
	WarmupDuration time.Duration // Default 60s - allows system to stabilize after startup

	// Ants-specific options
	PreAlloc         bool
	NonBlocking      bool
	MaxBlockingTasks int
	ExpiryDuration   time.Duration
}

// DefaultWorkerPoolConfig returns a default configuration for the worker pool.
func DefaultWorkerPoolConfig() WorkerPoolConfig {
	return WorkerPoolConfig{
		MinWorkers:         5,
		MaxWorkers:         8192,
		AdjustmentInterval: 5 * time.Second,
		ResultBatchSize:    512,
		ResultBatchTimeout: 10 * time.Millisecond,
		ResultChannelDepth: 2048,
		TargetQueueLatency: 100 * time.Millisecond,
		// M/M/c scaling defaults
		ScaleUpCooldown:    30 * time.Second,  // React quickly to increased load
		ScaleDownCooldown:  120 * time.Second, // Be conservative about reducing capacity
		ScaleUpThreshold:   1.10,              // Scale up when 10% more workers needed
		ScaleDownThreshold: 0.80,              // Scale down when 20% fewer workers needed
		WarmupDuration:     60 * time.Second,  // No scaling during first minute
		// Ants-specific options
		PreAlloc:         false,
		NonBlocking:      false,
		MaxBlockingTasks: 0,
		ExpiryDuration:   5 * time.Minute,
	}
}

// optimalResultChannelDepth calculates the optimal result channel buffer size
// based on worker count and batch size. This balances memory usage with throughput.
//
// The formula considers:
// - At least 2x batch size to allow double-buffering
// - Scaled to worker count to handle burst capacity
// - Capped at maxWorkers to prevent memory waste at low load
func optimalResultChannelDepth(maxWorkers, minWorkers, batchSize int) int {
	if batchSize <= 0 {
		batchSize = 256
	}

	// Base: 2x batch size for double-buffering
	depth := batchSize * 2

	// Scale up based on worker count (1 result per worker in flight)
	// Use sqrt scaling to balance memory vs throughput
	workerScale := int(math.Sqrt(float64(maxWorkers)))
	if workerScale > depth {
		depth = workerScale
	}

	// Cap at maxWorkers (each worker produces at most 1 result)
	if depth > maxWorkers {
		depth = maxWorkers
	}

	// Minimum of minWorkers * 2 to handle burst at startup
	if depth < minWorkers*2 {
		depth = minWorkers * 2
	}

	// Never smaller than batch size
	if depth < batchSize {
		depth = batchSize
	}

	return depth
}

// NewDynamicWorkerPool creates a new dynamic worker pool.
func NewDynamicWorkerPool(ctx context.Context, q Queue, config WorkerPoolConfig, logger *log.Logger) (*DynamicWorkerPool, error) {
	if config.MinWorkers <= 0 {
		config.MinWorkers = 1
	}
	if config.MaxWorkers < config.MinWorkers {
		config.MaxWorkers = config.MinWorkers
	}
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

	// Build ants options
	var antsOptions []ants.Option
	if pool.logger != nil {
		antsOptions = append(antsOptions, ants.WithLogger(pool.logger))
	}
	antsOptions = append(antsOptions, ants.WithPanicHandler(func(err interface{}) {
		if pool.logger != nil {
			pool.logger.Printf("Worker panic: %v", err)
		}
	}))

	if config.PreAlloc {
		antsOptions = append(antsOptions, ants.WithPreAlloc(true))
	}
	if config.NonBlocking {
		antsOptions = append(antsOptions, ants.WithNonblocking(true))
	}
	if config.MaxBlockingTasks > 0 {
		antsOptions = append(antsOptions, ants.WithMaxBlockingTasks(config.MaxBlockingTasks))
	}
	if config.ExpiryDuration > 0 {
		antsOptions = append(antsOptions, ants.WithExpiryDuration(config.ExpiryDuration))
	}

	antsPool, err := ants.NewPoolWithFunc(config.MaxWorkers, workerFunc, antsOptions...)
	if err != nil {
		return nil, err
	}
	pool.antsPool = antsPool
	pool.antsPool.Tune(config.MinWorkers)
	pool.lastTarget.Store(int64(config.MinWorkers))
	pool.lastScaleTime.Store(time.Now().UnixNano())

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
	p.wg.Add(routineCount)
	go p.dispatcher()
	go p.resultProcessor()
	if p.config.AdjustmentInterval > 0 {
		go p.autoScale()
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
	if !p.stopping.CompareAndSwap(0, 1) {
		return
	}

	startTime := time.Now()
	pending := p.tasksSubmitted.Load() - p.tasksCompleted.Load()

	if p.logger != nil {
		p.logger.Printf("Draining DynamicWorkerPool (pending=%d, running=%d)...",
			pending, p.antsPool.Running())
	}

	// Signal all goroutines to stop
	p.stopOnce.Do(func() { close(p.stopCh) })
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
	if waitTimeout <= 0 {
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
		close(p.resultChan)
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
			if !errors.Is(err, ErrQueueClosed) {
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

		select {
		case p.resultChan <- result:
			return
		case <-p.stopCh:
			// Rate-limited logging: only log every 1000th drop to avoid spam
			dropped := p.droppedResults.Add(1)
			if p.logger != nil && dropped == 1 {
				p.logger.Printf("Dropping results during shutdown (result channel stalled)")
			}
			p.tasksCompleted.Add(1)
			return
		case <-time.After(backoff):
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

// autoScale periodically tunes the ants pool capacity using M/M/c queueing theory.
// Implements hysteresis and asymmetric cooldowns to prevent oscillation.
func (p *DynamicWorkerPool) autoScale() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.AdjustmentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			// Skip scaling during warmup period
			if time.Since(p.startTime) < p.config.WarmupDuration {
				continue
			}

			stats := p.queue.Stats()

			// Record metrics to rolling windows
			p.metrics.Record(stats)
			p.metrics.RecordUtilization(p.antsPool.Running(), p.antsPool.Cap())

			desired := p.desiredCapacity(stats)
			current := p.antsPool.Cap()

			// No change needed
			if desired == current {
				continue
			}

			now := time.Now()

			if desired > current {
				// SCALE UP: Check hysteresis threshold
				ratio := float64(desired) / float64(current)
				if ratio < p.config.ScaleUpThreshold {
					// Change too small - skip to prevent oscillation
					continue
				}

				// Check cooldown period
				if now.Sub(p.lastScaleUpTime) < p.config.ScaleUpCooldown {
					continue
				}

				// Apply scale up
				p.antsPool.Tune(desired)
				p.lastScaleUpTime = now
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(now.UnixNano())
				p.scalingEvents.Add(1)

				if p.logger != nil {
					p.logger.Printf("Scaled UP worker pool: %d → %d (ratio=%.2f, queue=%d)",
						current, desired, ratio, stats.QueueDepth)
				}
			} else {
				// SCALE DOWN: More conservative - check hysteresis threshold
				ratio := float64(desired) / float64(current)
				if ratio > p.config.ScaleDownThreshold {
					// Change too small - skip to prevent oscillation
					continue
				}

				// Use long window to ensure sustained low utilization
				longMetrics := p.metrics.GetLongWindowMetrics()
				if longMetrics.SampleCount < 10 {
					// Not enough history - don't scale down yet
					continue
				}
				if longMetrics.AvgUtilization > 0.25 {
					// Still moderately utilized over long term - don't scale down
					continue
				}
				if longMetrics.AvgQueueDepth > float64(p.config.MinWorkers) {
					// Queue not consistently empty - don't scale down
					continue
				}

				// Check longer cooldown period for scale down
				if now.Sub(p.lastScaleDownTime) < p.config.ScaleDownCooldown {
					continue
				}

				// Apply scale down
				p.antsPool.Tune(desired)
				p.lastScaleDownTime = now
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(now.UnixNano())
				p.scalingEvents.Add(1)

				if p.logger != nil {
					p.logger.Printf("Scaled DOWN worker pool: %d → %d (ratio=%.2f, longUtil=%.2f)",
						current, desired, ratio, longMetrics.AvgUtilization)
				}
			}
		}
	}
}

func (p *DynamicWorkerPool) desiredCapacity(stats Stats) int {
	current := p.antsPool.Cap()
	if current <= 0 {
		current = p.config.MinWorkers
	}

	minWorkers := p.config.MinWorkers
	maxWorkers := p.config.MaxWorkers
	if maxWorkers < minWorkers {
		maxWorkers = minWorkers
	}

	// 1. Get observed arrival rate from short window (reacts to spikes)
	lambda := p.metrics.GetShortWindowMetrics().AvgEnqueueRate
	if lambda <= 0 {
		lambda = stats.EnqueueRate
	}
	if lambda <= 0 {
		// No load - return minimum
		return minWorkers
	}

	// 2. Estimate service time (τ) from observed throughput
	tau := p.estimateServiceTime(stats, current)
	if tau <= 0 {
		// No throughput data yet - use heuristic from Little's Law fallback
		return p.littleLawFallback(lambda, stats, current)
	}

	// 3. Get variability coefficients for Allen-Cunneen
	ca, cs := p.metrics.GetVariabilityCoefficients()
	// Defaults already set to 1.0 by GetVariabilityCoefficients() if insufficient data

	// 4. Calculate optimal worker count using M/M/c + Allen-Cunneen
	wTarget := p.config.TargetQueueLatency.Seconds()
	cTheory, _, err := FindCForSLO(lambda, tau, wTarget, ca, cs, maxWorkers)

	// 5. Fallback if M/M/c calculation fails (e.g., unstable system)
	if err != nil {
		if p.logger != nil {
			p.logger.Printf("M/M/c calculation failed (λ=%.2f, τ=%.3fs): %v; using Little's Law fallback",
				lambda, tau, err)
		}
		return p.littleLawFallback(lambda, stats, current)
	}

	// 6. Add 15% headroom for safety margin and clamp
	desired := int(math.Ceil(float64(cTheory) * 1.15))
	return clamp(desired, minWorkers, maxWorkers)
}

// estimateServiceTime estimates the average service time per job from observed metrics.
// It prefers actual measured service times when available, falling back to throughput-based
// estimation only when insufficient measurement data exists.
func (p *DynamicWorkerPool) estimateServiceTime(stats Stats, currentWorkers int) float64 {
	// Prefer actual measured service times (recorded in workerFunc)
	measuredTau := p.metrics.GetAverageServiceTime()
	if measuredTau > 0 {
		// Sanity bounds: tau should be between 1ms and 30s
		if measuredTau < 0.001 {
			measuredTau = 0.001
		}
		if measuredTau > 30.0 {
			measuredTau = 30.0
		}
		return measuredTau
	}

	// Fallback: estimate from throughput if no measured data yet
	if currentWorkers <= 0 || stats.DequeueRate <= 0 {
		return 0
	}

	running := p.antsPool.Running()
	if running <= 0 {
		running = 1
	}

	// Service time = active workers / throughput
	// This is only used during initial warmup before we have measurements
	tau := float64(running) / stats.DequeueRate

	// Conservative bounds for fallback estimation
	if tau < 0.001 {
		tau = 0.001
	}
	if tau > 1.0 {
		// Cap fallback at 1s - if jobs really take longer, measurements will show it
		tau = 1.0
	}
	return tau
}

// littleLawFallback uses Little's Law (L = λW) as a fallback scaling heuristic.
func (p *DynamicWorkerPool) littleLawFallback(lambda float64, stats Stats, current int) int {
	minWorkers := p.config.MinWorkers
	maxWorkers := p.config.MaxWorkers
	targetLatency := p.config.TargetQueueLatency
	if targetLatency <= 0 {
		targetLatency = 100 * time.Millisecond
	}

	desired := current

	// Estimate per-worker throughput
	perWorker := 0.0
	if current > 0 && stats.DequeueRate > 0 {
		perWorker = stats.DequeueRate / float64(current)
	}
	if perWorker > 0 && lambda > 0 {
		desired = int(math.Ceil(lambda / perWorker))
	}

	// Enforce latency budget: target queue depth = λ * W_target
	targetDepth := lambda * targetLatency.Seconds()
	if targetDepth < float64(minWorkers) {
		targetDepth = float64(minWorkers)
	}

	depth := float64(stats.QueueDepth)
	if depth > targetDepth && targetDepth > 0 {
		// Queue building up - scale up proportionally
		scale := depth / targetDepth
		desired = int(math.Ceil(float64(desired) * scale))
	}

	return clamp(desired, minWorkers, maxWorkers)
}

// clamp constrains a value between min and max bounds.
func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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

// Tune adjusts the worker pool capacity to the specified number of workers.
// This is used for initial pre-sizing based on M/M/c calculations.
// The capacity is clamped between MinWorkers and MaxWorkers.
func (p *DynamicWorkerPool) Tune(capacity int) {
	if capacity < p.config.MinWorkers {
		capacity = p.config.MinWorkers
	}
	if capacity > p.config.MaxWorkers {
		capacity = p.config.MaxWorkers
	}
	current := p.antsPool.Cap()
	if capacity != current {
		p.antsPool.Tune(capacity)
		p.lastTarget.Store(int64(capacity))
		p.lastScaleTime.Store(time.Now().UnixNano())
		p.scalingEvents.Add(1)
		if p.logger != nil {
			p.logger.Printf("Pre-sized worker pool from %d to %d workers", current, capacity)
		}
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
