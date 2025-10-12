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

	config WorkerPoolConfig
	logger *log.Logger
}

// WorkerPoolStats exposes runtime metrics for the dynamic worker pool.
type WorkerPoolStats struct {
	MinWorkers      int
	MaxWorkers      int
	CurrentCapacity int
	RunningWorkers  int
	WaitingTasks    int
	TargetWorkers   int
	TasksSubmitted  int64
	TasksCompleted  int64
	ScalingEvents   int64
	LastScaleTime   time.Time
	PendingResults  int
}

// NewResultRouter creates a new result router with buffered channels.
func NewResultRouter(config WorkerPoolConfig, logger *log.Logger) *ResultRouter {
	bufferSize := config.MaxWorkers // Buffer size based on max workers
	return &ResultRouter{
		PulseResultChan:        make(chan []jobs.Result, bufferSize),
		InterventionResultChan: make(chan []jobs.Result, bufferSize),
		CodeResultChan:         make(chan []jobs.Result, bufferSize),
		config:                 config,
		logger:                 logger,
	}
}

// RouteResults takes a batch of mixed results and routes them to appropriate channels.
func (r *ResultRouter) RouteResults(results []jobs.Result) {
	if len(results) == 0 {
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
			r.logger.Printf("Unknown job type in result: %v", result.Payload["type"])
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
	backoff := r.config.ResultBatchTimeout
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	ticker := time.NewTicker(backoff)
	defer ticker.Stop()

	for {
		select {
		case ch <- batch:
			return
		case <-ticker.C:
			r.logger.Printf("Backpressure: %s results stalled (%d jobs waiting)", label, len(batch))
		}
	}
}

// Close closes all result channels.
func (r *ResultRouter) Close() {
	close(r.PulseResultChan)
	close(r.InterventionResultChan)
	close(r.CodeResultChan)
}

// DynamicWorkerPool manages a pool of workers that execute jobs from a queue.
// It can dynamically adjust the number of workers based on load.
type DynamicWorkerPool struct {
	queue      Queue
	antsPool   *ants.PoolWithFunc
	logger     *log.Logger
	config     WorkerPoolConfig
	resultChan chan jobs.Result
	router     *ResultRouter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	tasksSubmitted atomic.Int64
	tasksCompleted atomic.Int64
	scalingEvents  atomic.Int64
	lastTarget     atomic.Int64
	lastScaleTime  atomic.Int64
	stopping       atomic.Int32
}

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
    MinWorkers         int
    MaxWorkers         int
    AdjustmentInterval time.Duration
    ResultBatchSize    int
    ResultBatchTimeout time.Duration
    TargetQueueLatency time.Duration
    // Ants-specific options
    PreAlloc       bool
    NonBlocking    bool
    MaxBlockingTasks int
    ExpiryDuration time.Duration
}

