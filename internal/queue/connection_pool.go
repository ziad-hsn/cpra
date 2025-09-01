package queue

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

// ConnectionPool manages HTTP connections for efficient reuse
type ConnectionPool struct {
	client    *http.Client
	transport *http.Transport

	// Pool configuration
	maxIdleCons        int
	maxIdleConsPerHost int
	maxConsPerHost     int
	idleConTimeout     time.Duration

	// Statistics
	activeCons    int64
	totalRequests int64

	mu sync.RWMutex
}

// PoolConfig holds connection pool configuration
type PoolConfig struct {
	MaxIdleCons           int           // Maximum idle connections
	MaxIdleConsPerHost    int           // Maximum idle connections per host
	MaxConsPerHost        int           // Maximum connections per host
	IdleConTimeout        time.Duration // Idle connection timeout
	DialTimeout           time.Duration // Connection dial timeout
	TLSHandshakeTimeout   time.Duration // TLS handshake timeout
	ResponseHeaderTimeout time.Duration // Response header timeout
}

// PoolStats holds connection pool statistics
type PoolStats struct {
	TotalRequests      int64
	MaxIdleCons        int64
	MaxIdleConsPerHost int64
	MaxConsPerHost     int64
}

// DefaultPoolConfig returns default configuration
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxIdleCons:           100,
		MaxIdleConsPerHost:    10,
		MaxConsPerHost:        50,
		IdleConTimeout:        90 * time.Second,
		DialTimeout:           10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(config PoolConfig) *ConnectionPool {
	transport := &http.Transport{
		MaxIdleConns:        config.MaxIdleCons,
		MaxIdleConnsPerHost: config.MaxIdleConsPerHost,
		MaxConnsPerHost:     config.MaxConsPerHost,
		IdleConnTimeout:     config.IdleConTimeout,

		DialContext: (&net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,

		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,

		// Optimize for performance
		DisableCompression: false,
		DisableKeepAlives:  false,
		ForceAttemptHTTP2:  true,

		// TLS configuration
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	return &ConnectionPool{
		client:             client,
		transport:          transport,
		maxIdleCons:        config.MaxIdleCons,
		maxIdleConsPerHost: config.MaxIdleConsPerHost,
		maxConsPerHost:     config.MaxConsPerHost,
		idleConTimeout:     config.IdleConTimeout,
	}
}

// GetClient returns the HTTP client
func (cp *ConnectionPool) GetClient() *http.Client {
	return cp.client
}

// Do perform an HTTP request using the pooled client
func (cp *ConnectionPool) Do(req *http.Request) (*http.Response, error) {
	cp.mu.Lock()
	cp.totalRequests++
	cp.mu.Unlock()

	return cp.client.Do(req)
}

// DoWithContext performs an HTTP request with context
func (cp *ConnectionPool) DoWithContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	return cp.Do(req)
}

// Stats returns connection pool statistics
func (cp *ConnectionPool) Stats() PoolStats {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	return PoolStats{
		TotalRequests:      cp.totalRequests,
		MaxIdleCons:        int64(cp.maxIdleCons),
		MaxIdleConsPerHost: int64(cp.maxIdleConsPerHost),
		MaxConsPerHost:     int64(cp.maxConsPerHost),
	}
}

// Close closes the connection pool and cleans up resources
func (cp *ConnectionPool) Close() {
	cp.transport.CloseIdleConnections()
}
