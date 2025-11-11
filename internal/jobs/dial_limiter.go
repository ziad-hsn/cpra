// Package jobs provides dial limiting to prevent CPU spikes during network outages.
//
// When all monitored targets become unreachable simultaneously (network outage),
// unconstrained dial attempts can spike CPU from 50% to 800%+ in seconds.
// This limiter provides:
//
//  1. Global concurrency limit (semaphore) - caps in-flight dial attempts
//  2. Global rate limit (token bucket) - caps dial attempts per second
//
// The combination prevents thundering herd during mass failures while still
// allowing high throughput during normal operation.
package jobs

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DialLimiter provides global rate and concurrency limiting for network dials.
// It prevents CPU spikes during network outages by limiting both:
// - How many dials can be in-flight simultaneously (concurrency)
// - How many dials can start per second (rate)
type DialLimiter struct {
	sem         chan struct{}
	rateLimiter *rate.Limiter
	config      DialLimiterConfig
}

// DialLimiterConfig configures the dial limiter behavior.
type DialLimiterConfig struct {
	// MaxConcurrentDials is the maximum number of in-flight dial attempts.
	// During normal operation, this should be high enough to not bottleneck.
	// During outages, this prevents runaway goroutines.
	// Default: 2048
	MaxConcurrentDials int

	// DialsPerSecond is the maximum rate of new dial attempts per second.
	// This prevents bursts from overwhelming the system during outages.
	// Default: 10000 (10k dials/sec)
	DialsPerSecond int

	// BurstSize allows temporary bursts above the rate limit.
	// Default: 500
	BurstSize int

	// AcquireTimeout is how long to wait for a dial slot before giving up.
	// Default: 5 seconds
	AcquireTimeout time.Duration
}

// DefaultDialLimiterConfig returns sensible defaults for 100k+ monitors.
// At 100k monitors with 5s intervals = 20k checks/sec normal load.
// CRITICAL: These limits prevent CPU spikes during thundering herd when
// all check intervals align. Lower limits = more goroutines blocked waiting
// = less CPU overhead from runaway goroutines.
func DefaultDialLimiterConfig() DialLimiterConfig {
	return DialLimiterConfig{
		MaxConcurrentDials: 2048,  // Caps in-flight dials during outages
		DialsPerSecond:     10000, // Prevents goroutine pile-up
		BurstSize:          500,   // Limits initial thundering herd burst
		AcquireTimeout:     5 * time.Second,
	}
}

var (
	globalDialLimiter     *DialLimiter
	globalDialLimiterOnce sync.Once
)

// GetDialLimiter returns the global dial limiter, creating it if needed.
func GetDialLimiter() *DialLimiter {
	globalDialLimiterOnce.Do(func() {
		globalDialLimiter = NewDialLimiter(DefaultDialLimiterConfig())
	})
	return globalDialLimiter
}

// SetDialLimiterConfig allows overriding the dial limiter configuration.
// Must be called before any dials are made (typically at startup).
func SetDialLimiterConfig(cfg DialLimiterConfig) {
	globalDialLimiterOnce.Do(func() {
		globalDialLimiter = NewDialLimiter(cfg)
	})
}

// NewDialLimiter creates a new dial limiter with the given configuration.
func NewDialLimiter(cfg DialLimiterConfig) *DialLimiter {
	if cfg.MaxConcurrentDials <= 0 {
		cfg.MaxConcurrentDials = 2048
	}
	if cfg.DialsPerSecond <= 0 {
		cfg.DialsPerSecond = 10000
	}
	if cfg.BurstSize <= 0 {
		cfg.BurstSize = 500
	}
	if cfg.AcquireTimeout <= 0 {
		cfg.AcquireTimeout = 5 * time.Second
	}

	return &DialLimiter{
		sem:         make(chan struct{}, cfg.MaxConcurrentDials),
		rateLimiter: rate.NewLimiter(rate.Limit(cfg.DialsPerSecond), cfg.BurstSize),
		config:      cfg,
	}
}

// Acquire attempts to acquire permission to start a dial.
// Returns true if permission was granted, false if timeout/cancelled.
// The caller MUST call Release() after the dial completes (success or failure).
func (d *DialLimiter) Acquire(ctx context.Context) bool {
	// First, check rate limit (token bucket)
	// Use a short timeout context for rate limiter
	rateCtx, rateCancel := context.WithTimeout(ctx, d.config.AcquireTimeout)
	defer rateCancel()

	if err := d.rateLimiter.Wait(rateCtx); err != nil {
		return false
	}

	// Then, acquire concurrency slot (semaphore)
	select {
	case d.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(d.config.AcquireTimeout):
		return false
	}
}

// TryAcquire attempts to acquire permission without blocking.
// Returns true if permission was granted immediately, false otherwise.
func (d *DialLimiter) TryAcquire() bool {
	// Check rate limit (non-blocking)
	if !d.rateLimiter.Allow() {
		return false
	}

	// Try to acquire concurrency slot (non-blocking)
	select {
	case d.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release releases a dial slot back to the pool.
// Must be called after Acquire() returns true, regardless of dial outcome.
func (d *DialLimiter) Release() {
	select {
	case <-d.sem:
	default:
		// Semaphore already empty (shouldn't happen if used correctly)
	}
}

// Stats returns current dial limiter statistics.
func (d *DialLimiter) Stats() DialLimiterStats {
	return DialLimiterStats{
		InFlightDials:      len(d.sem),
		MaxConcurrentDials: cap(d.sem),
		TokensAvailable:    int(d.rateLimiter.Tokens()),
		DialsPerSecond:     d.config.DialsPerSecond,
	}
}

// DialLimiterStats provides dial limiter metrics.
type DialLimiterStats struct {
	InFlightDials      int
	MaxConcurrentDials int
	TokensAvailable    int
	DialsPerSecond     int
}
