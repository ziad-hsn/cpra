package queue

import (
	"context"
	"cpra/internal/jobs"
	"log"
	"sync"
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

	// Send to appropriate channels (non-blocking)
	if len(pulseResults) > 0 {
		select {
		case r.PulseResultChan <- pulseResults:
		default:
			r.logger.Printf("Warning: PulseResultChan full, dropping %d results", len(pulseResults))
		}
	}
	if len(interventionResults) > 0 {
		select {
		case r.InterventionResultChan <- interventionResults:
		default:
			r.logger.Printf("Warning: InterventionResultChan full, dropping %d results", len(interventionResults))
		}
	}
	if len(codeResults) > 0 {
		select {
		case r.CodeResultChan <- codeResults:
		default:
			r.logger.Printf("Warning: CodeResultChan full, dropping %d results", len(codeResults))
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
}

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
	MinWorkers         int
	MaxWorkers         int
	AdjustmentInterval time.Duration
	ResultBatchSize    int
	ResultBatchTimeout time.Duration
}

// DefaultWorkerPoolConfig returns a default configuration for the worker pool.
func DefaultWorkerPoolConfig() WorkerPoolConfig {
	return WorkerPoolConfig{
		MinWorkers:         10,
		MaxWorkers:         1000,
		AdjustmentInterval: 5 * time.Second,
		ResultBatchSize:    100,
		ResultBatchTimeout: 10 * time.Millisecond,
	}
}

// NewDynamicWorkerPool creates a new dynamic worker pool.
func NewDynamicWorkerPool(q Queue, config WorkerPoolConfig, logger *log.Logger) (*DynamicWorkerPool, error) {
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
			pool.logger.Printf("Error: Invalid job type in worker pool: %T", job)
			return
		}
		result := j.Execute()
		pool.resultChan <- result
	}

	antsPool, err := ants.NewPoolWithFunc(config.MinWorkers, workerFunc, ants.WithPanicHandler(func(err interface{}) {
		pool.logger.Printf("Worker panic: %v", err)
	}))
	if err != nil {
		return nil, err
	}
	pool.antsPool = antsPool

	return pool, nil
}

// Start begins the worker pool's operations.
func (p *DynamicWorkerPool) Start() {
	p.wg.Add(2)
	go p.dispatcher()
	go p.resultProcessor()
	p.logger.Println("DynamicWorkerPool started")
}

// GetRouter returns the result router for accessing type-specific result channels.
func (p *DynamicWorkerPool) GetRouter() *ResultRouter {
	return p.router
}

// Stop gracefully shuts down the worker pool.
func (p *DynamicWorkerPool) Stop() {
	p.logger.Println("Stopping DynamicWorkerPool...")
	p.cancel()
	p.wg.Wait()
	p.antsPool.Release()
	close(p.resultChan)
	p.router.Close()
	p.logger.Println("DynamicWorkerPool stopped")
}

// dispatcher fetches batches of jobs from the queue and submits them to the ants pool.
func (p *DynamicWorkerPool) dispatcher() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			// Dequeue a batch of jobs. The maxSize can be tuned.
			jobs, err := p.queue.DequeueBatch(p.config.MaxWorkers) // Using MaxWorkers as batch size for now
			if err != nil {
				if err != ErrQueueClosed {
					p.logger.Printf("Error dequeuing job batch: %v", err)
				}
				time.Sleep(100 * time.Millisecond) // Wait a bit if there's an error
				continue
			}
			if len(jobs) == 0 {
				time.Sleep(10 * time.Millisecond) // Wait if the queue is empty
				continue
			}

			for _, job := range jobs {
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
