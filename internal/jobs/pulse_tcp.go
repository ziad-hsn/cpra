package jobs

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// PulseTCPJob performs TCP connection health checks.
// It attempts to establish a TCP connection to verify port availability.
//
// Safety features:
//   - Uses global dial limiter via acquireTCPSlot()
//   - Checks context cancellation between retries
//   - Uses pre-allocated payloads to reduce allocations
//   - Uses optimized TCP dialer with SO_REUSEADDR
type PulseTCPJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	JobType     string
	Driver      string
	Port        int
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

// Execute performs the TCP connection check with retries.
func (p *PulseTCPJob) Execute(ctx context.Context) Result {
	// Use pre-allocated payload to reduce allocations
	payload := GetPulseTCPPayload()
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	// Acquire TCP connection slot to limit concurrent dials
	if !acquireTCPSlot(ctx, p.Timeout) {
		return Result{
			Ent:     p.Entity,
			Err:     ErrSemaphoreTimeout,
			Payload: payload,
		}
	}
	defer releaseTCPSlot()

	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	for attempt := 0; attempt < attempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return Result{Ent: p.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		// Use an optimized dialer instead of net.DialTimeout
		conn, err := DialTCP(ctx, address, p.Timeout)
		if err == nil {
			// Don't set a deadline, just close immediately for a health check
			_ = conn.Close()
			return Result{Ent: p.Entity, Err: nil, Payload: payload}
		}

		// Brief pause before retry (don't block on last attempt)
		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	return Result{
		Ent:     p.Entity,
		Err:     ErrTCPCheckFailed,
		Payload: payload,
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (p *PulseTCPJob) Copy() Job { job := *p; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (p *PulseTCPJob) GetEnqueueTime() time.Time { return p.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (p *PulseTCPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (p *PulseTCPJob) GetStartTime() time.Time { return p.StartTime }

// SetStartTime sets when the job started executing.
func (p *PulseTCPJob) SetStartTime(t time.Time) { p.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (p *PulseTCPJob) IsNil() bool { return p == nil }
