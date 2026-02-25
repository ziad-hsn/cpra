package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"cpra/internal/constants"

	"github.com/valyala/fasthttp"
)

// =============================================================================
// Fasthttp Client Pool (Primary - High Performance)
// =============================================================================

// FastHTTPClientPool manages fasthttp.HostClient instances per host.
// Using HostClient instead of Client allows connection reuse per host.
type FastHTTPClientPool struct {
	clients sync.Map // map[string]*fasthttp.HostClient
}

// fasthttpClients is the global fasthttp client pool.
var fasthttpClients = &FastHTTPClientPool{}

// Get returns a fasthttp.HostClient for the given host.
// Clients are cached and reused for connection pooling.
func (p *FastHTTPClientPool) Get(host string, isTLS bool) *fasthttp.HostClient {
	key := host
	if isTLS {
		key = "tls:" + host
	}

	if v, ok := p.clients.Load(key); ok {
		return v.(*fasthttp.HostClient)
	}

	client := &fasthttp.HostClient{
		Addr: host,

		// Connection pooling - optimized for health checks at scale
		MaxConns:            constants.DefaultWorkerPerCore,   // Max concurrent connections per host
		MaxIdleConnDuration: constants.DefaultScaleUpCooldown, // Release idle connections faster

		// Timeouts - will be overridden per-request with DoTimeout
		ReadTimeout:        10 * time.Second,
		WriteTimeout:       10 * time.Second,
		MaxConnWaitTimeout: 5 * time.Second,

		// TLS configuration
		IsTLS: isTLS,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_CHACHA20_POLY1305_SHA256,
				tls.TLS_AES_128_GCM_SHA256,
			},
			RootCAs: loadRootCAs(),
		},

		// Custom dialer with SO_REUSEADDR for faster socket recycling
		Dial: customFasthttpDialer,

		// Disable features not needed for health checks
		DisableHeaderNamesNormalizing: true,
		DisablePathNormalizing:        true,
		// Connection settings
		MaxConnDuration: 5 * time.Minute,
	}

	actual, _ := p.clients.LoadOrStore(key, client)
	return actual.(*fasthttp.HostClient)
}

// AcquireHTTPDialSlot acquires a global dial slot for HTTP requests.
// This prevents CPU spikes during network outages when all targets fail.
// Returns true if slot acquired, false if timeout/cancelled.
// Caller MUST call ReleaseHTTPDialSlot() after the request completes.
func AcquireHTTPDialSlot(ctx context.Context) bool {
	return GetDialLimiter().Acquire(ctx)
}

// ReleaseHTTPDialSlot releases a dial slot back to the global pool.
func ReleaseHTTPDialSlot() {
	GetDialLimiter().Release()
}

var (
	rootCAsOnce sync.Once
	rootCAsPool *x509.CertPool
)

// loadRootCAs loads an optional PEM bundle from CPRA_TLS_CA. If unset, nil is returned (system roots).
func loadRootCAs() *x509.CertPool {
	rootCAsOnce.Do(func() {
		pemPath := os.Getenv("CPRA_TLS_CA")
		if pemPath == "" {
			return
		}
		data, err := os.ReadFile(pemPath)
		if err != nil {
			return
		}
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(data) {
			rootCAsPool = pool
		}
	})
	return rootCAsPool
}

// GetFastHTTPClient returns a fasthttp.HostClient for the given URL.
// This is the primary method for HTTP health checks.
func GetFastHTTPClient(rawURL string) (*fasthttp.HostClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	host := u.Host
	isTLS := u.Scheme == "https"

	// Add default port if missing
	if u.Port() == "" {
		if isTLS {
			host = host + ":443"
		} else {
			host = host + ":80"
		}
	}

	return fasthttpClients.Get(host, isTLS), nil
}

// ExtractHostFromURL extracts host:port from a URL for fasthttp.
func ExtractHostFromURL(rawURL string) (host string, isTLS bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false, err
	}

	host = u.Host
	isTLS = u.Scheme == "https"

	// Add default port if missing
	if u.Port() == "" {
		if isTLS {
			host = host + ":443"
		} else {
			host = host + ":80"
		}
	}

	return host, isTLS, nil
}

// =============================================================================
// Legacy net/http Client Pool (Kept for backwards compatibility)
// =============================================================================

// httpClientPool stores shared HTTP clients keyed by timeout to promote
// connection reuse and reduce per-entity client allocations.
var httpClientPool sync.Map // map[time.Duration]*http.Client

// sharedTransport is a single transport shared across all HTTP clients.
// Balances connection reuse (CPU) vs memory usage.
var sharedTransport = &http.Transport{
	// Connection pooling - balanced for CPU vs memory tradeoff
	// Each idle connection consumes ~10-50KB (buffers, TLS state, etc.)
	MaxIdleConns:        constants.DefaultChannelDepth,    // Global pool
	MaxIdleConnsPerHost: constants.MaxIdleConnsPerHost,    // Per-host idle connections
	MaxConnsPerHost:     0,                                // No limit on concurrent connections per host
	IdleConnTimeout:     constants.DefaultScaleUpCooldown, // Release idle connections faster
	DisableKeepAlives:   false,                            // Enable keep-alive for reuse

	// Connection establishment optimization
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second, // Connection timeout
		KeepAlive: 30 * time.Second, // TCP keep-alive probe interval
		DualStack: false,            // Disable dual-stack to reduce connection attempts
	}).DialContext,

	// Response handling
	ResponseHeaderTimeout: 0, // Use client timeout
	ExpectContinueTimeout: 0, // Disable Expect: 100-continue

	// TLS optimization
	TLSHandshakeTimeout: 10 * time.Second,
	ForceAttemptHTTP2:   false, // HTTP/1.1 is faster for health checks
}

// GetHTTPClient returns a shared *http.Client for the given timeout.
// Clients share a Transport with sensible connection pooling defaults.
// DEPRECATED: Use GetFastHTTPClient for better performance.
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
