package queue

import (
	"context"
	"errors"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"math"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/puzpuzpuz/xsync/v4"
)

// Logger is the diagnostic interface shared with the worker-pool dependency.
// Standard-library and application loggers both satisfy it.
type Logger interface {
	Printf(string, ...interface{})
}

// optionalLogger preserves the nil-pointer behavior of the former *log.Logger
// argument when a caller supplies a typed nil through the interface.
func optionalLogger(logger Logger) Logger {
	if logger == nil {
		return nil
	}
	value := reflect.ValueOf(logger)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil
		}
	}
	return logger
}

// ResultRouter handles routing of job results to type-specific channels.
// This enables decoupling result processing from the main worker pool.
type ResultRouter struct {
	PulseResultChan        chan []jobs.Result
	InterventionResultChan chan []jobs.Result
	CodeResultChan         chan []jobs.Result
	logger                 Logger
	config                 WorkerPoolConfig
	done                   <-chan struct{}
	wakeup                 chan<- struct{}
	closeOnce              sync.Once
}

// WorkerPoolStats exposes runtime metrics for the dynamic worker pool.
type WorkerPoolStats struct {
	RequestedCapacity   int           `json:"requested_capacity"`
	ReservedCapacity    int           `json:"reserved_capacity"`
	DrainingWorkers     int           `json:"draining_workers"`
	CapacityCondition   string        `json:"capacity_condition"`
	ActiveExecutions    int           `json:"active_executions"`
	OldestExecutionAge  time.Duration `json:"oldest_execution_age"`
	PendingSubmissions  int           `json:"pending_submissions"`
	OldestSubmissionAge time.Duration `json:"oldest_submission_age"`
	Attempts            int64         `json:"attempts"`
	AttemptTimeouts     int64         `json:"attempt_timeouts"`
	AttemptDuration     time.Duration `json:"attempt_duration"`
	RetryDelay          time.Duration `json:"retry_delay"`
	SLOCondition        string        `json:"slo_condition"`
	ServiceTime         time.Duration `json:"service_time"`
	ServiceCV           float64       `json:"service_cv"`
	ServiceSamples      int           `json:"service_samples"`
	ArrivalRate         float64       `json:"arrival_rate"`
	SizingModel         string        `json:"sizing_model"`
	LastScaleTime       time.Time     `json:"last_scale_time,omitzero"`
	MinWorkers          int           `json:"min_workers"`
	MaxWorkers          int           `json:"max_workers"`
	CurrentCapacity     int           `json:"current_capacity"`
	RunningWorkers      int           `json:"running_workers"`
	WaitingTasks        int           `json:"waiting_tasks"`
	TargetWorkers       int           `json:"target_workers"`
	TasksSubmitted      int64         `json:"tasks_submitted"`
	TasksCompleted      int64         `json:"tasks_completed"`
	ScalingEvents       int64         `json:"scaling_events"`
	PendingResults      int           `json:"pending_results"`
}

// NewResultRouter creates a new result router with buffered channels.
func NewResultRouter(config WorkerPoolConfig, logger Logger) *ResultRouter {
	logger = optionalLogger(logger)
	bufferSize := config.ResultChannelDepth
	return &ResultRouter{
		PulseResultChan:        make(chan []jobs.Result, bufferSize),
		InterventionResultChan: make(chan []jobs.Result, bufferSize),
		CodeResultChan:         make(chan []jobs.Result, bufferSize),
		config:                 config,
		logger:                 logger,
	}
}

