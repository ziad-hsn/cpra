package queue

import (
	"context"
	"cpra/internal/jobs"
	"errors"
	"log"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/puzpuzpuz/xsync/v4"
)

// ResultRouter handles routing of job results to type-specific channels.
// This enables decoupling result processing from the main worker pool.
type ResultRouter struct {
	PulseResultChan        chan []jobs.Result
	InterventionResultChan chan []jobs.Result
	CodeResultChan         chan []jobs.Result
	logger                 *log.Logger
	config                 WorkerPoolConfig
	done                   <-chan struct{}
	closeOnce              sync.Once
}

// WorkerPoolStats exposes runtime metrics for the dynamic worker pool.
type WorkerPoolStats struct {
	SLOCondition    string        `json:"slo_condition"`
	ServiceTime     time.Duration `json:"service_time"`
	ServiceCV       float64       `json:"service_cv"`
	ServiceSamples  int           `json:"service_samples"`
	SizingModel     string        `json:"sizing_model"`
	LastScaleTime   time.Time     `json:"last_scale_time"`
	MinWorkers      int           `json:"min_workers"`
	MaxWorkers      int           `json:"max_workers"`
	CurrentCapacity int           `json:"current_capacity"`
	RunningWorkers  int           `json:"running_workers"`
	WaitingTasks    int           `json:"waiting_tasks"`
	TargetWorkers   int           `json:"target_workers"`
	TasksSubmitted  int64         `json:"tasks_submitted"`
	TasksCompleted  int64         `json:"tasks_completed"`
	ScalingEvents   int64         `json:"scaling_events"`
	PendingResults  int           `json:"pending_results"`
}

// NewResultRouter creates a new result router with buffered channels.
func NewResultRouter(config WorkerPoolConfig, logger *log.Logger) *ResultRouter {
	bufferSize := config.ResultChannelDepth
	return &ResultRouter{
		PulseResultChan:        make(chan []jobs.Result, bufferSize),
		InterventionResultChan: make(chan []jobs.Result, bufferSize),
		CodeResultChan:         make(chan []jobs.Result, bufferSize),
		config:                 config,
		logger:                 logger,
	}
}

// resultType returns the job class of a result, preferring the typed Result.Type
// field and falling back to Payload["type"] for results created before the field
// was introduced.
func resultType(result jobs.Result) string {
	if result.Type != "" {
		return result.Type
	}
	t, _ := result.Payload["type"].(string)
	return t
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
		switch resultType(result) {
		case "pulse":
			pulseResults = append(pulseResults, result)
		case "intervention":
			interventionResults = append(interventionResults, result)
		case "code":
			codeResults = append(codeResults, result)
		default:
			r.logger.Printf("Unknown job type in result: %v", resultType(result))
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
		case <-r.done:
			return
		case <-ticker.C:
			r.logger.Printf("Backpressure: %s results stalled (%d jobs waiting)", label, len(batch))
		}
	}
}

// Close closes all result channels. It is idempotent.
func (r *ResultRouter) Close() {
	r.closeOnce.Do(func() {
		close(r.PulseResultChan)
		close(r.InterventionResultChan)
		close(r.CodeResultChan)
	})
}

