package queue

import (
	"cpra/internal/runtime/jobs"
	"log"
	"sync/atomic"
	"time"
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

// TapPulseResults creates a tee of the pulse results channel for non-intrusive fan-out.
// Returns two channels: primary (for main consumer) and tap (for metrics/tracing).
// Both channels close when done closes or the source channel is exhausted.
//
// Example:
//
//	done := jobs.Or(ctx.Done(), stopCh)
//	pulseMain, pulseMetrics := router.TapPulseResults(done)
//	go consumeResults(pulseMain)      // existing consumer
//	go sampleMetrics(pulseMetrics)    // lightweight observer
func (r *ResultRouter) TapPulseResults(done <-chan struct{}) (<-chan []jobs.Result, <-chan []jobs.Result) {
	return jobs.Tee(done, r.PulseResultChan)
}

// TapInterventionResults creates a tee of the intervention results channel.
// See TapPulseResults for usage.
func (r *ResultRouter) TapInterventionResults(done <-chan struct{}) (<-chan []jobs.Result, <-chan []jobs.Result) {
	return jobs.Tee(done, r.InterventionResultChan)
}

// TapCodeResults creates a tee of the code results channel.
// See TapPulseResults for usage.
func (r *ResultRouter) TapCodeResults(done <-chan struct{}) (<-chan []jobs.Result, <-chan []jobs.Result) {
	return jobs.Tee(done, r.CodeResultChan)
}
