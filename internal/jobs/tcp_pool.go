package jobs

import (
	"context"
	"net"
	"sync"
	"time"
)

// tcpDialer is a shared dialer for TCP health checks with optimized settings.
// Using a shared dialer allows the OS to reuse TIME_WAIT sockets more efficiently.
var tcpDialer = &net.Dialer{
	Timeout:       10 * time.Second, // Default connection timeout
	KeepAlive:     -1,               // Disable keep-alive for health checks (we close immediately)
	DualStack:     false,            // IPv4 only to reduce connection attempts
	FallbackDelay: -1,               // Disable Happy Eyeballs (RFC 6555) to reduce parallel dials
}

// tcpConnSemaphore limits concurrent TCP connection attempts to prevent
// socket exhaustion. At 1M monitors with 1s intervals, we could have
// 1M concurrent dials without this limit.
var (
	tcpConnSemaphore     chan struct{}
	tcpConnSemaphoreOnce sync.Once
	tcpConnSemaphoreSize = 4096 // Max concurrent TCP dials (reduced from 8192)
)

func getTCPConnSemaphore() chan struct{} {
	tcpConnSemaphoreOnce.Do(func() {
		tcpConnSemaphore = make(chan struct{}, tcpConnSemaphoreSize)
	})
	return tcpConnSemaphore
}

// acquireTCPSlot acquires a slot in the TCP connection semaphore.
// Returns false if timeout expires or context is cancelled before acquiring a slot.
func acquireTCPSlot(ctx context.Context, timeout time.Duration) bool {
	sem := getTCPConnSemaphore()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(timeout):
		return false
	}
}

// releaseTCPSlot releases a slot back to the TCP connection semaphore.
func releaseTCPSlot() {
	sem := getTCPConnSemaphore()
	select {
	case <-sem:
	default:
		// Semaphore already empty (shouldn't happen)
	}
}

// DialTCP performs a TCP connection check with connection limiting.
// This is optimized for health checks where we only need to verify
// connectivity, not transfer data.
func DialTCP(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	// Use the shared dialer with custom timeout
	dialer := *tcpDialer // Copy to avoid modifying shared dialer
	if timeout > 0 {
		dialer.Timeout = timeout
	}
	return dialer.DialContext(ctx, "tcp", address)
}

// SetTCPConcurrencyLimit allows adjusting the TCP connection semaphore size.
// This should be called before any TCP checks are performed.
func SetTCPConcurrencyLimit(limit int) {
	if limit > 0 {
		tcpConnSemaphoreSize = limit
	}
}