// DynamicWorkerPool manages a pool of workers that execute jobs from a queue.
// It can dynamically adjust the number of workers based on load.
type DynamicWorkerPool struct {
	feedback        *feedback
	serviceMetrics  durationMetrics
	sizingPolicy    atomic.Pointer[sizingPolicy]
	sizingModel     atomic.Uint32
	queueMu         sync.RWMutex
	queue           Queue
	ctx             context.Context
	cancel          context.CancelFunc
	antsPool        *ants.PoolWithFunc
	logger          *log.Logger
	resultQueues    []*xsync.MPMCQueue[jobs.Result]
	notifyChans     []chan struct{}
	numShards       int
	pendingResults  atomic.Int64
	router          *ResultRouter
	config          WorkerPoolConfig
	wg              sync.WaitGroup
	workerWG        sync.WaitGroup
	dispatcherDone  chan struct{}
	stopped         chan struct{}
	workCtx         context.Context
	workCancel      context.CancelFunc
	tasksSubmitted  atomic.Int64
	tasksCompleted  atomic.Int64
	scalingEvents   atomic.Int64
	lastTarget      atomic.Int64
	lastScaleTime   atomic.Int64
	stopping        atomic.Int32
	started         atomic.Bool
	resultBatchPool sync.Pool

	// arrivalRate is the externally-provided job arrival rate (lambda), stored
	// as float64 bits. When set (>0), the autoscaler sizes the pool from it
	// instead of the throttled enqueue rate, so it can scale up even when the
	// queue is empty.
	arrivalRate atomic.Uint64

	// serviceTime is the externally-provided per-job service time (tau, seconds),
	// stored as float64 bits. Used until completed jobs provide observations.
	serviceTime atomic.Uint64
}

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
	MinWorkers         int
	MaxWorkers         int
	AdjustmentInterval time.Duration
	ResultBatchSize    int
	ResultBatchTimeout time.Duration
	ResultChannelDepth int
	// NumShards is the number of result-routing shards. 0 (default) means
	// runtime.GOMAXPROCS(0), capped at 64. Each shard has its own channel and
	// resultProcessor goroutine so results are routed in parallel.
	NumShards          int
	TargetQueueLatency time.Duration
	// DrainTimeout cancels accepted operations after this grace period.
	DrainTimeout time.Duration
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
		NumShards:          0,
		TargetQueueLatency: 100 * time.Millisecond,
		PreAlloc:           false,
		NonBlocking:        false,
		MaxBlockingTasks:   0,
		ExpiryDuration:     5 * time.Minute,
	}
}

