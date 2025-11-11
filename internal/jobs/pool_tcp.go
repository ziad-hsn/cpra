package jobs

import (
	"context"
	"net"
	"sync"
	"syscall"
	"time"
)

// TCP dialer with sync.Once for thread-safe lazy initialization.
// This allows configuration to be set before first use.
var (
	tcpDialer     *net.Dialer
	tcpDialerOnce sync.Once
)

// getTCPDialer returns the shared TCP dialer, initializing it on first use.
// Uses sync.Once to ensure exactly one initialization.
func getTCPDialer() *net.Dialer {
	tcpDialerOnce.Do(func() {
		tcpDialer = &net.Dialer{
			Timeout:       10 * time.Second, // Default connection timeout
			KeepAlive:     -1,               // Disable keep-alive for health checks (we close immediately)
			DualStack:     false,            // IPv4 only to reduce connection attempts
			FallbackDelay: -1,               // Disable Happy Eyeballs (RFC 6555) to reduce parallel dials
			// Control function to set socket options for faster recycling
			Control: func(network, address string, c syscall.RawConn) error {
				return c.Control(func(fd uintptr) {
					// SO_REUSEADDR allows faster socket recycling
					_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				})
			},
		}
	})
	return tcpDialer
}

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
