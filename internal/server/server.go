// Package server provides a graceful HTTP server with health endpoints, mTLS support,
// and Connect-Go/gRPC handler registration for the CPRA API.
package server

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"cpra/internal/config"
	"cpra/internal/controller/commands"
	"cpra/internal/health"
	"cpra/internal/mtls"

	"go.uber.org/zap"
)

// Server manages HTTP server with health endpoints and graceful shutdown.
// It supports Connect-Go handlers for gRPC/HTTP/JSON API access.
type Server struct {
	logger        *zap.SugaredLogger
	config        *config.EnvConfig
	healthManager *health.HealthManager
	httpServer    *http.Server
	mux           *http.ServeMux
	certManager   *mtls.CertificateManager

	// Command channel for API handlers to send commands to the ECS loop
	commandCh chan<- commands.Command

	// Graceful shutdown state
	draining      atomic.Bool
	shutdownStart time.Time
}

// NewServer creates a new HTTP server with health endpoints.
//
// The cmdCh parameter is optional (can be nil). If provided, it enables API handlers
// to send commands to the ECS loop for monitor CRUD operations.
func NewServer(cfg *config.EnvConfig, logger *zap.SugaredLogger, cmdCh chan<- commands.Command) (*Server, error) {
	healthManager := health.NewHealthManager(logger.Named("health"))

	s := &Server{
		logger:        logger,
		config:        cfg,
		healthManager: healthManager,
		mux:           http.NewServeMux(),
		commandCh:     cmdCh,
	}

	// Initialize certificate manager if mTLS is enabled
	if cfg.MTLSEnabled {
		certManager := mtls.NewCertificateManager(
			logger.Named("mtls"),
			cfg.MTLSCertFile,
			cfg.MTLSKeyFile,
			cfg.MTLSCAFile,
			cfg.MTLSReloadEnabled,
			time.Duration(cfg.MTLSReloadInterval)*time.Second,
		)

		// Load initial certificates
		if err := certManager.LoadCertificates(); err != nil {
			return nil, fmt.Errorf("failed to load certificates: %w", err)
		}

		s.certManager = certManager
	}

	s.setupRoutes()
	return s, nil
}

// setupRoutes configures HTTP routes
func (s *Server) setupRoutes() {
	// Health endpoints - combine health path prefix with specific paths
	livenessPath := s.config.HealthPath + s.config.LivenessPath
	readinessPath := s.config.HealthPath + s.config.ReadinessPath

	s.mux.HandleFunc(livenessPath, s.livenessHandler)
	s.mux.HandleFunc(readinessPath, s.readinessHandler)

	s.logger.Infof("Health endpoints configured: liveness=%s, readiness=%s", livenessPath, readinessPath)

	// Add a root handler for debugging
	s.mux.HandleFunc("/", s.rootHandler)
}

// livenessHandler wraps the health manager's liveness handler with graceful shutdown awareness
func (s *Server) livenessHandler(w http.ResponseWriter, r *http.Request) {
	// During graceful shutdown, mark as not alive
	if s.draining.Load() {
		s.healthManager.SetAlive(false)
	}

	s.healthManager.LivenessHandler(w, r)
}

// readinessHandler wraps the health manager's readiness handler with graceful shutdown awareness
func (s *Server) readinessHandler(w http.ResponseWriter, r *http.Request) {
	// During graceful shutdown, immediately return not ready
	if s.draining.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"not ready","message":"server draining","timestamp":"%s"}`,
			time.Now().UTC().Format(time.RFC3339))
		return
	}

	s.healthManager.ReadinessHandler(w, r)
}

// rootHandler provides basic server information
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
		"service": "%s",
		"version": "%s",
		"health": {
			"liveness": "%s%s",
			"readiness": "%s%s"
		},
		"status": "running"
	}`,
		s.config.ServiceName,
		s.config.ServiceVersion,
		s.config.HealthPath, s.config.LivenessPath,
		s.config.HealthPath, s.config.ReadinessPath)
}

// HealthManager returns the health manager for adding checks
func (s *Server) HealthManager() *health.HealthManager {
	return s.healthManager
}