// NewDynamicWorkerPool creates a new dynamic worker pool.
func NewDynamicWorkerPool(q Queue, config WorkerPoolConfig, logger *log.Logger) (*DynamicWorkerPool, error) {
	if config.PreAlloc {
		return nil, errors.New("preallocated workers do not support dynamic resizing")
	}
	if config.MinWorkers <= 0 {
		config.MinWorkers = 1
	}
	if config.MaxWorkers < config.MinWorkers {
		config.MaxWorkers = config.MinWorkers
	}
	if config.ResultBatchSize <= 0 {
		config.ResultBatchSize = 256
	}
	if config.ResultBatchTimeout <= 0 {
		config.ResultBatchTimeout = 10 * time.Millisecond
	}
	if config.ResultChannelDepth <= 0 {
		config.ResultChannelDepth = config.ResultBatchSize * 4
	}
	if config.ResultChannelDepth < config.ResultBatchSize {
		config.ResultChannelDepth = config.ResultBatchSize
	}
	if config.ResultChannelDepth > config.MaxWorkers {
		config.ResultChannelDepth = config.MaxWorkers
	}
	if config.ResultChannelDepth <= 0 {
		config.ResultChannelDepth = config.MinWorkers * 2
	}
	if config.TargetQueueLatency <= 0 {
		config.TargetQueueLatency = 100 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())

	numShards := config.NumShards
	if numShards <= 0 {
		numShards = runtime.GOMAXPROCS(0)
	}
	if numShards > 64 {
		numShards = 64
	}
	if numShards < 1 {
		numShards = 1
	}
	// Per-shard buffer keeps total buffering bounded to ~ResultChannelDepth.
	shardDepth := config.ResultChannelDepth / numShards
	if shardDepth < 64 {
		shardDepth = 64
	}

	pool := &DynamicWorkerPool{
		queue:        q,
		logger:       logger,
		config:       config,
		resultQueues: make([]*xsync.MPMCQueue[jobs.Result], numShards),
		notifyChans:  make([]chan struct{}, numShards),
		numShards:    numShards,
		router:       NewResultRouter(config, logger),
		ctx:          ctx,
		cancel:       cancel,
	}
	for i := range pool.resultQueues {
		pool.resultQueues[i] = xsync.NewMPMCQueue[jobs.Result](shardDepth)
		pool.notifyChans[i] = make(chan struct{}, 1)
	}
	pool.resultBatchPool.New = func() any {
		b := make([]jobs.Result, 0, pool.config.ResultBatchSize)
		return &b
	}
	pool.workCtx, pool.workCancel = context.WithCancel(context.Background())
	pool.dispatcherDone = make(chan struct{})
	pool.stopped = make(chan struct{})

	workerFunc := func(job interface{}) {
		defer pool.workerWG.Done()
		j, ok := job.(jobs.Job)
		if !ok {
			if pool.logger != nil {
				pool.logger.Printf("Error: Invalid job type in worker pool: %T", job)
			}
			return
		}
		if cj, ok := j.(interface{ SetContext(context.Context) }); ok {
			cj.SetContext(pool.workCtx)
		}
		started := time.Now()
		result := j.Execute()
		pool.serviceMetrics.observe(time.Since(started))
		shard := int(result.Entity().ID() % uint32(pool.numShards))
		q := pool.resultQueues[shard]
		// Fast path: lock-free enqueue into the shard's MPMC queue. This avoids
		// the per-send hchan lock that dominated the worker->shard handoff.
		if q.TryEnqueue(result) {
			pool.pendingResults.Add(1)
			pool.notify(shard)
			return
		}
		// Slow path: shard queue full (backpressure). Spin-yield until there is
		// room or the pool is shutting down.
		for {
			if q.TryEnqueue(result) {
				pool.pendingResults.Add(1)
				pool.notify(shard)
				return
			}
			select {
			case <-pool.ctx.Done():
				return
			default:
				runtime.Gosched()
			}
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

// Start begins the worker pool's operations. It is idempotent: a second call
// is a no-op, so Start cannot spawn duplicate goroutines or corrupt wg.
func (p *DynamicWorkerPool) Start() {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	routineCount := 1 + p.numShards
	if p.config.AdjustmentInterval > 0 {
		routineCount++
	}
	p.wg.Add(routineCount)
	go p.dispatcher()
	for i := 0; i < p.numShards; i++ {
		go p.resultProcessor(i)
	}
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
		<-p.stopped
		return
	}
	defer close(p.stopped)
	if !p.started.Load() {
		p.cancel()
		p.workCancel()
		p.antsPool.Release()
		p.router.Close()
		return
	}
	timeout := p.config.DrainTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.AfterFunc(timeout, p.workCancel)
	defer timer.Stop()
	// The dispatcher drains the accepted queue, then no more workerWG.Add can run.
	<-p.dispatcherDone
	p.workerWG.Wait()
	p.workCancel()
	// Every worker has published its result; processors flush before closing routes.
	p.cancel()
	p.wg.Wait()
	p.antsPool.Release()
	p.router.Close()
}

func (p *DynamicWorkerPool) Queue() Queue {
	p.queueMu.RLock()
	defer p.queueMu.RUnlock()
	return p.queue
}

// dispatcher fetches batches of jobs from the queue and submits them to the ants pool.
func (p *DynamicWorkerPool) dispatcher() {
	defer close(p.dispatcherDone)
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

			p.queueMu.RLock()
			q := p.queue
			p.queueMu.RUnlock()
			batch, err := q.DequeueBatch(batchTarget)
			if err != nil {
				if errors.Is(err, ErrQueueClosed) && p.stopping.Load() != 0 {
					return
				}
				if !errors.Is(err, ErrQueueClosed) {
					p.logger.Printf("Error dequeuing job batch: %v", err)
				}
				time.Sleep(100 * time.Millisecond) // Wait a bit if there's an error
				continue
			}
			if len(batch) == 0 {
				if p.stopping.Load() != 0 {
					return
				}
				time.Sleep(10 * time.Millisecond) // Wait if the queue is empty
				continue
			}

			p.tasksSubmitted.Add(int64(len(batch)))

			for _, job := range batch {
				p.workerWG.Add(1)
				for {
					if err := p.antsPool.Invoke(job); err == nil {
						break
					}
					// A full nonblocking pool is backpressure, not permission to lose an accepted job.
					time.Sleep(time.Millisecond)
				}
			}
		}
	}
}

