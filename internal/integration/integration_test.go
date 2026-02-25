// Package integration provides integration tests for health endpoints, mTLS, and tracing.
//go:build integration
// +build integration

package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cpra/internal/config"
	"cpra/internal/health"
	"cpra/internal/otel"
	"cpra/internal/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// testConfig creates a test configuration
func testConfig() *config.EnvConfig {
	return &config.EnvConfig{
		Env:                "test",
		Debug:             true,
		LogLevel:          "debug",
		LogFormat:         "console",
		HealthPort:        0, // Random port
		LivenessPath:      "/healthz",
		ReadinessPath:     "/readyz",
		HealthCacheTTL:    5, // Short cache for testing
		MTLSEnabled:       false, // Disabled for basic tests
		OTLPEnabled:       false, // Disabled for basic tests
		ServiceName:       "cpra-test",
		ServiceVersion:    "test",
		TraceRatio:        1.0, // Sample all traces in tests
	}
}

// TestHealthEndpoints tests liveness and readiness endpoints
func TestHealthEndpoints(t *testing.T) {
	cfg := testConfig()
	logger := zaptest.NewLogger(t).Sugar()
	
	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	
	// Add some health checks
	srv.AddReadinessCheck(health.NewFuncChecker("test-dep", 2*time.Second, func(ctx context.Context) error {
		return nil // Always healthy
	}))
	
	// Create test server
	testServer := httptest.NewServer(srv.HealthManager())
	defer testServer.Close()
	
	client := &http.Client{Timeout: 5 * time.Second}
	
	t.Run("Liveness endpoint", func(t *testing.T) {
		resp, err := client.Get(testServer.URL + cfg.LivenessPath)
		if err != nil {
			t.Fatalf("Liveness request failed: %v", err)
		}
		defer resp.Body.Close()
		
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
		
		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}
		
		if response["status"] != "ok" {
			t.Errorf("Expected status 'ok', got %v", response["status"])
		}
	})
	
	t.Run("Readiness endpoint", func(t *testing.T) {
		resp, err := client.Get(testServer.URL + cfg.ReadinessPath)
		if err != nil {
			t.Fatalf("Readiness request failed: %v", err)
		}
		defer resp.Body.Close()
		
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
		
		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}
		
		if response["status"] != "ready" {
			t.Errorf("Expected status 'ready', got %v", response["status"])
		}
		
		// Check that our test dependency is included
		checks, ok := response["checks"].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected checks to be a map")
		}
		
		testDep, ok := checks["test-dep"]
		if !ok {
			t.Errorf("Expected test-dep check to be present")
		} else {
			testDepMap := testDep.(map[string]interface{})
			if testDepMap["status"] != "healthy" {
				t.Errorf("Expected test-dep to be healthy, got %v", testDepMap["status"])
			}
		}
	})
	
	t.Run("Invalid path", func(t *testing.T) {
		resp, err := client.Get(testServer.URL + "/invalid")
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()
		
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", resp.StatusCode)
		}
	})
}

// TestHealthCheckFailure tests health check failure scenarios
func TestHealthCheckFailure(t *testing.T) {
	cfg := testConfig()
	logger := zaptest.NewLogger(t).Sugar()
	
	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	
	// Add a failing health check
	srv.AddReadinessCheck(health.NewFuncChecker("failing-dep", 1*time.Second, func(ctx context.Context) error {
		return fmt.Errorf("service unavailable")
	}))
	
	testServer := httptest.NewServer(srv.HealthManager())
	defer testServer.Close()
	
	client := &http.Client{Timeout: 5 * time.Second}
	
	t.Run("Readiness with failing dependency", func(t *testing.T) {
		resp, err := client.Get(testServer.URL + cfg.ReadinessPath)
		if err != nil {
			t.Fatalf("Readiness request failed: %v", err)
		}
		defer resp.Body.Close()
		
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Expected status 503, got %d", resp.StatusCode)
		}
		
		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}
		
		if response["status"] != "not ready" {
			t.Errorf("Expected status 'not ready', got %v", response["status"])
		}
	})
}

// TestTraceContextPropagation tests trace context propagation
func TestTraceContextPropagation(t *testing.T) {
	// Initialize OpenTelemetry for testing
	cfg := testConfig()
	cfg.OTLPEnabled = true
	cfg.OTLPEndpoint = "localhost:4317"
	cfg.OTLPInsecure = true
	
	logger := zaptest.NewLogger(t).Sugar()
	
	// Initialize tracing
	tracingManager := otel.NewTracingManager(cfg, logger)
	ctx := context.Background()
	if err := tracingManager.Initialize(ctx); err != nil {
		t.Logf("Failed to initialize tracing (expected if no OTLP collector): %v", err)
		// Continue with test - we can still test propagation without exporter
	}
	defer tracingManager.Shutdown(ctx)
	
	tracer := otel.Tracer("integration-test")
	
	t.Run("Trace context creation and propagation", func(t *testing.T) {
		// Start a parent span
		parentCtx, parentSpan := tracer.Start(ctx, "parent-operation")
		defer parentSpan.End()
		
		parentSpan.SetAttributes(
			attribute.String("test.case", "trace-propagation"),
			attribute.String("service.name", cfg.ServiceName),
		)
		
		// Simulate HTTP request with trace propagation
		propagator := otel.GetTextMapPropagator()
		headers := make(http.Header)
		propagator.Inject(parentCtx, propagation.HeaderCarrier(headers))
		
		// Verify trace headers are set
		traceParent := headers.Get("traceparent")
		if traceParent == "" {
			t.Error("Expected traceparent header to be set")
		}
		
		t.Logf("Trace parent header: %s", traceParent)
		
		// Extract trace context
		extractedCtx := propagator.Extract(context.Background(), propagation.HeaderCarrier(headers))
		
		// Start child span with extracted context
		childCtx, childSpan := tracer.Start(extractedCtx, "child-operation")
		defer childSpan.End()
		
		childSpan.SetAttributes(
			attribute.String("test.operation", "child"),
		)
		
		// Verify parent-child relationship
		parentSpanCtx := parentSpan.SpanContext()
		childSpanCtx := childSpan.SpanContext()
		
		if !parentSpanCtx.TraceID().IsValid() {
			t.Error("Parent trace ID is not valid")
		}
		
		if !childSpanCtx.TraceID().IsValid() {
			t.Error("Child trace ID is not valid")
		}
		
		if parentSpanCtx.TraceID() != childSpanCtx.TraceID() {
			t.Error("Parent and child should have the same trace ID")
		}
		
		if parentSpanCtx.SpanID() == childSpanCtx.SpanID() {
			t.Error("Parent and child should have different span IDs")
		}
		
		t.Logf("Parent span - Trace ID: %s, Span ID: %s", 
			parentSpanCtx.TraceID(), parentSpanCtx.SpanID())
		t.Logf("Child span - Trace ID: %s, Span ID: %s", 
			childSpanCtx.TraceID(), childSpanCtx.SpanID())
		
		// Add some span events
		parentSpan.AddEvent("parent-event", trace.WithAttributes(
			attribute.String("event.type", "test"),
		))
		
		childSpan.AddEvent("child-event", trace.WithAttributes(
			attribute.String("event.type", "test"),
		))
		
		_ = parentCtx
		_ = childCtx
	})
}

