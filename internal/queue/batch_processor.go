package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/jobs"
)

// BatchProcessorLogger interface for structured logging
type BatchProcessorLogger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// BatchProcessor handles high-throughput job processing with batching
type BatchProcessor struct {
	// Core components
	queue    *BoundedQueue
	connPool *ConnectionPool

	// Processing configuration
	batchSize     int
	maxConcurrent int
	timeout       time.Duration

	// Statistics
	processed     int64
	failed        int64
	totalDuration int64

	// State management
	running int32

	// Logging
	logger BatchProcessorLogger

	// Result channels
	pulseResults        chan jobs.Result
	interventionResults chan jobs.Result
	codeResults         chan jobs.Result
}

// ProcessorConfig holds batch processor configuration
type ProcessorConfig struct {
	BatchSize     int           // Jobs per batch
	MaxConcurrent int           // Maximum concurrent batches
	Timeout       time.Duration // Processing timeout
	RetryAttempts int           // Retry attempts for failed jobs
	RetryDelay    time.Duration // Delay between retries
}

// ProcessorStats holds processing statistics
type ProcessorStats struct {
	Processed   int64
	Failed      int64
	AverageTime time.Duration
	Throughput  float64 // jobs per second
	QueueDepth  int32
}

// NewBatchProcessor creates a new batch processor
func NewBatchProcessor(queue *BoundedQueue, connPool *ConnectionPool, config ProcessorConfig, logger BatchProcessorLogger, pulseResults chan jobs.Result, interventionResults chan jobs.Result, codeResults chan jobs.Result) *BatchProcessor {
	return &BatchProcessor{
		queue:               queue,
		connPool:            connPool,
		batchSize:           config.BatchSize,
		maxConcurrent:       config.MaxConcurrent,
		timeout:             config.Timeout,
		logger:              logger,
		pulseResults:        pulseResults,
		interventionResults: interventionResults,
		codeResults:         codeResults,
	}
}

// Start starts the batch processor
func (bp *BatchProcessor) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&bp.running, 0, 1) {
		return fmt.Errorf("processor already running")
	}

	bp.logger.Info("Starting batch processor with %d max concurrent batches", bp.maxConcurrent)

	// Start processing goroutines
	for i := 0; i < bp.maxConcurrent; i++ {
		go bp.processingLoop(ctx, i)
	}

	return nil
}

// Stop stops the batch processor
func (bp *BatchProcessor) Stop() {
	atomic.StoreInt32(&bp.running, 0)
}

// processingLoop is the main processing loop for each worker
func (bp *BatchProcessor) processingLoop(ctx context.Context, workerID int) {
	for atomic.LoadInt32(&bp.running) == 1 {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get a batch from the queue
		batch, err := bp.queue.DequeueBatch(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Process the batch
		bp.processBatch(ctx, batch, workerID)
	}
}

// processBatch processes a single batch of jobs
func (bp *BatchProcessor) processBatch(ctx context.Context, batch []jobs.Job, workerID int) {
	if len(batch) == 0 {
		return
	}

	startTime := time.Now()

	// Create a context with timeout for this batch
	batchCtx, cancel := context.WithTimeout(ctx, bp.timeout)
	defer cancel()

	// Process jobs in parallel within the batch
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 1000) // Limit concurrent jobs per batch

	successCount := int64(0)
	failureCount := int64(0)

	for i, job := range batch {
		wg.Add(1)
		go func(j jobs.Job, jobIndex int) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-batchCtx.Done():
				atomic.AddInt64(&failureCount, 1)
				return
			}

			// Execute the job with timing
			startTime := time.Now()
			j.SetStartTime(startTime)
			result := j.Execute()
			endTime := time.Now()
			
			// Calculate job latency and update queue statistics
			jobLatency := endTime.Sub(startTime)
			bp.queue.UpdateJobLatency(jobLatency)
			
			if result.Err != nil {
				atomic.AddInt64(&failureCount, 1)
				bp.logger.Warn("Worker %d: Job %d failed after %v: %v", workerID, jobIndex, jobLatency, result.Err)
			} else {
				atomic.AddInt64(&successCount, 1)
			}

			// Publish result to the appropriate channel (non-blocking)
			switch j.(type) {
			case *jobs.PulseHTTPJob, *jobs.PulseTCPJob, *jobs.PulseICMPJob:
				select {
				case bp.pulseResults <- result:
				default:
				}
			case *jobs.InterventionDockerJob:
				select {
				case bp.interventionResults <- result:
				default:
				}
			case *jobs.CodeLogJob, *jobs.CodeSlackJob, *jobs.CodePagerDutyJob:
				select {
				case bp.codeResults <- result:
				default:
				}
			default:
				select {
				case bp.pulseResults <- result:
				default:
				}
			}
		}(job, i)
	}

	wg.Wait()

	// Update statistics
	duration := time.Since(startTime)
	atomic.AddInt64(&bp.processed, successCount)
	atomic.AddInt64(&bp.failed, failureCount)
	atomic.AddInt64(&bp.totalDuration, int64(duration))

	if len(batch) > 0 {
		bp.logger.Debug("Worker %d: Processed batch of %d jobs (%d success, %d failed) in %v",
			workerID, len(batch), successCount, failureCount, duration.Truncate(time.Millisecond))
	}
}

// Stats returns current processing statistics
func (bp *BatchProcessor) Stats() ProcessorStats {

	processed := atomic.LoadInt64(&bp.processed)
	failed := atomic.LoadInt64(&bp.failed)
	totalDuration := atomic.LoadInt64(&bp.totalDuration)

	var avgTime time.Duration
	var throughput float64

	if processed > 0 {
		avgTime = time.Duration(totalDuration / processed)
		throughput = float64(processed) / (float64(totalDuration) / float64(time.Second))
	}

	queueStats := bp.queue.Stats()

	return ProcessorStats{
		Processed:   processed,
		Failed:      failed,
		AverageTime: avgTime,
		Throughput:  throughput,
		QueueDepth:  queueStats.QueueDepth,
	}
}

// IsRunning returns true if the processor is currently running
func (bp *BatchProcessor) IsRunning() bool {
	return atomic.LoadInt32(&bp.running) == 1
}