// Start begins serving HTTP requests
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.config.HealthPort)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(s.mux, 1<<20), // 1MB request body limit
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second, // Prevent slowloris attacks
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB max header size
		ErrorLog:          zap.NewStdLog(s.logger.Desugar()),
	}

	// Go 1.25: Enable HTTP/2 cleartext (h2c) for gRPC/Connect without TLS
	// This allows gRPC clients to connect without TLS in development
	s.httpServer.Protocols = new(http.Protocols)
	s.httpServer.Protocols.SetHTTP1(true)
	s.httpServer.Protocols.SetUnencryptedHTTP2(true)

	// Configure TLS if mTLS is enabled
	if s.config.MTLSEnabled {
		if err := s.configureTLS(); err != nil {
			return fmt.Errorf("failed to configure TLS: %w", err)
		}

		// Start certificate reloader
		if s.certManager != nil {
			s.certManager.StartReloader()
		}
	}

	s.logger.Infof("Starting HTTP server on %s (mTLS: %v)", addr, s.config.MTLSEnabled)

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		var err error
		if s.config.MTLSEnabled {
			err = s.httpServer.ListenAndServeTLS("", "")
		} else {
			err = s.httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
		close(errChan)
	}()

	// Wait for startup error or context cancellation
	select {
	case err := <-errChan:
		if err != nil {
			return fmt.Errorf("server failed to start: %w", err)
		}
		return nil
	case <-time.After(1 * time.Second):
		s.logger.Info("HTTP server started successfully")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// configureTLS sets up TLS configuration for the server
func (s *Server) configureTLS() error {
	if s.certManager == nil {
		return fmt.Errorf("certificate manager not initialized")
	}

	// Create TLS config using certificate manager
	requireClientCert := s.config.MTLSCAFile != ""
	tlsConfig := s.certManager.CreateServerTLSConfig(s.config.ServiceName, requireClientCert)

	s.httpServer.TLSConfig = tlsConfig
	s.logger.Infof("TLS configured with cert=%s, key=%s, ca=%s, clientAuth=%v",
		s.config.MTLSCertFile, s.config.MTLSKeyFile, s.config.MTLSCAFile, requireClientCert)

	return nil
}

// Stop initiates graceful shutdown
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}

	s.logger.Info("Initiating graceful server shutdown...")
	s.shutdownStart = time.Now()

	// Mark as draining - health checks will start returning not ready/alive
	s.draining.Store(true)
	s.logger.Info("Server marked as draining - health checks will fail")

	// Wait a moment for load balancers to notice the health check failures
	select {
	case <-time.After(5 * time.Second):
		s.logger.Info("Grace period completed, shutting down server")
	case <-ctx.Done():
		s.logger.Warn("Shutdown context cancelled, proceeding with immediate shutdown")
	}

	// Shutdown HTTP server with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	// Stop certificate manager
	if s.certManager != nil {
		s.certManager.Stop()
	}

	duration := time.Since(s.shutdownStart)
	s.logger.Infof("Server graceful shutdown completed in %v", duration)
	return nil
}

// AddReadinessCheck adds a readiness check to the health manager
func (s *Server) AddReadinessCheck(checker health.Checker) {
	s.healthManager.AddReadinessCheck(checker)
}

// AddLivenessCheck adds a liveness check to the health manager
func (s *Server) AddLivenessCheck(checker health.Checker) {
	s.healthManager.AddLivenessCheck(checker)
}

// CertificateManager returns the certificate manager (may be nil if mTLS disabled)
func (s *Server) CertificateManager() *mtls.CertificateManager {
	return s.certManager
}

// RegisterHandler mounts an HTTP handler at the given path pattern.
//
// This is used to register Connect-Go service handlers, for example:
//
//	path, handler := cprav1connect.NewMonitorServiceHandler(monitorHandler)
//	server.RegisterHandler(path, handler)
//
// The handler receives the full path including any trailing slash.
func (s *Server) RegisterHandler(path string, handler http.Handler) {
	s.mux.Handle(path, handler)
	s.logger.Infof("Registered API handler: %s", path)
}

// CommandChan returns the command channel for API handlers.
// May be nil if the server was created without a command channel.
func (s *Server) CommandChan() chan<- commands.Command {
	return s.commandCh
}