// SetWakeup connects a coalescing owner hint before the pool starts. The router
// never closes the channel; the owner must keep it open until all pools stop.
func (r *ResultRouter) SetWakeup(wakeup chan<- struct{}) { r.wakeup = wakeup }

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
			if r.logger != nil {
				r.logger.Printf("Unknown job type in result: %v", resultType(result))
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
	backoff := r.config.ResultBatchTimeout
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	ticker := time.NewTicker(backoff)
	defer ticker.Stop()

	for {
		select {
		case ch <- batch:
			select {
			case r.wakeup <- struct{}{}:
			default:
			}
			return
		case <-r.done:
			return
		case <-ticker.C:
			if r.logger != nil {
				r.logger.Printf("Backpressure: %s results stalled (%d jobs waiting)", label, len(batch))
			}
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
	activeExecutions   pendingAges
	pendingSubmissions pendingAges
	feedback           *feedback
	serviceMetrics     durationMetrics
	sizingPolicy       atomic.Pointer[sizingPolicy]
	sizingModel        atomic.Uint32
	queueMu            sync.RWMutex
	queue              Queue
	ctx                context.Context
	cancel             context.CancelFunc
	antsPool           *ants.PoolWithFunc
	logger             Logger
	resultQueues       []*xsync.MPMCQueue[jobs.Result]
	notifyChans        []chan struct{}
	numShards          int
	pendingResults     atomic.Int64
	router             *ResultRouter
	config             WorkerPoolConfig
	wg                 sync.WaitGroup
	workerWG           sync.WaitGroup
	dispatcherDone     chan struct{}
	stopped            chan struct{}
	workCtx            context.Context
	workCancel         context.CancelFunc
	tasksSubmitted     atomic.Int64
	tasksCompleted     atomic.Int64
	scalingEvents      atomic.Int64
	lastTarget         atomic.Int64
	requestedCapacity  atomic.Int64
	lastScaleTime      atomic.Int64
	stopping           atomic.Int32
	started            atomic.Bool
	resultBatchPool    sync.Pool

	// arrivalRate is the externally-provided job arrival rate (lambda), stored
	// as float64 bits. When set (>0), the autoscaler sizes the pool from it
	// instead of the throttled enqueue rate, so it can scale up even when the
	// queue is empty.
	arrivalRate     atomic.Uint64
	arrivalRateSet  atomic.Bool
	attempts        atomic.Int64
	attemptTimeouts atomic.Int64
	attemptDuration atomic.Int64
	retryDelay      atomic.Int64

	// serviceTime is the externally-provided per-job service time (tau, seconds),
	// stored as float64 bits. Used until completed jobs provide observations.
	serviceTime atomic.Uint64
}

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
	// FairDispatch reserves driver/flow concurrency before removing pulse work.
	FairDispatch       bool
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
		TargetQueueLatency: 250 * time.Millisecond,
		PreAlloc:           false,
		NonBlocking:        false,
		MaxBlockingTasks:   0,
		ExpiryDuration:     5 * time.Minute,
	}
}

// NewDynamicWorkerPool creates a new dynamic worker pool.
func NewDynamicWorkerPool(q Queue, config WorkerPoolConfig, logger Logger) (*DynamicWorkerPool, error) {
	logger = optionalLogger(logger)
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
		config.TargetQueueLatency = 250 * time.Millisecond
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
		if task, ok := job.(*poolTask); ok {
			job = pool.pendingSubmissions.take(task.pending)
		}
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
		entry := pool.activeExecutions.begin(j)
		pool.activeExecutions.finish(entry, true)
		defer func() {
			if entry != nil {
				pool.activeExecutions.take(entry)
			}
		}()
		var result jobs.Result
		if lease, ok := j.(*fairLease); ok {
			defer func() { lease.complete(result) }()
		}
		result = j.Execute()
		pool.activeExecutions.take(entry)
		entry = nil
		pool.serviceMetrics.observe(time.Since(started))
		pool.attempts.Add(int64(result.Attempts))
		pool.attemptTimeouts.Add(int64(result.AttemptTimeouts))
		pool.attemptDuration.Add(int64(result.AttemptDuration))
		pool.retryDelay.Add(int64(result.RetryDelay))
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
			pool.logger.Printf("Worker panic: job_panicked")
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
	initial := pool.minimumCapacity()
	pool.antsPool.Tune(initial)
	pool.lastTarget.Store(int64(initial))
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
		p.logger.Printf("DynamicWorkerPool started")
	}
}

