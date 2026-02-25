// Package client provides HTTP and gRPC clients with mTLS support and connection pooling.
package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cpra/internal/config"
	"cpra/internal/mtls"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// ClientManager manages HTTP and gRPC clients with shared configuration
type ClientManager struct {
	logger      *zap.SugaredLogger
	config      *config.EnvConfig
	certManager *mtls.CertificateManager
	
	// HTTP transport pool
	httpTransports  map[string]*http.Transport
	httpClients     map[string]*http.Client
	transportsMu    sync.RWMutex
	
	// gRPC connection pool  
	grpcConnections map[string]*grpc.ClientConn
	grpcMu          sync.RWMutex
}

// HTTPClientOptions configures HTTP client creation
type HTTPClientOptions struct {
	Name                string        // Client identifier for pooling
	Timeout             time.Duration // Request timeout
	IdleConnTimeout     time.Duration // Idle connection timeout
	MaxIdleConns        int          // Max idle connections
	MaxIdleConnsPerHost int          // Max idle connections per host
	InsecureSkipVerify  bool         // Skip TLS verification
}

// GRPCClientOptions configures gRPC client creation
type GRPCClientOptions struct {
	Name               string        // Connection identifier for pooling
	Address            string        // Target address
	Timeout            time.Duration // Connection timeout
	InsecureSkipVerify bool         // Skip TLS verification
	UserAgent          string       // User agent string
}

// NewClientManager creates a new client manager
func NewClientManager(cfg *config.EnvConfig, logger *zap.SugaredLogger, certManager *mtls.CertificateManager) *ClientManager {
	return &ClientManager{
		logger:          logger,
		config:          cfg,
		certManager:     certManager,
		httpTransports:  make(map[string]*http.Transport),
		httpClients:     make(map[string]*http.Client),
		grpcConnections: make(map[string]*grpc.ClientConn),
	}
}

// GetHTTPClient returns a configured HTTP client, creating and caching it if needed
func (cm *ClientManager) GetHTTPClient(opts HTTPClientOptions) (*http.Client, error) {
	cm.transportsMu.RLock()
	if client, exists := cm.httpClients[opts.Name]; exists {
		cm.transportsMu.RUnlock()
		return client, nil
	}
	cm.transportsMu.RUnlock()
	
	cm.transportsMu.Lock()
	defer cm.transportsMu.Unlock()
	
	// Double-check after acquiring write lock
	if client, exists := cm.httpClients[opts.Name]; exists {
		return client, nil
	}
	
	// Apply defaults
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.IdleConnTimeout == 0 {
		opts.IdleConnTimeout = 90 * time.Second
	}
	if opts.MaxIdleConns == 0 {
		opts.MaxIdleConns = 10
	}
	if opts.MaxIdleConnsPerHost == 0 {
		opts.MaxIdleConnsPerHost = 2
	}
	
	var transport *http.Transport
	var err error
	
	// Use mTLS certificate manager if available and enabled
	if cm.config.MTLSEnabled && cm.certManager != nil {
		transport, err = cm.certManager.CreateClientTransport(
			cm.config.MTLSClientCert,
			cm.config.MTLSClientKey,
			opts.InsecureSkipVerify,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create mTLS transport: %w", err)
		}
	} else {
		// Standard HTTP transport
		transport = &http.Transport{
			MaxIdleConns:        opts.MaxIdleConns,
			IdleConnTimeout:     opts.IdleConnTimeout,
			MaxIdleConnsPerHost: opts.MaxIdleConnsPerHost,
			DisableKeepAlives:   false,
			
			// Reasonable timeouts
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: opts.InsecureSkipVerify,
			},
		}
	}
	
	client := &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
	}
	
	cm.httpTransports[opts.Name] = transport
	cm.httpClients[opts.Name] = client
	
	cm.logger.Infof("Created HTTP client '%s' (mTLS: %v, timeout: %v)", 
		opts.Name, cm.config.MTLSEnabled, opts.Timeout)
	
	return client, nil
}

