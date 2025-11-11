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
	payload := GetPulseHTTPPayload()

	// Acquire global dial slot to prevent CPU spikes during network outages.
	if !AcquireHTTPDialSlot(ctx) {
		return Result{Ent: p.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
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

	err := RetryWithBackoff(ctx, p.Retries+1, 50*time.Millisecond, func() error {
		resp.Reset()
		if httpErr := client.DoTimeout(req, resp, p.Timeout); httpErr != nil {
			return httpErr
		}
		if statusCode := resp.StatusCode(); statusCode < 200 || statusCode >= 300 {
			return ErrHTTPCheckFailed // Non-2xx triggers retry
		}
		return nil
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: p.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: p.Entity, Err: ErrHTTPCheckFailed, Payload: payload}
	}
	return Result{Ent: p.Entity, Err: nil, Payload: payload}
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