// TestMTLSIntegration tests mTLS configuration and certificate handling
func TestMTLSIntegration(t *testing.T) {
	// Skip if certificates are not available
	certFile := os.Getenv("TEST_CERT_FILE")
	keyFile := os.Getenv("TEST_KEY_FILE")
	caFile := os.Getenv("TEST_CA_FILE")
	
	if certFile == "" || keyFile == "" || caFile == "" {
		t.Skip("Skipping mTLS test: TEST_CERT_FILE, TEST_KEY_FILE, and TEST_CA_FILE environment variables not set")
	}
	
	cfg := testConfig()
	cfg.MTLSEnabled = true
	cfg.MTLSCertFile = certFile
	cfg.MTLSKeyFile = keyFile
	cfg.MTLSCAFile = caFile
	
	logger := zaptest.NewLogger(t).Sugar()
	
	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	
	t.Run("Certificate loading", func(t *testing.T) {
		certManager := srv.CertificateManager()
		if certManager == nil {
			t.Fatal("Certificate manager should not be nil when mTLS is enabled")
		}
		
		// Test certificate access
		serverCert := certManager.GetServerCertificate()
		if serverCert == nil {
			t.Error("Server certificate getter should not be nil")
		}
		
		caCertPool := certManager.GetCACertPool()
		if caCertPool == nil {
			t.Error("CA certificate pool should not be nil")
		}
	})
}

// TestGracefulShutdown tests server graceful shutdown
func TestGracefulShutdown(t *testing.T) {
	cfg := testConfig()
	logger := zaptest.NewLogger(t).Sugar()
	
	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	
	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	
	go func() {
		if err := srv.Start(ctx); err != nil {
			t.Logf("Server start error (expected): %v", err)
		}
	}()
	
	// Give server time to start
	time.Sleep(100 * time.Millisecond)
	
	t.Run("Graceful shutdown", func(t *testing.T) {
		// Cancel context to trigger shutdown
		cancel()
		
		// Stop server with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		
		err := srv.Stop(shutdownCtx)
		if err != nil {
			t.Errorf("Server shutdown failed: %v", err)
		}
	})
}

// TestHealthCheckCaching tests readiness check caching
func TestHealthCheckCaching(t *testing.T) {
	cfg := testConfig()
	cfg.HealthCacheTTL = 1 // 1 second cache
	logger := zaptest.NewLogger(t).Sugar()
	
	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	
	// Add a slow health check
	callCount := 0
	srv.AddReadinessCheck(health.NewFuncChecker("slow-check", 2*time.Second, func(ctx context.Context) error {
		callCount++
		time.Sleep(100 * time.Millisecond) // Simulate slow check
		return nil
	}))
	
	testServer := httptest.NewServer(srv.HealthManager())
	defer testServer.Close()
	
	client := &http.Client{Timeout: 5 * time.Second}
	
	t.Run("Caching behavior", func(t *testing.T) {
		// First request - should execute check
		resp1, err := client.Get(testServer.URL + cfg.ReadinessPath)
		if err != nil {
			t.Fatalf("First request failed: %v", err)
		}
		resp1.Body.Close()
		
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp1.StatusCode)
		}
		
		firstCallCount := callCount
		
		// Second request immediately - should use cache
		resp2, err := client.Get(testServer.URL + cfg.ReadinessPath)
		if err != nil {
			t.Fatalf("Second request failed: %v", err)
		}
		resp2.Body.Close()
		
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp2.StatusCode)
		}
		
		if callCount != firstCallCount {
			t.Errorf("Expected call count to remain %d (cached), but got %d", firstCallCount, callCount)
		}
		
		// Wait for cache to expire
		time.Sleep(2 * time.Second)
		
		// Third request - should execute check again
		resp3, err := client.Get(testServer.URL + cfg.ReadinessPath)
		if err != nil {
			t.Fatalf("Third request failed: %v", err)
		}
		resp3.Body.Close()
		
		if resp3.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp3.StatusCode)
		}
		
		if callCount <= firstCallCount {
			t.Errorf("Expected call count to increase after cache expiry, got %d", callCount)
		}
	})
}