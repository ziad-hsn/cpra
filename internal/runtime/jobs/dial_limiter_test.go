package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDialLimiter_NewWithDefaults tests creating dial limiter with default config
func TestDialLimiter_NewWithDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultDialLimiterConfig()
	limiter := NewDialLimiter(cfg)

	if limiter == nil {
		t.Fatal("expected non-nil limiter")
	}

	stats := limiter.Stats()
	// Updated for 1M+ monitors scale
	if stats.MaxConcurrentDials != 16384 {
		t.Errorf("MaxConcurrentDials = %d, want 16384", stats.MaxConcurrentDials)
	}
	if stats.DialsPerSecond != 35000 {
		t.Errorf("DialsPerSecond = %d, want 35000", stats.DialsPerSecond)
	}
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials = %d, want 0", stats.InFlightDials)
	}
}

// TestDialLimiter_NewWithCustomConfig tests custom configuration
func TestDialLimiter_NewWithCustomConfig(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 100,
		DialsPerSecond:     500,
		BurstSize:          50,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	stats := limiter.Stats()
	if stats.MaxConcurrentDials != 100 {
		t.Errorf("MaxConcurrentDials = %d, want 100", stats.MaxConcurrentDials)
	}
	if stats.DialsPerSecond != 500 {
		t.Errorf("DialsPerSecond = %d, want 500", stats.DialsPerSecond)
	}
}

// TestDialLimiter_NewWithZeroValues tests that zero values get defaults
func TestDialLimiter_NewWithZeroValues(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{} // All zeros
	limiter := NewDialLimiter(cfg)

	stats := limiter.Stats()
	if stats.MaxConcurrentDials != 2048 {
		t.Errorf("MaxConcurrentDials = %d, want 2048 (default)", stats.MaxConcurrentDials)
	}
	if stats.DialsPerSecond != 10000 {
		t.Errorf("DialsPerSecond = %d, want 10000 (default)", stats.DialsPerSecond)
	}
}

// TestDialLimiter_Acquire_Success tests successful slot acquisition
func TestDialLimiter_Acquire_Success(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 10,
		DialsPerSecond:     1000,
		BurstSize:          100,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	ctx := context.Background()
	if !limiter.Acquire(ctx) {
		t.Fatal("expected Acquire to succeed")
	}

	stats := limiter.Stats()
	if stats.InFlightDials != 1 {
		t.Errorf("InFlightDials = %d, want 1", stats.InFlightDials)
	}

	limiter.Release()
	stats = limiter.Stats()
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials after Release = %d, want 0", stats.InFlightDials)
	}
}

// TestDialLimiter_Acquire_MultipleSlots tests acquiring multiple slots
func TestDialLimiter_Acquire_MultipleSlots(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 5,
		DialsPerSecond:     1000,
		BurstSize:          100,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if !limiter.Acquire(ctx) {
			t.Fatalf("expected Acquire #%d to succeed", i+1)
		}
	}

	stats := limiter.Stats()
	if stats.InFlightDials != 5 {
		t.Errorf("InFlightDials = %d, want 5", stats.InFlightDials)
	}

	// Release all
	for i := 0; i < 5; i++ {
		limiter.Release()
	}

	stats = limiter.Stats()
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials after all Release = %d, want 0", stats.InFlightDials)
	}
}

// TestDialLimiter_Acquire_ContextCancelled tests acquire with cancelled context
func TestDialLimiter_Acquire_ContextCancelled(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 1,
		DialsPerSecond:     1000,
		BurstSize:          100,
		AcquireTimeout:     5 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	// Acquire the only slot
	ctx := context.Background()
	if !limiter.Acquire(ctx) {
		t.Fatal("expected first Acquire to succeed")
	}

	// Try to acquire with already cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	if limiter.Acquire(cancelledCtx) {
		t.Fatal("expected Acquire with cancelled context to fail")
	}

	limiter.Release()
}

// TestDialLimiter_TryAcquire_Success tests non-blocking acquire
func TestDialLimiter_TryAcquire_Success(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 10,
		DialsPerSecond:     10000,
		BurstSize:          100,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	if !limiter.TryAcquire() {
		t.Fatal("expected TryAcquire to succeed")
	}

	stats := limiter.Stats()
	if stats.InFlightDials != 1 {
		t.Errorf("InFlightDials = %d, want 1", stats.InFlightDials)
	}

	limiter.Release()
}

// TestDialLimiter_TryAcquire_FullSemaphore tests TryAcquire when slots exhausted
func TestDialLimiter_TryAcquire_FullSemaphore(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 2,
		DialsPerSecond:     10000,
		BurstSize:          100,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	// Acquire all slots
	if !limiter.TryAcquire() {
		t.Fatal("expected first TryAcquire to succeed")
	}
	if !limiter.TryAcquire() {
		t.Fatal("expected second TryAcquire to succeed")
	}

	// Third should fail (non-blocking)
	if limiter.TryAcquire() {
		t.Fatal("expected third TryAcquire to fail (semaphore full)")
	}

	// Release one
	limiter.Release()

	// Now should succeed again
	if !limiter.TryAcquire() {
		t.Fatal("expected TryAcquire after Release to succeed")
	}

	// Cleanup
	limiter.Release()
	limiter.Release()
}