// GetGRPCConnection returns a configured gRPC connection, creating and caching it if needed
func (cm *ClientManager) GetGRPCConnection(ctx context.Context, opts GRPCClientOptions) (*grpc.ClientConn, error) {
	cm.grpcMu.RLock()
	if conn, exists := cm.grpcConnections[opts.Name]; exists {
		cm.grpcMu.RUnlock()
		return conn, nil
	}
	cm.grpcMu.RUnlock()
	
	cm.grpcMu.Lock()
	defer cm.grpcMu.Unlock()
	
	// Double-check after acquiring write lock
	if conn, exists := cm.grpcConnections[opts.Name]; exists {
		return conn, nil
	}
	
	// Apply defaults
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = fmt.Sprintf("%s/%s", cm.config.ServiceName, cm.config.ServiceVersion)
	}
	
	// Prepare dial options
	var dialOpts []grpc.DialOption
	
	// Configure credentials
	if cm.config.MTLSEnabled && cm.certManager != nil {
		// Create TLS credentials using certificate manager
		tlsConfig := &tls.Config{
			ServerName:         "", // Will be set based on target
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: opts.InsecureSkipVerify,
		}
		
		// Add client certificate if configured
		if cm.config.MTLSClientCert != "" && cm.config.MTLSClientKey != "" {
			cert, err := tls.LoadX509KeyPair(cm.config.MTLSClientCert, cm.config.MTLSClientKey)
			if err != nil {
				return nil, fmt.Errorf("failed to load client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		
		// Use CA pool for server verification
		if caPool := cm.certManager.GetCACertPool(); caPool != nil {
			tlsConfig.RootCAs = caPool
		}
		
		creds := credentials.NewTLS(tlsConfig)
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else if opts.InsecureSkipVerify {
		// Insecure connection (for development/testing)
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// Standard TLS
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
		})))
	}
	
	// Additional options
	dialOpts = append(dialOpts,
		grpc.WithUserAgent(opts.UserAgent),
		grpc.WithBlock(), // Wait for connection to be ready
	)
	
	// Create connection with timeout
	dialCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	
	conn, err := grpc.DialContext(dialCtx, opts.Address, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to gRPC server at %s: %w", opts.Address, err)
	}
	
	cm.grpcConnections[opts.Name] = conn
	
	cm.logger.Infof("Created gRPC connection '%s' to %s (mTLS: %v)", 
		opts.Name, opts.Address, cm.config.MTLSEnabled)
	
	return conn, nil
}

// CreateDefaultHTTPClient creates an HTTP client with default settings for the service
func (cm *ClientManager) CreateDefaultHTTPClient(name string) (*http.Client, error) {
	return cm.GetHTTPClient(HTTPClientOptions{
		Name:                name,
		Timeout:             30 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 2,
		InsecureSkipVerify:  false,
	})
}

// CreateDefaultGRPCConnection creates a gRPC connection with default settings
func (cm *ClientManager) CreateDefaultGRPCConnection(ctx context.Context, name, address string) (*grpc.ClientConn, error) {
	return cm.GetGRPCConnection(ctx, GRPCClientOptions{
		Name:               name,
		Address:            address,
		Timeout:            30 * time.Second,
		InsecureSkipVerify: false,
		UserAgent:          fmt.Sprintf("%s/%s", cm.config.ServiceName, cm.config.ServiceVersion),
	})
}

// Close closes all client connections and cleans up resources
func (cm *ClientManager) Close() error {
	cm.grpcMu.Lock()
	defer cm.grpcMu.Unlock()
	
	cm.transportsMu.Lock()
	defer cm.transportsMu.Unlock()
	
	// Close gRPC connections
	for name, conn := range cm.grpcConnections {
		if err := conn.Close(); err != nil {
			cm.logger.Warnf("Failed to close gRPC connection '%s': %v", name, err)
		}
	}
	cm.grpcConnections = make(map[string]*grpc.ClientConn)
	
	// Close HTTP transports
	for name, transport := range cm.httpTransports {
		transport.CloseIdleConnections()
		cm.logger.Debugf("Closed HTTP transport '%s'", name)
	}
	cm.httpTransports = make(map[string]*http.Transport)
	cm.httpClients = make(map[string]*http.Client)
	
	cm.logger.Info("Client manager closed")
	return nil
}

// HealthCheck performs a health check on all active connections
func (cm *ClientManager) HealthCheck(ctx context.Context) error {
	cm.grpcMu.RLock()
	grpcConns := make(map[string]*grpc.ClientConn)
	for name, conn := range cm.grpcConnections {
		grpcConns[name] = conn
	}
	cm.grpcMu.RUnlock()
	
	// Check gRPC connection states
	for name, conn := range grpcConns {
		state := conn.GetState()
		cm.logger.Debugf("gRPC connection '%s' in state: %v", name, state)
		// Could check specific states and attempt to reconnect here if needed
		_ = state // Avoid unused variable warning
	}
	
	return nil
}

// Stats returns statistics about the client connections
func (cm *ClientManager) Stats() map[string]interface{} {
	cm.grpcMu.RLock()
	grpcCount := len(cm.grpcConnections)
	cm.grpcMu.RUnlock()
	
	cm.transportsMu.RLock()
	httpCount := len(cm.httpClients)
	cm.transportsMu.RUnlock()
	
	return map[string]interface{}{
		"http_clients":    httpCount,
		"grpc_connections": grpcCount,
		"mtls_enabled":    cm.config.MTLSEnabled,
	}
}