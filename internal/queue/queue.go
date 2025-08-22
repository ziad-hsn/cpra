package queue

import (
	"context"
	"cpra/internal/jobs"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/panjf2000/ants/v2"
	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
)

// QueueManager combines K8s workqueue (deduplication) with ants pool (execution)
type QueueManager struct {
	// Workqueues for deduplication and rate limiting
	pulseQueue        workqueue.TypedRateLimitingInterface[string]
	interventionQueue workqueue.TypedRateLimitingInterface[string]
	codeQueue         workqueue.TypedRateLimitingInterface[string]

	// Ants pools for efficient execution
	pulsePool        *ants.Pool
	interventionPool *ants.Pool
	codePool         *ants.Pool

	// Result channels
	pulseResults        chan jobs.Result
	interventionResults chan jobs.Result
	codeResults         chan jobs.Result

	// Job storage for retrieval after dequeue
	jobStore sync.Map // key: string(entityID-jobType), value: jobs.Job

	// Metrics
	metrics *QueueMetrics
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type QueueMetrics struct {
	pulsesQueued        atomic.Int64
	pulsesProcessed     atomic.Int64
	interventionsQueued atomic.Int64
	codesSent           atomic.Int64
	queueDepth          atomic.Int64
	droppedJobs         atomic.Int64
}

// JobKey creates a unique key for deduplication
type JobKey struct {
	Entity  ecs.Entity
	JobType string
}

func (k JobKey) String() string {
	return fmt.Sprintf("%d-%s", k.Entity.ID(), k.JobType)
}

// customRateLimiter implements a token bucket rate limiter
type customRateLimiter struct {
	limiter *rate.Limiter
}

func (r *customRateLimiter) When(item string) time.Duration {
	return r.limiter.Reserve().Delay()
}

func (r *customRateLimiter) Forget(item string) {}

func (r *customRateLimiter) NumRequeues(item string) int {
	return 0
}

// NewQueueManager creates an optimized queue manager based on Little's Law
func NewQueueManager(monitorCount int) (*QueueManager, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Calculate worker counts using blocking coefficient from research
	// For I/O bound: Workers = Cores * (1 + Wait/Service)
	// Assuming Wait=100ms, Service=5ms → coefficient=20
	cores := getOptimalCores()
	blockingCoefficient := 20
	baseWorkers := cores * blockingCoefficient

	// Scale workers based on monitor count (Little's Law)
	// L = λ * W where L=workers, λ=arrival rate, W=avg response time
	pulseWorkers := calculateWorkers(monitorCount, baseWorkers, "pulse")
	interventionWorkers := calculateWorkers(monitorCount/10, baseWorkers/4, "intervention")
	codeWorkers := calculateWorkers(monitorCount/5, baseWorkers/2, "code")

	// Create rate limiters using golang.org/x/time/rate
	pulseLimiter := &customRateLimiter{
		limiter: rate.NewLimiter(rate.Limit(monitorCount), monitorCount*2),
	}

	interventionLimiter := &customRateLimiter{
		limiter: rate.NewLimiter(rate.Limit(monitorCount/10), monitorCount/5),
	}

	codeLimiter := &customRateLimiter{
		limiter: rate.NewLimiter(rate.Limit(monitorCount/5), monitorCount/2),
	}

	// Create workqueues with custom rate limiters using the recommended approach
	pulseQueue := workqueue.NewTypedRateLimitingQueueWithConfig(
		pulseLimiter,
		workqueue.TypedRateLimitingQueueConfig[string]{
			Name: "pulse-queue",
		},
	)

	interventionQueue := workqueue.NewTypedRateLimitingQueueWithConfig(
		interventionLimiter,
		workqueue.TypedRateLimitingQueueConfig[string]{
			Name: "intervention-queue",
		},
	)

	codeQueue := workqueue.NewTypedRateLimitingQueueWithConfig(
		codeLimiter,
		workqueue.TypedRateLimitingQueueConfig[string]{
			Name: "code-queue",
		},
	)

	// Create ants pools with pre-allocated workers and non-blocking mode
	pulsePool, err := ants.NewPool(
		pulseWorkers,
		ants.WithNonblocking(true),              // Don't block producers
		ants.WithPreAlloc(true),                 // Pre-allocate workers
		ants.WithMaxBlockingTasks(monitorCount), // Buffer for bursts
		ants.WithPanicHandler(panicHandler),
	)
	if err != nil {
		cancel() // Fix context leak
		return nil, fmt.Errorf("failed to create pulse pool: %w", err)
	}

	interventionPool, err := ants.NewPool(
		interventionWorkers,
		ants.WithNonblocking(true),
		ants.WithPreAlloc(true),
		ants.WithMaxBlockingTasks(monitorCount/10),
		ants.WithPanicHandler(panicHandler),
	)
	if err != nil {
		cancel() // Fix context leak
		pulsePool.Release()
		return nil, fmt.Errorf("failed to create intervention pool: %w", err)
	}

	codePool, err := ants.NewPool(
		codeWorkers,
		ants.WithNonblocking(true),
		ants.WithPreAlloc(true),
		ants.WithMaxBlockingTasks(monitorCount/5),
		ants.WithPanicHandler(panicHandler),
	)
	if err != nil {
		cancel() // Fix context leak
		pulsePool.Release()
		interventionPool.Release()
		return nil, fmt.Errorf("failed to create code pool: %w", err)
	}

	// Result channels sized per Little's Law
	resultBufferSize := calculateResultBuffer(monitorCount)

	qm := &QueueManager{
		pulseQueue:          pulseQueue,
		interventionQueue:   interventionQueue,
		codeQueue:           codeQueue,
		pulsePool:           pulsePool,
		interventionPool:    interventionPool,
		codePool:            codePool,
		pulseResults:        make(chan jobs.Result, resultBufferSize),
		interventionResults: make(chan jobs.Result, resultBufferSize/10),
		codeResults:         make(chan jobs.Result, resultBufferSize/5),
		metrics:             &QueueMetrics{},
		ctx:                 ctx,
		cancel:              cancel,
	}

	// Start queue processors
	qm.startProcessors()

	return qm, nil
}

// EnqueuePulse adds a pulse job with deduplication
func (qm *QueueManager) EnqueuePulse(entity ecs.Entity, job jobs.Job) error {
	key := JobKey{Entity: entity, JobType: "pulse"}

	// Store job for later retrieval
	qm.jobStore.Store(key.String(), job)

	// Add to queue (deduplicates automatically)
	qm.pulseQueue.Add(key.String())
	qm.metrics.pulsesQueued.Add(1)

	return nil
}

// EnqueueIntervention adds an intervention job with deduplication
func (qm *QueueManager) EnqueueIntervention(entity ecs.Entity, job jobs.Job) error {
	key := JobKey{Entity: entity, JobType: "intervention"}
	qm.jobStore.Store(key.String(), job)
	qm.interventionQueue.Add(key.String())
	qm.metrics.interventionsQueued.Add(1)
	return nil
}

// EnqueueCode adds a code notification job
func (qm *QueueManager) EnqueueCode(entity ecs.Entity, job jobs.Job) error {
	key := JobKey{Entity: entity, JobType: "code"}
	qm.jobStore.Store(key.String(), job)
	qm.codeQueue.Add(key.String())
	return nil
}

// startProcessors launches goroutines to process queues
func (qm *QueueManager) startProcessors() {
	// Pulse processor
	qm.wg.Add(1)
	go func() {
		defer qm.wg.Done()
		qm.processQueue(qm.pulseQueue, qm.pulsePool, qm.pulseResults, "pulse")
	}()

	// Intervention processor
	qm.wg.Add(1)
	go func() {
		defer qm.wg.Done()
		qm.processQueue(qm.interventionQueue, qm.interventionPool, qm.interventionResults, "intervention")
	}()

	// Code processor
	qm.wg.Add(1)
	go func() {
		defer qm.wg.Done()
		qm.processQueue(qm.codeQueue, qm.codePool, qm.codeResults, "code")
	}()
}

// processQueue pulls from workqueue and submits to ants pool
func (qm *QueueManager) processQueue(
	queue workqueue.TypedRateLimitingInterface[string],
	pool *ants.Pool,
	results chan jobs.Result,
	queueType string,
) {
	for {
		// Check for shutdown
		select {
		case <-qm.ctx.Done():
			return
		default:
		}

		// Get next item (blocks until available)
		key, shutdown := queue.Get()
		if shutdown {
			return
		}

		// Process the item
		qm.processItem(key, queue, pool, results, queueType)
	}
}

func (qm *QueueManager) processItem(
	key string,
	queue workqueue.TypedRateLimitingInterface[string],
	pool *ants.Pool,
	results chan jobs.Result,
	queueType string,
) {
	// Ensure we mark item as done
	defer queue.Done(key)

	// Retrieve job from store
	jobInterface, ok := qm.jobStore.Load(key)
	if !ok {
		// Job not found, forget it
		queue.Forget(key)
		return
	}

	job := jobInterface.(jobs.Job)

	// Submit to ants pool (non-blocking)
	err := pool.Submit(func() {
		// Execute job
		result := job.Execute()

		// Send result (non-blocking)
		select {
		case results <- result:
			if queueType == "pulse" {
				qm.metrics.pulsesProcessed.Add(1)
			}
		default:
			// Result channel full, log and continue
			qm.metrics.droppedJobs.Add(1)
		}

		// Clean up job from store
		qm.jobStore.Delete(key)
	})

	if err != nil {
		// Pool at capacity, requeue with backoff
		queue.AddRateLimited(key)
	} else {
		// Success, forget the item
		queue.Forget(key)
	}
}

// GetChannels returns the result channels for system integration
func (qm *QueueManager) GetChannels() (pulse, intervention, code chan jobs.Result) {
	return qm.pulseResults, qm.interventionResults, qm.codeResults
}

// GetMetrics returns current queue metrics
func (qm *QueueManager) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"pulsesQueued":        qm.metrics.pulsesQueued.Load(),
		"pulsesProcessed":     qm.metrics.pulsesProcessed.Load(),
		"interventionsQueued": qm.metrics.interventionsQueued.Load(),
		"codesSent":           qm.metrics.codesSent.Load(),
		"droppedJobs":         qm.metrics.droppedJobs.Load(),
		"pulseQueueDepth":     qm.pulseQueue.Len(),
		"pulsePoolRunning":    qm.pulsePool.Running(),
		"pulsePoolFree":       qm.pulsePool.Free(),
	}
}