// TestDialLimiter_Concurrency tests concurrent access safety
func TestDialLimiter_Concurrency(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 50,
		DialsPerSecond:     100000,
		BurstSize:          1000,
		AcquireTimeout:     5 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	const numGoroutines = 100
	const iterations = 50

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var failCount atomic.Int64

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				if limiter.Acquire(ctx) {
					successCount.Add(1)
					// Simulate some work
					time.Sleep(1 * time.Millisecond)
					limiter.Release()
				} else {
					failCount.Add(1)
				}
				cancel()
			}
		}()
	}

	wg.Wait()

	// Should have many successes (exact count depends on timing)
	if successCount.Load() == 0 {
		t.Fatal("expected at least some successful acquisitions")
	}

	// Final state should be clean
	stats := limiter.Stats()
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials after test = %d, want 0", stats.InFlightDials)
	}

	t.Logf("Concurrent test: %d successes, %d failures", successCount.Load(), failCount.Load())
}

// TestDialLimiter_ConcurrencyLimit tests that concurrency limit is respected
func TestDialLimiter_ConcurrencyLimit(t *testing.T) {
	t.Parallel()
	const maxConcurrent = 5
	cfg := DialLimiterConfig{
		MaxConcurrentDials: maxConcurrent,
		DialsPerSecond:     100000,
		BurstSize:          1000,
		AcquireTimeout:     2 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	var maxObserved atomic.Int32
	var current atomic.Int32
	var wg sync.WaitGroup

	const numGoroutines = 20
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if limiter.Acquire(ctx) {
				c := current.Add(1)
				// Track max observed concurrency
				for {
					old := maxObserved.Load()
					if int32(c) <= old || maxObserved.CompareAndSwap(old, int32(c)) {
						break
					}
				}

				time.Sleep(50 * time.Millisecond) // Hold slot briefly
				current.Add(-1)
				limiter.Release()
			}
		}()
	}

	wg.Wait()

	observed := maxObserved.Load()
	if observed > maxConcurrent {
		t.Errorf("Max observed concurrency = %d, exceeds limit %d", observed, maxConcurrent)
	}

	t.Logf("Max observed concurrency: %d (limit: %d)", observed, maxConcurrent)
}

// TestDialLimiter_Stats tests statistics reporting
func TestDialLimiter_Stats(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 100,
		DialsPerSecond:     5000,
		BurstSize:          200,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	// Initial stats
	stats := limiter.Stats()
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials = %d, want 0", stats.InFlightDials)
	}
	if stats.MaxConcurrentDials != 100 {
		t.Errorf("MaxConcurrentDials = %d, want 100", stats.MaxConcurrentDials)
	}
	if stats.DialsPerSecond != 5000 {
		t.Errorf("DialsPerSecond = %d, want 5000", stats.DialsPerSecond)
	}

	// Acquire some slots
	ctx := context.Background()
	limiter.Acquire(ctx)
	limiter.Acquire(ctx)
	limiter.Acquire(ctx)

	stats = limiter.Stats()
	if stats.InFlightDials != 3 {
		t.Errorf("InFlightDials after 3 acquires = %d, want 3", stats.InFlightDials)
	}

	// Cleanup
	limiter.Release()
	limiter.Release()
	limiter.Release()
}

// TestDialLimiter_Release_NoOp tests Release when semaphore is empty
func TestDialLimiter_Release_NoOp(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 10,
		DialsPerSecond:     1000,
		BurstSize:          100,
		AcquireTimeout:     1 * time.Second,
	}
	limiter := NewDialLimiter(cfg)

	// Release without acquire should be no-op (not panic)
	limiter.Release()
	limiter.Release()

	stats := limiter.Stats()
	if stats.InFlightDials != 0 {
		t.Errorf("InFlightDials = %d, want 0", stats.InFlightDials)
	}
}

// TestDialLimiter_DefaultDialLimiterConfig tests default configuration values
// Updated for 1M+ monitors scale
func TestDialLimiter_DefaultDialLimiterConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultDialLimiterConfig()

	if cfg.MaxConcurrentDials != 16384 {
		t.Errorf("MaxConcurrentDials = %d, want 16384", cfg.MaxConcurrentDials)
	}
	if cfg.DialsPerSecond != 35000 {
		t.Errorf("DialsPerSecond = %d, want 35000", cfg.DialsPerSecond)
	}
	if cfg.BurstSize != 4000 {
		t.Errorf("BurstSize = %d, want 4000", cfg.BurstSize)
	}
	if cfg.AcquireTimeout != 5*time.Second {
		t.Errorf("AcquireTimeout = %v, want 5s", cfg.AcquireTimeout)
	}
}

// TestDialLimiter_RateLimit tests that rate limiting is working
func TestDialLimiter_RateLimit(t *testing.T) {
	t.Parallel()
	cfg := DialLimiterConfig{
		MaxConcurrentDials: 1000,
		DialsPerSecond:     100, // Low rate for testing
		BurstSize:          10,  // Small burst
		AcquireTimeout:     100 * time.Millisecond,
	}
	limiter := NewDialLimiter(cfg)

	// Exhaust burst
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if !limiter.Acquire(ctx) {
			t.Fatalf("expected Acquire #%d within burst to succeed", i+1)
		}
		limiter.Release()
	}

	// Now rate limiter should slow us down
	// At 100/sec with burst exhausted, we can only get ~10 in 100ms
	start := time.Now()
	count := 0
	deadline := start.Add(100 * time.Millisecond)

	for time.Now().Before(deadline) {
		shortCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		if limiter.Acquire(shortCtx) {
			count++
			limiter.Release()
		}
		cancel()
	}

	// Should have gotten roughly 10 acquires (100/sec * 0.1 sec)
	// Allow some variance due to timing
	if count > 20 {
		t.Errorf("Rate limiting not working: got %d acquires in 100ms, expected ~10", count)
	}

	t.Logf("Rate limit test: %d acquires in 100ms (target: ~10)", count)
}
