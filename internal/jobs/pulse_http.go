package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/valyala/fasthttp"
)

// PulseHTTPJob performs HTTP health checks using fasthttp for high performance.
// It supports configurable methods, timeouts, and retry logic.
//
// Safety features:
//   - Uses global dial limiter to prevent CPU spikes during outages
//   - Checks context cancellation before each retry
//   - Uses pre-allocated payloads to reduce allocations
//   - Uses fasthttp request/response pools
type PulseHTTPJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	URL         string
	Method      string
	JobType     string
	Driver      string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	// Host and IsTLS are pre-computed for fasthttp client selection
	Host  string
	IsTLS bool
}

// Execute performs the HTTP health check with retries.
func (p *PulseHTTPJob) Execute(ctx context.Context) Result {
	attempts := p.Retries + 1
	// Use pre-allocated payload to reduce allocations
	payload := GetPulseHTTPPayload()

	// Acquire global dial slot to prevent CPU spikes during network outages.
	// This is critical when 100k+ monitors become unreachable simultaneously.
	if !AcquireHTTPDialSlot(ctx) {
		return Result{
			Ent:     p.Entity,
			Err:     ErrDialLimiterTimeout,
			Payload: payload,
		}
	}
	defer ReleaseHTTPDialSlot()

	// Get the fasthttp client for this host (cached per-host)
	client := fasthttpClients.Get(p.Host, p.IsTLS)

	// Acquire request/response from pool
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set up the request once (reused across retries)
	req.SetRequestURI(p.URL)
	req.Header.SetMethod(p.Method)

	for i := 0; i < attempts; i++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return Result{Ent: p.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		// Reset response for reuse
		resp.Reset()

		err := client.DoTimeout(req, resp, p.Timeout)
		if err == nil {
			statusCode := resp.StatusCode()
			if statusCode >= 200 && statusCode < 300 {
				return Result{Ent: p.Entity, Err: nil, Payload: payload}
			}
			// Non-2xx status code: continue to next retry
		}

		// Brief pause before retry (don't block on last attempt)
		if i < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: p.Entity, Err: ErrHTTPCheckFailed, Payload: payload}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (p *PulseHTTPJob) Copy() Job { job := *p; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (p *PulseHTTPJob) GetEnqueueTime() time.Time { return p.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (p *PulseHTTPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (p *PulseHTTPJob) GetStartTime() time.Time { return p.StartTime }

// SetStartTime sets when the job started executing.
func (p *PulseHTTPJob) SetStartTime(t time.Time) { p.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (p *PulseHTTPJob) IsNil() bool { return p == nil }