// Shutdown gracefully stops all queues and pools
func (qm *QueueManager) Shutdown() {
	// Signal shutdown
	qm.cancel()

	// Shutdown queues
	qm.pulseQueue.ShutDown()
	qm.interventionQueue.ShutDown()
	qm.codeQueue.ShutDown()

	// Wait for processors to finish
	qm.wg.Wait()

	// Release pools
	qm.pulsePool.Release()
	qm.interventionPool.Release()
	qm.codePool.Release()

	// Close result channels
	close(qm.pulseResults)
	close(qm.interventionResults)
	close(qm.codeResults)
}

// Helper functions

func getOptimalCores() int {
	cores := runtime.NumCPU()
	if cores < 4 {
		return 4 // Minimum for adequate concurrency
	}
	return cores
}

func calculateWorkers(monitorCount, base int, jobType string) int {
	// Apply Little's Law: Workers = ArrivalRate * ResponseTime
	var avgResponseTime float64

	switch jobType {
	case "pulse":
		avgResponseTime = 0.2 // 200ms average for HTTP checks
	case "intervention":
		avgResponseTime = 1.0 // 1s for Docker operations
	case "code":
		avgResponseTime = 0.1 // 100ms for notifications
	default:
		avgResponseTime = 0.2
	}

	arrivalRate := float64(monitorCount) // jobs per second
	optimalWorkers := int(arrivalRate * avgResponseTime)

	// Apply bounds
	if optimalWorkers < base {
		return base
	}
	if optimalWorkers > 1000 {
		return 1000 // Cap at 1000 to prevent resource exhaustion
	}

	return optimalWorkers
}

func calculateResultBuffer(monitorCount int) int {
	// Size to handle burst of all monitors failing simultaneously
	buffer := monitorCount * 3
	if buffer > 100000 {
		return 100000 // Cap at 100k for memory efficiency
	}
	if buffer < 10000 {
		return 10000 // Minimum buffer
	}
	return buffer
}

func panicHandler(err interface{}) {
	fmt.Printf("Worker panic recovered: %v\n", err)
}
