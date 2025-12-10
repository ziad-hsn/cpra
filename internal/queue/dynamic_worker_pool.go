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

func (r *ResultRouter) sendWithBackpressure(ch chan []jobs.Result, batch []jobs.Result, label string) {
	if r.closed.Load() {
		return
	}
	backoff := r.config.ResultBatchTimeout
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	ticker := time.NewTicker(backoff)
	defer ticker.Stop()

	attempts := 0
	const maxAttempts = 10
	for {
		select {
		case ch <- batch:
			return
		default:
		}

		select {
		case ch <- batch:
			return
		case <-ticker.C:
			if r.logger != nil {
				r.logger.Printf("Backpressure: %s results stalled (%d jobs waiting)", label, len(batch))
			}
			attempts++
			if attempts >= maxAttempts {
				if r.logger != nil {
					r.logger.Printf("Dropping %s results after %d stalled sends", label, attempts)
				}
				return
			}
		case <-r.stopCh:
			if r.logger != nil {
				r.logger.Printf("Dropping %s results during shutdown (%d jobs waiting)", label, len(batch))
			}
			return
		}
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
		PreAlloc:           false,
		NonBlocking:        false,
		MaxBlockingTasks:   0,
		ExpiryDuration:     5 * time.Minute,
	}
}

// NewDynamicWorkerPool creates a new dynamic worker pool.
func NewDynamicWorkerPool(q Queue, config WorkerPoolConfig, logger *log.Logger) (*DynamicWorkerPool, error) {
	if config.MinWorkers <= 0 {
		config.MinWorkers = 1
	}
	if config.MaxWorkers < config.MinWorkers {
		config.MaxWorkers = config.MinWorkers
	}
	if config.ResultBatchSize <= 0 {
		config.ResultBatchSize = 256
	}
	if config.ResultChannelDepth <= 0 {
		config.ResultChannelDepth = config.ResultBatchSize * 4
	}
	if config.ResultChannelDepth > config.MaxWorkers {
		config.ResultChannelDepth = config.MaxWorkers
	}
	if config.ResultChannelDepth <= 0 {
		config.ResultChannelDepth = config.MinWorkers * 2
	}
	if config.ResultChannelDepth < config.ResultBatchSize {
		config.ResultChannelDepth = config.ResultBatchSize
	}
	if config.TargetQueueLatency <= 0 {
		config.TargetQueueLatency = 100 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopCh := make(chan struct{})

	pool := &DynamicWorkerPool{
		queue:      q,
		logger:     logger,
		config:     config,
		resultChan: make(chan jobs.Result, config.ResultChannelDepth),
		router:     NewResultRouter(config, logger, stopCh),
		ctx:        ctx,
		cancel:     cancel,
		stopCh:     stopCh,
	}
	pool.resultBatchPool.New = func() any {
		s := make([]jobs.Result, 0, pool.config.ResultBatchSize)
		return &s
	}

	workerFunc := func(job interface{}) {
		j, ok := job.(jobs.Job)
		if !ok {
			if pool.logger != nil {
				pool.logger.Printf("Error: Invalid job type in worker pool: %T", job)
			}
			return
		}
		result := j.Execute()
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
	if p.logger != nil {
		p.logger.Println("Draining DynamicWorkerPool...")
	}
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.cancel()

	drainWindow := p.config.TargetQueueLatency * 5
	if drainWindow < 2*time.Second {
		drainWindow = 2 * time.Second
	}
	p.waitForResultsUntil(time.Now().Add(drainWindow))

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
	case <-time.After(waitTimeout):
		if p.logger != nil {
			p.logger.Println("Draining timed out; forcing shutdown and dropping remaining results")
		}
	}

	p.router.Close()
	p.antsPool.Release()
	if p.logger != nil {
		p.logger.Println("DynamicWorkerPool stopped")
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
			p.tasksSubmitted.Add(int64(len(batch)))
			for _, job := range batch {
				if err := p.antsPool.Invoke(job); err != nil {
					p.logger.Printf("Error invoking job: %v", err)
				}
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
func (p *DynamicWorkerPool) resultProcessor() {
	defer p.wg.Done()

	batch := p.getResultBatch()
	ticker := time.NewTicker(p.config.ResultBatchTimeout)
	defer ticker.Stop()

	draining := false
	for {
		select {
		case <-p.stopCh:
			draining = true
		case <-p.ctx.Done():
			draining = true
		case result, ok := <-p.resultChan:
			if !ok { // resultChan was closed
				if len(*batch) > 0 {
					p.router.RouteResults(*batch)
					p.putResultBatch(batch)
				}
				return
			}
			p.tasksCompleted.Add(1)
			*batch = append(*batch, result)
			if len(*batch) >= p.config.ResultBatchSize {
				p.router.RouteResults(*batch)
				p.putResultBatch(batch)
				batch = p.getResultBatch()
				// Reset the ticker to prevent immediate firing
				ticker.Reset(p.config.ResultBatchTimeout)
			}
		case <-ticker.C:
			// Route partial batches on timeout
			if len(*batch) > 0 {
				p.router.RouteResults(*batch)
				p.putResultBatch(batch)
				batch = p.getResultBatch()
			}
		}

		if draining {
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
func (p *DynamicWorkerPool) deliverResult(result jobs.Result) {
	backoff := p.config.ResultBatchTimeout
	if backoff <= 0 {
		backoff = 10 * time.Millisecond
	}
	attempts := 0
	for {
		if p.resultsClosed.Load() {
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
			if p.logger != nil {
				p.logger.Printf("Dropping result during shutdown (result channel stalled)")
			}
			attempts++
		case <-time.After(backoff):
			if p.stopping.Load() == 0 {
				continue
			}
			attempts++
		}
		if attempts >= 5 {
			if p.logger != nil {
				p.logger.Printf("Dropping result during shutdown after %d send attempts", attempts)
			}
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

// autoScale periodically tunes the ants pool capacity based on queue depth.
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
			stats := p.queue.Stats()
			desired := p.desiredCapacity(stats)
			current := p.antsPool.Cap()
			if desired != current {
				p.antsPool.Tune(desired)
				if p.logger != nil {
					p.logger.Printf("Tuned worker pool capacity to %d (queue depth=%d)", desired, stats.QueueDepth)
				}
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(time.Now().UnixNano())
				p.scalingEvents.Add(1)
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

	enqueueRate := stats.EnqueueRate
	if enqueueRate <= 0 {
		enqueueRate = stats.DequeueRate
	}
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
	if perWorker > 0 && enqueueRate > 0 {
		desired = int(math.Ceil(enqueueRate / perWorker))
	}

	// Enforce latency budget using Little's Law (L = λW)
	if enqueueRate > 0 {
		targetDepth := enqueueRate * targetLatency.Seconds()
		// Always allow at least minWorkers entities worth of backlog
		if targetDepth < float64(minWorkers) {
			targetDepth = float64(minWorkers)
		}
		depth := float64(stats.QueueDepth)
		if depth > targetDepth && targetDepth > 0 {
			scale := depth / targetDepth
			desired = int(math.Ceil(float64(desired) * scale))
		} else if depth < targetDepth/2 && desired > minWorkers {
			// More conservative scaling down for small workloads
			if current > minWorkers*2 {
				// Scale down gradually for larger pools
				desired = int(math.Max(float64(minWorkers), math.Ceil(float64(desired)*0.9)))
			} else {
				// For small pools near minimum, maintain capacity longer
				desired = int(math.Max(float64(minWorkers), math.Ceil(float64(desired)*0.95)))
			}
		}
	}

	if desired < minWorkers {
		desired = minWorkers
	}
	if desired > maxWorkers {
		desired = maxWorkers
	}
	return desired
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
