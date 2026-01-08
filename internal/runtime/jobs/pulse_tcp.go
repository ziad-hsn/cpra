package jobs

import (
	"context"
	"net"
	"strconv"
	"time"
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
	BaseJob
	Host string
	Port int
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

// Reset clears the job for pool reuse.
func (p *PulseTCPJob) Reset() {
	p.BaseJob.Reset()
	p.Host = ""
	p.Port = 0
}

// IsNil checks if the job is nil.
func (p *PulseTCPJob) IsNil() bool {
	return p == nil
}
