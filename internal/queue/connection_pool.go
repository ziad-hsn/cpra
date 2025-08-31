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
	maxIdleConns        int
	maxIdleConnsPerHost int
	maxConnsPerHost     int
	idleConnTimeout     time.Duration

	// Statistics
	activeConns   int64
	totalRequests int64

	mu sync.RWMutex
}

// PoolConfig holds connection pool configuration
type PoolConfig struct {
	MaxIdleConns          int           // Maximum idle connections
	MaxIdleConnsPerHost   int           // Maximum idle connections per host
	MaxConnsPerHost       int           // Maximum connections per host
	IdleConnTimeout       time.Duration // Idle connection timeout
	DialTimeout           time.Duration // Connection dial timeout
	TLSHandshakeTimeout   time.Duration // TLS handshake timeout
	ResponseHeaderTimeout time.Duration // Response header timeout
}

// PoolStats holds connection pool statistics
type PoolStats struct {
	TotalRequests       int64
	MaxIdleConns        int64
	MaxIdleConnsPerHost int64
	MaxConnsPerHost     int64
}

// DefaultPoolConfig returns default configuration
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		DialTimeout:           10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(config PoolConfig) *ConnectionPool {
	transport := &http.Transport{
		MaxIdleConns:        config.MaxIdleConns,
		MaxIdleConnsPerHost: config.MaxIdleConnsPerHost,
		MaxConnsPerHost:     config.MaxConnsPerHost,
		IdleConnTimeout:     config.IdleConnTimeout,

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
		client:              client,
		transport:           transport,
		maxIdleConns:        config.MaxIdleConns,
		maxIdleConnsPerHost: config.MaxIdleConnsPerHost,
		maxConnsPerHost:     config.MaxConnsPerHost,
		idleConnTimeout:     config.IdleConnTimeout,
	}
}

// GetClient returns the HTTP client
func (cp *ConnectionPool) GetClient() *http.Client {
	return cp.client
}

// Do performs an HTTP request using the pooled client
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
		TotalRequests:       cp.totalRequests,
		MaxIdleConns:        int64(cp.maxIdleConns),
		MaxIdleConnsPerHost: int64(cp.maxIdleConnsPerHost),
		MaxConnsPerHost:     int64(cp.maxConnsPerHost),
	}
}

// Close closes the connection pool and cleans up resources
func (cp *ConnectionPool) Close() {
	cp.transport.CloseIdleConnections()
}
