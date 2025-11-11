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
	payload := GetPulseTCPPayload()

	// Acquire TCP connection slot to limit concurrent dials
	if !acquireTCPSlot(ctx, p.Timeout) {
		return Result{Ent: p.Entity, Err: ErrSemaphoreTimeout, Payload: payload}
	}
	defer releaseTCPSlot()

	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	err := RetryWithBackoff(ctx, p.Retries+1, 50*time.Millisecond, func() error {
		conn, dialErr := DialTCP(ctx, address, p.Timeout)
		if dialErr != nil {
			return dialErr
		}
		_ = conn.Close()
		return nil
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: p.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: p.Entity, Err: ErrTCPCheckFailed, Payload: payload}
	}
	return Result{Ent: p.Entity, Err: nil, Payload: payload}
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