// resultProcessor collects individual results from one shard and routes them
// through the router in batches. Running one goroutine per shard removes the
// single-goroutine result-routing funnel.
func (p *DynamicWorkerPool) resultProcessor(shard int) {
	defer p.wg.Done()

	q := p.resultQueues[shard]
	notify := p.notifyChans[shard]
	batch := p.getResultBatch()
	ticker := time.NewTicker(p.config.ResultBatchTimeout)
	defer ticker.Stop()

	for {
		// Drain all available results lock-free (no hchan lock).
		for {
			result, ok := q.TryDequeue()
			if !ok {
				break
			}
			p.pendingResults.Add(-1)
			batch = p.handleResult(batch, result, ticker)
		}

		// Queue empty. Block for a wakeup, a partial-batch timeout, or shutdown.
		select {
		case <-p.ctx.Done():
			for {
				result, ok := q.TryDequeue()
				if !ok {
					break
				}
				p.pendingResults.Add(-1)
				batch = p.handleResult(batch, result, ticker)
			}
			p.flushResultBatch(batch)
			return
		case <-notify:
			// A worker enqueued; drain again.
		case <-ticker.C:
			// Route partial batches on timeout.
			if len(batch) > 0 {
				p.router.RouteResults(batch)
				p.putResultBatch(batch)
				batch = p.getResultBatch()
			}
		}
	}
}

// notify wakes the shard's resultProcessor (non-blocking). The buffered
// channel of size 1 coalesces wakeups: a signal is dropped when the processor
// is already awake, so the common case is a single cheap send.
func (p *DynamicWorkerPool) notify(shard int) {
	select {
	case p.notifyChans[shard] <- struct{}{}:
	default:
	}
}

// handleResult appends a result to the batch and routes it when full.
// It returns the (possibly new) batch slice.
func (p *DynamicWorkerPool) handleResult(batch []jobs.Result, result jobs.Result, ticker *time.Ticker) []jobs.Result {
	p.tasksCompleted.Add(1)
	batch = append(batch, result)
	if len(batch) >= p.config.ResultBatchSize {
		p.router.RouteResults(batch)
		p.putResultBatch(batch)
		batch = p.getResultBatch()
		// Reset the ticker to prevent immediate firing
		ticker.Reset(p.config.ResultBatchTimeout)
	}
	return batch
}

// flushResultBatch routes any remaining results in a partial batch.
func (p *DynamicWorkerPool) flushResultBatch(batch []jobs.Result) {
	if len(batch) > 0 {
		p.router.RouteResults(batch)
		p.putResultBatch(batch)
	}
}

func (p *DynamicWorkerPool) getResultBatch() []jobs.Result {
	if v := p.resultBatchPool.Get(); v != nil {
		return (*v.(*[]jobs.Result))[:0]
	}
	return make([]jobs.Result, 0, p.config.ResultBatchSize)
}

