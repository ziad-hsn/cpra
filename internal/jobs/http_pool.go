package jobs

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// httpClientPool stores shared HTTP clients keyed by timeout to promote
// connection reuse and reduce per-entity client allocations.
var httpClientPool sync.Map // map[time.Duration]*http.Client

// sharedTransport is a single transport shared across all HTTP clients.
// Balances connection reuse (CPU) vs memory usage.
var sharedTransport = &http.Transport{
	// Connection pooling - balanced for CPU vs memory tradeoff
	// Each idle connection consumes ~10-50KB (buffers, TLS state, etc.)
	MaxIdleConns:        2048,              // Total idle connections (was 100000 - too high)
	MaxIdleConnsPerHost: 4,                 // Per-host idle connections (was 1024 - too high)
	MaxConnsPerHost:     0,                 // No limit on concurrent connections per host
	IdleConnTimeout:     30 * time.Second,  // Shorter timeout to release memory faster (was 90s)
	DisableKeepAlives:   false,             // Enable keep-alive for reuse

	// Connection establishment optimization
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second, // Connection timeout
		KeepAlive: 30 * time.Second, // TCP keep-alive probe interval
		DualStack: false,            // Disable dual-stack to reduce connection attempts
	}).DialContext,

	// Response handling
	ResponseHeaderTimeout: 0,  // Use client timeout
	ExpectContinueTimeout: 0,  // Disable Expect: 100-continue

	// TLS optimization
	TLSHandshakeTimeout: 10 * time.Second,
	ForceAttemptHTTP2:   false, // HTTP/1.1 is faster for health checks
}

// GetHTTPClient returns a shared *http.Client for the given timeout.
// Clients share a Transport with sensible connection pooling defaults.
func GetHTTPClient(timeout time.Duration) *http.Client {
	if v, ok := httpClientPool.Load(timeout); ok {
		return v.(*http.Client)
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: sharedTransport,
	}
	actual, _ := httpClientPool.LoadOrStore(timeout, client)
	return actual.(*http.Client)
}