// DefaultWorkerPoolConfig returns a default configuration for the worker pool.
func DefaultWorkerPoolConfig() WorkerPoolConfig {
    return WorkerPoolConfig{
        MinWorkers:         5, // Reduced from 10 to allow smaller workloads
        MaxWorkers:         120000,
        AdjustmentInterval: 5 * time.Second, // Increased from 5s to reduce oscillation
        ResultBatchSize:    1000,
        ResultBatchTimeout: 10 * time.Millisecond,
        TargetQueueLatency: 100 * time.Millisecond,
        PreAlloc:           false,
        NonBlocking:        false,
        MaxBlockingTasks:   0,
        ExpiryDuration:     5 * time.Minute, // Better aligned with job timeouts (1m-1h)
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
		config.ResultBatchSize = config.MaxWorkers
	}
	if config.TargetQueueLatency <= 0 {
		config.TargetQueueLatency = 100 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool := &DynamicWorkerPool{
		queue:      q,
		logger:     logger,
		config:     config,
		resultChan: make(chan jobs.Result, config.MaxWorkers),
		router:     NewResultRouter(config, logger),
		ctx:        ctx,
		cancel:     cancel,
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
		if pool.stopping.Load() == 1 {
			return
		}
		select {
		case pool.resultChan <- result:
		case <-pool.ctx.Done():
		}
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
	p.cancel()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(p.config.TargetQueueLatency * 5):
		if p.logger != nil {
			p.logger.Println("Draining timed out, continuing shutdown")
		}
	}
	remaining := len(p.resultChan)
	if remaining > 0 && p.logger != nil {
		p.logger.Printf("Flushing %d queued results before close", remaining)
	}
	close(p.resultChan)
	p.router.Close()
	p.antsPool.Release()
	if p.logger != nil {
		p.logger.Println("DynamicWorkerPool stopped")
	}
}

// dispatcher fetches batches of jobs from the queue and submits them to the ants pool.
func (p *DynamicWorkerPool) dispatcher() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
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

			batch, err := p.queue.DequeueBatch(batchTarget)
			if err != nil {
				if !errors.Is(err, ErrQueueClosed) {
					p.logger.Printf("Error dequeuing job batch: %v", err)
				}
				time.Sleep(100 * time.Millisecond) // Wait a bit if there's an error
				continue
			}
			if len(batch) == 0 {
				time.Sleep(10 * time.Millisecond) // Wait if the queue is empty
				continue
			}

			p.tasksSubmitted.Add(int64(len(batch)))

			for _, job := range batch {
				if err := p.antsPool.Invoke(job); err != nil {
					p.logger.Printf("Error invoking job: %v", err)
				}
			}
		}
	}
}

// resultProcessor collects individual results and routes them through the router in batches.
func (p *DynamicWorkerPool) resultProcessor() {
	defer p.wg.Done()

	batch := make([]jobs.Result, 0, p.config.ResultBatchSize)
	ticker := time.NewTicker(p.config.ResultBatchTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			// Route any remaining results before shutting down
			if len(batch) > 0 {
				p.router.RouteResults(batch)
			}
			return
		case result, ok := <-p.resultChan:
			if !ok { // resultChan was closed
				if len(batch) > 0 {
					p.router.RouteResults(batch)
				}
				return
			}
			p.tasksCompleted.Add(1)
			batch = append(batch, result)
			if len(batch) >= p.config.ResultBatchSize {
				p.router.RouteResults(batch)
				batch = make([]jobs.Result, 0, p.config.ResultBatchSize)
				// Reset the ticker to prevent immediate firing
				ticker.Reset(p.config.ResultBatchTimeout)
			}
		case <-ticker.C:
			// Route partial batches on timeout
			if len(batch) > 0 {
				p.router.RouteResults(batch)
				batch = make([]jobs.Result, 0, p.config.ResultBatchSize)
			}
		}
	}
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
		p.logger.Println("Pausing worker pool...")
	}
	if p.antsPool != nil {
		p.antsPool.Tune(0) // Reduce capacity to 0 to pause processing
	}
}

// Resume resumes worker pool processing after a pause.
func (p *DynamicWorkerPool) Resume() {
	if p.logger != nil {
		p.logger.Println("Resuming worker pool...")
	}
	if p.antsPool != nil {
		// Restore to minimum workers
		p.antsPool.Tune(p.config.MinWorkers)
	}
}

// ReplaceQueue replaces the current queue with a new one.
// This is used for dynamic queue switching (e.g., from Workiva to Adaptive).
func (p *DynamicWorkerPool) ReplaceQueue(newQueue Queue) error {
	if newQueue == nil {
		return errors.New("new queue cannot be nil")
	}

	if p.logger != nil {
		p.logger.Println("Replacing queue in worker pool...")
	}

	// Pause processing to prevent race conditions
	p.Pause()

	// Replace the queue reference
	p.queue = newQueue

	// Resume processing with new queue
	p.Resume()

	if p.logger != nil {
		p.logger.Println("Queue replacement completed")
	}
	return nil
}