// GetRouter returns the result router for accessing type-specific result channels.
func (p *DynamicWorkerPool) GetRouter() *ResultRouter {
	return p.router
}

// DrainAndStop waits for outstanding tasks to finish before stopping the worker pool.
// CancelWork cancels operation contexts without dropping buffered results.
// The owner must still join DrainAndStop and consume result routes.
func (p *DynamicWorkerPool) CancelWork() { p.workCancel() }

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
	if p.config.FairDispatch {
		p.dispatchFair()
		return
	}
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
				if !errors.Is(err, ErrQueueClosed) && p.logger != nil {
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
			tasks := make([]*poolTask, 0, len(batch))
			for _, job := range batch {
				entry := p.pendingSubmissions.begin(job)
				p.pendingSubmissions.finish(entry, true)
				tasks = append(tasks, &poolTask{pending: entry})
			}

			for _, job := range tasks {
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
	timer := time.NewTimer(p.config.ResultBatchTimeout)
	timer.Stop()
	defer timer.Stop()

	for {
		// Drain all available results lock-free (no hchan lock).
		for {
			result, ok := q.TryDequeue()
			if !ok {
				break
			}
			p.pendingResults.Add(-1)
			batch = p.handleResult(batch, result, timer)
		}

		// Queue empty. Block for a wakeup, a partial-batch timeout, or shutdown.
		var deadline <-chan time.Time
		if len(batch) > 0 {
			deadline = timer.C
		}
		select {
		case <-p.ctx.Done():
			for {
				result, ok := q.TryDequeue()
				if !ok {
					break
				}
				p.pendingResults.Add(-1)
				batch = p.handleResult(batch, result, timer)
			}
			p.flushResultBatch(batch)
			return
		case <-notify:
			// A worker enqueued; drain again.
		case <-deadline:
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
func (p *DynamicWorkerPool) handleResult(batch []jobs.Result, result jobs.Result, timer *time.Timer) []jobs.Result {
	p.tasksCompleted.Add(1)
	if len(batch) == 0 {
		// Start one flush deadline with the first result. Idle shards wait only
		// for notification/cancellation, and later results cannot defer it.
		timer.Reset(p.config.ResultBatchTimeout)
	}
	batch = append(batch, result)
	if len(batch) >= p.config.ResultBatchSize {
		p.router.RouteResults(batch)
		p.putResultBatch(batch)
		batch = p.getResultBatch()
		timer.Stop()
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
	lastCondition := "available"
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
				pending, _, _ := p.pendingSubmissions.summary()
				queued := stats.QueueDepth + pending
				adjusted := p.feedback.adjust(current, desired, p.config.MaxWorkers, queued, time.Now())
				// Historical SLO breaches cannot require capacity for an empty workload.
				if !p.arrivalRateSet.Load() || p.getArrivalRate() != 0 || queued != 0 {
					desired = adjusted
				}
			}
			if desired != current {
				p.antsPool.Tune(desired)
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(time.Now().UnixNano())
				p.scalingEvents.Add(1)
			}
			state := p.Stats()
			if state.CapacityCondition != lastCondition {
				if p.logger != nil {
					p.logger.Printf("Worker capacity %s: requested=%d supplied=%d active=%d pending=%d", state.CapacityCondition, state.RequestedCapacity, state.CurrentCapacity, state.ActiveExecutions, stats.QueueDepth+state.PendingSubmissions)
				}
				lastCondition = state.CapacityCondition
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
	minimum, maximum := p.minimumCapacity(), p.config.MaxWorkers
	current = max(1, current)
	lambda := p.getArrivalRate()
	active, _, activeMean := p.activeExecutions.summary()
	pending, oldest, _ := p.pendingSubmissions.summary()
	stats.QueueDepth += pending
	stats.OldestPendingAge = max(stats.OldestPendingAge, oldest)
	relief := func(model float64) int {
		model = max(model, float64(p.reservationFloor()))
		if stats.QueueDepth > 0 && stats.OldestPendingAge > p.config.TargetQueueLatency {
			model = max(model, float64(current)*1.25)
		}
		model = max(float64(minimum), model)
		p.requestedCapacity.Store(int64(math.Ceil(min(model, float64(math.MaxInt32)))))
		return int(math.Ceil(min(float64(maximum), min(float64(current)*2, model))))
	}
	if p.arrivalRateSet.Load() && lambda == 0 && stats.QueueDepth == 0 && active == 0 {
		p.sizingModel.Store(sizingWaiting)
		return relief(float64(minimum))
	}
	if !finitePositive(lambda) {
		lambda = stats.EnqueueRate
	}
	if !finitePositive(lambda) {
		lambda = stats.DequeueRate
	}
	if !finitePositive(lambda) {
		p.sizingModel.Store(sizingWaiting)
		return relief(float64(current))
	}
	tau, _, samples := p.serviceMetrics.snapshot()
	if samples == 0 {
		tau = p.getServiceTime()
	}
	// Running checks are censored observations: their elapsed age is a lower
	// bound on occupancy. Do not wait for a hanging batch to finish to scale.
	if active > 0 {
		tau = max(tau, activeMean.Seconds())
	}
	if !finitePositive(tau) && finitePositive(stats.DequeueRate) {
		tau = float64(current) / stats.DequeueRate
	}
	if !finitePositive(tau) {
		p.sizingModel.Store(sizingWaiting)
		return relief(float64(current))
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
	// Configured demand does not erase accumulated backlog after an outage.
	// This can raise the model's target; it never removes its headroom.
	{
		budget := p.config.TargetQueueLatency.Seconds()
		targetDepth := math.Max(float64(minimum), lambda*budget)
		if depth := float64(stats.QueueDepth); depth > targetDepth && targetDepth > 0 {
			desired *= depth / targetDepth
		}
	}
	// Apply bounds before conversion, then limit growth to 2x per adjustment.
	// These constrain actuation; they do not replace the queueing model.
	return relief(desired)
}

// Stats returns runtime statistics for the worker pool.
func (p *DynamicWorkerPool) Stats() WorkerPoolStats {
	mean, cv, samples := p.serviceMetrics.snapshot()
	models := [...]string{"awaiting_observations", "erlang_c_allen_cunneen", "little_law_fallback", "slo_unattainable"}
	pending := int(p.pendingResults.Load())
	active, activeAge, _ := p.activeExecutions.summary()
	submissions, submissionAge, _ := p.pendingSubmissions.summary()
	capacity, running := p.antsPool.Cap(), p.antsPool.Running()
	requested := max(p.config.MinWorkers, int(p.requestedCapacity.Load()), p.reservationFloor())
	condition := "available"
	if requested > p.config.MaxWorkers {
		condition = "worker_limit"
	} else if requested > capacity {
		condition = "growing"
	} else if active+submissions > capacity {
		condition = "draining"
	}
	return WorkerPoolStats{
		RequestedCapacity: requested, ReservedCapacity: p.reservationFloor(), DrainingWorkers: max(0, running-capacity), CapacityCondition: condition,
		ActiveExecutions: active, OldestExecutionAge: activeAge, PendingSubmissions: submissions, OldestSubmissionAge: submissionAge,
		Attempts: p.attempts.Load(), AttemptTimeouts: p.attemptTimeouts.Load(),
		AttemptDuration: time.Duration(p.attemptDuration.Load()), RetryDelay: time.Duration(p.retryDelay.Load()),
		SLOCondition: p.sloCondition(),
		ArrivalRate:  p.getArrivalRate(),
		ServiceTime:  time.Duration(mean * float64(time.Second)), ServiceCV: cv, ServiceSamples: samples, SizingModel: models[p.sizingModel.Load()],
		MinWorkers:      p.config.MinWorkers,
		MaxWorkers:      p.config.MaxWorkers,
		CurrentCapacity: capacity,
		RunningWorkers:  running,
		WaitingTasks:    submissions,
		TargetWorkers:   int(p.lastTarget.Load()),
		TasksSubmitted:  p.tasksSubmitted.Load(),
		TasksCompleted:  p.tasksCompleted.Load(),
		ScalingEvents:   p.scalingEvents.Load(),
		LastScaleTime:   observedTime(p.lastScaleTime.Load()),
		PendingResults:  pending,
	}
}

// Pause temporarily stops the worker pool from processing new tasks.
func (p *DynamicWorkerPool) Pause() {
	if p.logger != nil {
		p.logger.Printf("Pausing worker pool...")
	}
	if p.antsPool != nil {
		p.antsPool.Tune(0) // Reduce capacity to 0 to pause processing
	}
}

// Resume resumes worker pool processing after a pause.
func (p *DynamicWorkerPool) Resume() {
	if p.logger != nil {
		p.logger.Printf("Resuming worker pool...")
	}
	if p.antsPool != nil {
		// Restore to minimum workers
		p.SetTargetWorkers(max(p.config.MinWorkers, int(p.lastTarget.Load())))
	}
}

// SetArrivalRate feeds the autoscaler the job arrival rate (lambda, jobs/sec).
// When set (>0), desiredCapacity sizes the pool from lambda instead of the
// throttled enqueue rate, so the pool can scale up even when the queue is empty.
func (p *DynamicWorkerPool) SetArrivalRate(lambda float64) {
	if lambda < 0 || math.IsNaN(lambda) || math.IsInf(lambda, 0) {
		return
	}
	p.arrivalRate.Store(math.Float64bits(lambda))
	p.arrivalRateSet.Store(true)
}

func (p *DynamicWorkerPool) getArrivalRate() float64 {
	return math.Float64frombits(p.arrivalRate.Load())
}

// SetServiceTime feeds the autoscaler the per-job service time (tau, seconds).
// It is a startup estimate; observed execution times take precedence.
func (p *DynamicWorkerPool) SetServiceTime(tau float64) {
	p.serviceTime.Store(math.Float64bits(tau))
}

// SetDriverDemand changes reservations without actuating capacity. The next
// ordinary evaluation applies the original twofold growth limit.
func (p *DynamicWorkerPool) SetDriverDemand(demand []DriverDemand) error {
	q, ok := p.Queue().(interface{ SetDemand([]DriverDemand) error })
	if !ok {
		return errors.New("worker queue does not support driver demand")
	}
	return q.SetDemand(demand)
}

func (p *DynamicWorkerPool) getServiceTime() float64 {
	return math.Float64frombits(p.serviceTime.Load())
}

func (p *DynamicWorkerPool) reservationFloor() int {
	if p.config.FairDispatch {
		if q, ok := p.Queue().(interface{ requestedCapacity() int }); ok {
			return q.requestedCapacity()
		}
	}
	return 0
}
func (p *DynamicWorkerPool) minimumCapacity() int {
	return p.config.MinWorkers
}

// SetModelWorkers applies the global model recommendation at startup.
func (p *DynamicWorkerPool) SetModelWorkers(n int) {
	p.SetTargetWorkers(n)
}

// SetTargetWorkers explicitly sets the worker pool capacity to n (clamped to
// the configured min/max) and records the target. It is used to apply the
// M/M/c-derived initial sizing computed at startup.
func (p *DynamicWorkerPool) SetTargetWorkers(n int) {
	minW, maxW := p.minimumCapacity(), p.config.MaxWorkers
	n = max(n, p.reservationFloor())
	p.requestedCapacity.Store(int64(n))
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
		p.logger.Printf("Replacing queue in worker pool...")
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
		p.logger.Printf("Queue replacement completed")
	}
	return nil
}
