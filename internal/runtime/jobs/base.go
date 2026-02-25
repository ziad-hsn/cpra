package jobs

import (
	"context"
	"time"

	"cpra/internal/constants"

	"github.com/mlange-42/ark/ecs"
)

// BaseJob provides common fields and methods for all job types.
// Embed this struct in your job implementation to get timestamp handling for free.
//
// Example:
//
//	type MyJob struct {
//	    BaseJob
//	    // ... additional fields
//	}
type BaseJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Entity      ecs.Entity
	JobType     string        // "pulse", "intervention", "code"
	Driver      string        // "http", "tcp", "icmp", "docker", etc.
	Timeout     time.Duration // Operation timeout
	Retries     int           // Number of retry attempts (0 = no retries)
}

// GetEnqueueTime returns when the job was enqueued.
func (b *BaseJob) GetEnqueueTime() time.Time { return b.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (b *BaseJob) SetEnqueueTime(t time.Time) { b.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (b *BaseJob) GetStartTime() time.Time { return b.StartTime }

// SetStartTime sets when the job started executing.
func (b *BaseJob) SetStartTime(t time.Time) { b.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (b *BaseJob) IsNil() bool { return b == nil }

// Reset clears the common fields for pool reuse.
func (b *BaseJob) Reset() {
	b.EnqueueTime = time.Time{}
	b.StartTime = time.Time{}
	b.Entity = ecs.Entity{}
	// JobType and Driver are typically constant for a job instance,
	// or set by the factory, so we might not want to clear them?
	// Existing reset functions comment: "// JobType and Driver are set on creation, don't clear"
	// So we don't clear them.
	// Retries and Timeout should be cleared?
	// Existing reset: "job.Timeout = 0", "job.Retries = 0".
	b.Timeout = 0
	b.Retries = 0
}

// CheckContext checks if the context is cancelled.
// Returns the context error if cancelled, nil otherwise.
// Use this before each retry attempt in Execute().
func (b *BaseJob) CheckContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// SleepBetweenRetries pauses briefly between retry attempts.
//
// Deprecated: Use RetryWithBackoff from helpers.go instead. This method uses
// time.Sleep which is not context-aware and can't be interrupted on shutdown.
// See "Concurrency in Go" p. 5-6 for why this is problematic.
func (b *BaseJob) SleepBetweenRetries(attempt, maxAttempts int) {
	if attempt < maxAttempts-1 {
		time.Sleep(constants.DefaultRetryBackoff)
	}
}

// Retry executes fn with exponential backoff using the job's configured retry count.
// This is a convenience wrapper around RetryWithBackoff that uses job settings.
//
// Example:
//
//	func (j *MyJob) Execute(ctx context.Context) Result {
//	    var conn net.Conn
//	    err := j.Retry(ctx, func() error {
//	        var dialErr error
//	        conn, dialErr = net.DialTimeout("tcp", j.Host, j.Timeout)
//	        return dialErr
//	    })
//	    if err != nil {
//	        return Result{Ent: j.Entity, Err: err}
//	    }
//	    defer conn.Close()
//	    // ... use conn ...
//	}
func (b *BaseJob) Retry(ctx context.Context, fn func() error) error {
	return RetryWithBackoff(ctx, b.GetAttempts(), constants.DefaultRetryBackoff, fn)
}

// GetAttempts returns the total number of attempts (retries + 1).
// Ensures at least 1 attempt even if Retries is 0 or negative.
func (b *BaseJob) GetAttempts() int {
	attempts := b.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	return attempts
}

// BaseNetworkJob extends BaseJob with dial limiter integration.
// Use this for any job that performs network I/O (HTTP, TCP, ICMP, etc.).
//
// The dial limiter prevents CPU spikes during network outages by limiting
// both the rate and concurrency of network operations.
//
// Example:
//
//	type MyNetworkJob struct {
//	    BaseNetworkJob
//	    Host string
//	    Port int
//	}
//
//	func (j *MyNetworkJob) Execute(ctx context.Context) Result {
//	    payload := getMyPayload()
//
//	    // REQUIRED: Acquire dial slot before ANY network I/O
//	    if !j.AcquireDialSlot(ctx) {
//	        return Result{Ent: j.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
//	    }
//	    defer j.ReleaseDialSlot()
//
//	    // ... perform network operation ...
//	}
type BaseNetworkJob struct {
	BaseJob
}

// AcquireDialSlot acquires a global dial slot for network operations.
// Returns true if the slot was acquired, false if the context was cancelled
// or the acquire timeout was exceeded.
//
// IMPORTANT: If this returns true, you MUST call ReleaseDialSlot() when done,
// typically via defer immediately after this call.
func (b *BaseNetworkJob) AcquireDialSlot(ctx context.Context) bool {
	return GetDialLimiter().Acquire(ctx)
}

// ReleaseDialSlot releases the dial slot back to the pool.
// Must be called after AcquireDialSlot() returns true.
func (b *BaseNetworkJob) ReleaseDialSlot() {
	GetDialLimiter().Release()
}

// TryAcquireDialSlot attempts to acquire a dial slot without blocking.
// Returns true if acquired immediately, false otherwise.
// Use this for non-blocking scenarios where you can skip the operation.
func (b *BaseNetworkJob) TryAcquireDialSlot() bool {
	return GetDialLimiter().TryAcquire()
}