func (p *DynamicWorkerPool) putResultBatch(batch []jobs.Result) {
	if batch == nil {
		return
	}
	batch = batch[:0]
	p.resultBatchPool.Put(&batch)
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
			p.queueMu.RLock()
			q := p.queue
			p.queueMu.RUnlock()
			stats := q.Stats()
			desired := p.desiredCapacity(stats)
			current := p.antsPool.Cap()
			if p.feedback != nil {
				desired = p.feedback.adjust(current, desired, p.config.MaxWorkers, stats.QueueDepth, time.Now())
			}
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

type sizingPolicy struct {
	totalLatency time.Duration
	headroom     float64
}

const (
	sizingWaiting uint32 = iota
	sizingErlang
	sizingFallback
	sizingUnattainable
)

// SetSizingPolicy sets the mean total-latency target and fractional headroom.
// Zero values select TargetQueueLatency plus service time and 15% headroom.
func (p *DynamicWorkerPool) SetSizingPolicy(totalLatency time.Duration, headroom float64) {
	if !finitePositive(headroom) {
		headroom = 0.15
	}
	p.sizingPolicy.Store(&sizingPolicy{totalLatency: totalLatency, headroom: headroom})
}

// GetVariabilityCoefficients returns interarrival and execution-time CVs.
// Unknown variability uses the M/M/c baseline of one. A measured zero is valid.
func (p *DynamicWorkerPool) GetVariabilityCoefficients() (ca, cs float64) {
	stats := p.Queue().Stats()
	ca = 1
	if stats.ArrivalSamples >= 2 {
		ca = stats.ArrivalCV
	}
	_, cs, _ = p.serviceMetrics.snapshot()
	return ca, cs
}

func (p *DynamicWorkerPool) desiredCapacity(stats Stats) int {
	current := p.antsPool.Cap()
	minimum, maximum := p.config.MinWorkers, p.config.MaxWorkers
	if current < minimum {
		current = minimum
	}
	lambda := p.getArrivalRate()
	if !finitePositive(lambda) {
		lambda = stats.EnqueueRate
	}
	if !finitePositive(lambda) {
		lambda = stats.DequeueRate
	}
	if !finitePositive(lambda) {
		p.sizingModel.Store(sizingWaiting)
		return current
	}
	tau, _, samples := p.serviceMetrics.snapshot()
	if samples == 0 {
		tau = p.getServiceTime()
	}
	if !finitePositive(tau) && finitePositive(stats.DequeueRate) {
		tau = float64(current) / stats.DequeueRate
	}
	if !finitePositive(tau) {
		p.sizingModel.Store(sizingWaiting)
		return current
	}
	target := tau + p.config.TargetQueueLatency.Seconds()
	headroom := 0.15
	if policy := p.sizingPolicy.Load(); policy != nil {
		if policy.totalLatency > 0 {
			target = policy.totalLatency.Seconds()
		}
		headroom = policy.headroom
	}
	ca, cs := p.GetVariabilityCoefficients()
	capacity, _, err := FindCForSLO(lambda, tau, target, ca, cs, maximum)
	desired := float64(capacity) * (1 + headroom)
	switch {
	case err == nil:
		p.sizingModel.Store(sizingErlang)
	case errors.Is(err, ErrNoFeasibleCapacity):
		// More workers cannot reduce execution time below the total budget.
		// Use offered load in that case, and report that the SLO is unattainable.
		// If the worker bound is the constraint, request the configured maximum.
		desired = float64(maximum)
		if target <= tau {
			desired = lambda * tau * (1 + headroom)
		}
		p.sizingModel.Store(sizingUnattainable)
	default:
		desired = lambda * tau * (1 + headroom)
		p.sizingModel.Store(sizingFallback)
	}
	// Preserve backlog relief for pools without an external demand estimate.
	// A lifetime average arrival rate can hide a burst after a quiet period.
	// This can raise the model's target; it never removes its headroom.
	if !finitePositive(p.getArrivalRate()) {
		budget := p.config.TargetQueueLatency.Seconds()
		targetDepth := math.Max(float64(minimum), lambda*budget)
		if depth := float64(stats.QueueDepth); depth > targetDepth && targetDepth > 0 {
			desired *= depth / targetDepth
		}
	}
	// Apply bounds before conversion, then limit growth to 2x per adjustment.
	// These constrain actuation; they do not replace the queueing model.
	desired = math.Max(float64(minimum), math.Min(float64(maximum), desired))
	desired = math.Min(desired, float64(current)*2)
	return int(math.Ceil(desired))
}

// Stats returns runtime statistics for the worker pool.
func (p *DynamicWorkerPool) Stats() WorkerPoolStats {
	mean, cv, samples := p.serviceMetrics.snapshot()
	models := [...]string{"awaiting_observations", "erlang_c_allen_cunneen", "little_law_fallback", "slo_unattainable"}
	pending := int(p.pendingResults.Load())
	return WorkerPoolStats{
		SLOCondition: p.sloCondition(),
		ServiceTime:  time.Duration(mean * float64(time.Second)), ServiceCV: cv, ServiceSamples: samples, SizingModel: models[p.sizingModel.Load()],
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
		PendingResults:  pending,
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

// SetArrivalRate feeds the autoscaler the job arrival rate (lambda, jobs/sec).
// When set (>0), desiredCapacity sizes the pool from lambda instead of the
// throttled enqueue rate, so the pool can scale up even when the queue is empty.
func (p *DynamicWorkerPool) SetArrivalRate(lambda float64) {
	p.arrivalRate.Store(math.Float64bits(lambda))
}

func (p *DynamicWorkerPool) getArrivalRate() float64 {
	return math.Float64frombits(p.arrivalRate.Load())
}

// SetServiceTime feeds the autoscaler the per-job service time (tau, seconds).
// It is a startup estimate; observed execution times take precedence.
func (p *DynamicWorkerPool) SetServiceTime(tau float64) {
	p.serviceTime.Store(math.Float64bits(tau))
}

func (p *DynamicWorkerPool) getServiceTime() float64 {
	return math.Float64frombits(p.serviceTime.Load())
}

// SetTargetWorkers explicitly sets the worker pool capacity to n (clamped to
// the configured min/max) and records the target. It is used to apply the
// M/M/c-derived initial sizing computed at startup.
func (p *DynamicWorkerPool) SetTargetWorkers(n int) {
	minW, maxW := p.config.MinWorkers, p.config.MaxWorkers
	if maxW < minW {
		maxW = minW
	}
	if n < minW {
		n = minW
	}
	if n > maxW {
		n = maxW
	}
	if p.antsPool != nil {
		p.antsPool.Tune(n)
	}
	p.lastTarget.Store(int64(n))
	p.lastScaleTime.Store(time.Now().UnixNano())
	if p.logger != nil {
		p.logger.Printf("Set target workers to %d", n)
	}
}

// ReplaceQueue replaces the current queue with a new one.
// This is used for dynamic queue switching (e.g., from Workiva to Adaptive).
// Any jobs still in the old queue are drained and re-enqueued into the new
// queue so no in-flight work is lost.
func (p *DynamicWorkerPool) ReplaceQueue(newQueue Queue) error {
	if newQueue == nil {
		return errors.New("new queue cannot be nil")
	}

	if p.logger != nil {
		p.logger.Println("Replacing queue in worker pool...")
	}

	// Swap the queue reference under the lock so the dispatcher and
	// autoScale goroutines observe a consistent value.
	p.queueMu.Lock()
	oldQueue := p.queue
	p.queue = newQueue
	p.queueMu.Unlock()

	// Drain the old queue and re-enqueue into the new queue (zero data loss).
	// DequeueBatch is atomic, so this races safely with the dispatcher: any
	// job the dispatcher already claimed is submitted to the pool, and any job
	// still in the old queue is re-enqueued here.
	drained := 0
	for {
		batch, err := oldQueue.DequeueBatch(1000)
		if err != nil {
			break
		}
		if len(batch) == 0 {
			break
		}
		for _, job := range batch {
			if err := newQueue.Enqueue(job); err != nil {
				if p.logger != nil {
					p.logger.Printf("Failed to re-enqueue drained job: %v", err)
				}
			}
		}
		drained += len(batch)
	}
	if drained > 0 && p.logger != nil {
		p.logger.Printf("Re-enqueued %d jobs during queue replacement", drained)
	}

	if p.logger != nil {
		p.logger.Println("Queue replacement completed")
	}
	return nil
}
