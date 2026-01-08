package jobs

import (
	"context"
	"net"
	"time"
)

// acquireTCPSlot acquires a slot from the global dial limiter.
// This provides both rate limiting and concurrency limiting to prevent
// CPU spikes during network outages when all targets become unreachable.
// Returns false if timeout expires or context is cancelled before acquiring a slot.
func acquireTCPSlot(ctx context.Context, _ time.Duration) bool {
	return GetDialLimiter().Acquire(ctx)
}

// releaseTCPSlot releases a slot back to the global dial limiter.
func releaseTCPSlot() {
	GetDialLimiter().Release()
}

// DialTCP performs a TCP connection check with connection limiting.
// This is optimized for health checks where we only need to verify
// connectivity, not transfer data.
func DialTCP(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	// Use the shared dialer with custom timeout
	dialer := *getTCPDialer() // Copy to avoid modifying shared dialer
	if timeout > 0 {
		dialer.Timeout = timeout
	}
	return dialer.DialContext(ctx, "tcp", address)
}

// SetTCPConcurrencyLimit is deprecated. Use SetDialLimiterConfig instead.
// Kept for backwards compatibility.
func SetTCPConcurrencyLimit(_ int) {
	// No-op: use SetDialLimiterConfig for unified dial limiting
}
