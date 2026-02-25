package jobs

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// =============================================================================
// PulseHTTPJob Execute Tests
// =============================================================================

// TestPulseHTTPJob_Execute_Success tests HTTP job with successful response
func TestPulseHTTPJob_Execute_Success(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, isTLS, err := ExtractHostFromURL(server.URL)
	if err != nil {
		t.Fatalf("failed to extract host: %v", err)
	}

	job := &PulseHTTPJob{
		URL:    server.URL,
		Method: "GET",
		Host:   host,
		IsTLS:  isTLS,
		BaseJob: BaseJob{
			Timeout: 5 * time.Second,
			Retries: 0,
		},
	}

	ctx := context.Background()
	result := job.Execute(ctx)

	if result.Err != nil {
		t.Errorf("expected nil error, got %v", result.Err)
	}
	if result.Payload == nil {
		t.Error("expected non-nil payload")
	}
}

// TestPulseHTTPJob_Execute_Success_2xx tests various 2xx responses
func TestPulseHTTPJob_Execute_Success_2xx(t *testing.T) {
	t.Parallel()
	statusCodes := []int{200, 201, 202, 204}

	for _, code := range statusCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			host, isTLS, _ := ExtractHostFromURL(server.URL)
			job := &PulseHTTPJob{
				URL:    server.URL,
				Method: "GET",
				Host:   host,
				IsTLS:  isTLS,
				BaseJob: BaseJob{
					Timeout: 5 * time.Second,
				},
			}

			result := job.Execute(context.Background())
			if result.Err != nil {
				t.Errorf("expected success for %d, got %v", code, result.Err)
			}
		})
	}
}

// TestPulseHTTPJob_Execute_Failure_Non2xx tests non-2xx response codes
func TestPulseHTTPJob_Execute_Failure_Non2xx(t *testing.T) {
	t.Parallel()
	statusCodes := []int{400, 401, 403, 404, 500, 502, 503}

	for _, code := range statusCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			host, isTLS, _ := ExtractHostFromURL(server.URL)
			job := &PulseHTTPJob{
				URL:    server.URL,
				Method: "GET",
				Host:   host,
				IsTLS:  isTLS,
				BaseJob: BaseJob{
					Timeout: 5 * time.Second,
					Retries: 0, // No retries
				},
			}

			result := job.Execute(context.Background())
			if result.Err == nil {
				t.Errorf("expected error for %d, got nil", code)
			}
			if !errors.Is(result.Err, ErrHTTPCheckFailed) {
				t.Errorf("expected ErrHTTPCheckFailed, got %v", result.Err)
			}
		})
	}
}

// TestPulseHTTPJob_Execute_Timeout tests timeout handling
func TestPulseHTTPJob_Execute_Timeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // Delay longer than timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, isTLS, _ := ExtractHostFromURL(server.URL)
	job := &PulseHTTPJob{
		URL:    server.URL,
		Method: "GET",
		Host:   host,
		IsTLS:  isTLS,
		BaseJob: BaseJob{
			Timeout: 50 * time.Millisecond, // Very short timeout
			Retries: 0,
		},
	}

	result := job.Execute(context.Background())
	if result.Err == nil {
		t.Error("expected timeout error, got nil")
	}
}

// TestPulseHTTPJob_Execute_ContextCancelled tests context cancellation
func TestPulseHTTPJob_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, isTLS, _ := ExtractHostFromURL(server.URL)
	job := &PulseHTTPJob{
		URL:    server.URL,
		Method: "GET",
		Host:   host,
		IsTLS:  isTLS,
		BaseJob: BaseJob{
			Timeout: 5 * time.Second,
			Retries: 0,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result := job.Execute(ctx)
	if result.Err == nil {
		t.Error("expected context cancelled error, got nil")
	}
}

// TestPulseHTTPJob_Execute_WithRetries tests retry logic
func TestPulseHTTPJob_Execute_WithRetries(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, isTLS, _ := ExtractHostFromURL(server.URL)
	job := &PulseHTTPJob{
		URL:    server.URL,
		Method: "GET",
		Host:   host,
		IsTLS:  isTLS,
		BaseJob: BaseJob{
			Timeout: 5 * time.Second,
			Retries: 3, // 4 total attempts
		},
	}

	result := job.Execute(context.Background())
	if result.Err != nil {
		t.Errorf("expected success after retries, got %v", result.Err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

// =============================================================================
// PulseTCPJob Execute Tests
// =============================================================================

// TestPulseTCPJob_Execute_Success tests TCP job with available port
func TestPulseTCPJob_Execute_Success(t *testing.T) {
	t.Parallel()
	// Start a TCP listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Accept connections in the background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	job := &PulseTCPJob{
		Host: "127.0.0.1",
		Port: addr.Port,
		BaseJob: BaseJob{
			Timeout: 5 * time.Second,
			Retries: 0,
		},
	}

	result := job.Execute(context.Background())
	if result.Err != nil {
		t.Errorf("expected nil error, got %v", result.Err)
	}
}

// TestPulseTCPJob_Execute_Failure_PortClosed tests TCP job with closed port
func TestPulseTCPJob_Execute_Failure_PortClosed(t *testing.T) {
	t.Parallel()
	// Use a port that's almost certainly not listening
	job := &PulseTCPJob{
		Host: "127.0.0.1",
		Port: 59999, // Unlikely to be in use
		BaseJob: BaseJob{
			Timeout: 500 * time.Millisecond,
			Retries: 0,
		},
	}

	result := job.Execute(context.Background())
	if result.Err == nil {
		t.Error("expected error for closed port, got nil")
	}
	if !errors.Is(result.Err, ErrTCPCheckFailed) {
		t.Errorf("expected ErrTCPCheckFailed, got %v", result.Err)
	}
}

// TestPulseTCPJob_Execute_ContextCancelled tests context cancellation
func TestPulseTCPJob_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	job := &PulseTCPJob{
		Host: "192.0.2.1", // TEST-NET-1, non-routable
		Port: 80,
		BaseJob: BaseJob{
			Timeout: 5 * time.Second,
			Retries: 0,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := job.Execute(ctx)
	if result.Err == nil {
		t.Error("expected context cancelled error, got nil")
	}
}

// TestPulseTCPJob_Execute_WithRetries tests retry logic for TCP
func TestPulseTCPJob_Execute_WithRetries(t *testing.T) {
	t.Parallel()
	// Start a listener that will close after some connections
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	job := &PulseTCPJob{
		Host: "127.0.0.1",
		Port: addr.Port,
		BaseJob: BaseJob{
			Timeout: 1 * time.Second,
			Retries: 2,
		},
	}

	result := job.Execute(context.Background())
	if result.Err != nil {
		t.Errorf("expected success, got %v", result.Err)
	}
}

// =============================================================================
// Job Interface Tests
// =============================================================================

// TestJob_Copy tests that Copy creates independent copies
func TestJob_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseHTTPJob{
		URL:    "http://example.com",
		Method: "GET",
		BaseJob: BaseJob{
			Retries: 3,
		},
	}
	original.SetEnqueueTime(time.Now())

	copied := original.Copy()
	copiedHTTP, ok := copied.(*PulseHTTPJob)
	if !ok {
		t.Fatal("Copy returned wrong type")
	}

	// Modify original
	original.URL = "http://modified.com"
	original.Retries = 0

	// Copy should be unchanged
	if copiedHTTP.URL != "http://example.com" {
		t.Errorf("URL was modified in copy, got %q", copiedHTTP.URL)
	}
	if copiedHTTP.BaseJob.Retries != 3 {
		t.Errorf("Retries was modified in copy, got %d", copiedHTTP.BaseJob.Retries)
	}
}

// TestJob_Timestamps tests enqueue and start time handling
func TestJob_Timestamps(t *testing.T) {
	t.Parallel()
	job := &PulseHTTPJob{}

	// Initially zero
	if !job.GetEnqueueTime().IsZero() {
		t.Error("EnqueueTime should be zero initially")
	}
	if !job.GetStartTime().IsZero() {
		t.Error("StartTime should be zero initially")
	}

	// Set times
	now := time.Now()
	job.SetEnqueueTime(now)
	job.SetStartTime(now.Add(100 * time.Millisecond))

	if !job.GetEnqueueTime().Equal(now) {
		t.Error("EnqueueTime not set correctly")
	}
	if !job.GetStartTime().Equal(now.Add(100 * time.Millisecond)) {
		t.Error("StartTime not set correctly")
	}
}

// TestJob_IsNil tests nil checking for all job types
func TestJob_IsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		job  Job
		want bool
	}{
		{"nil *PulseHTTPJob", (*PulseHTTPJob)(nil), true},
		{"nil *PulseTCPJob", (*PulseTCPJob)(nil), true},
		{"nil *PulseICMPJob", (*PulseICMPJob)(nil), true},
		{"nil *InterventionDockerJob", (*InterventionDockerJob)(nil), true},
		{"nil *CodeLogJob", (*CodeLogJob)(nil), true},
		{"non-nil PulseHTTPJob", &PulseHTTPJob{URL: "test"}, false},
		{"non-nil PulseTCPJob", &PulseTCPJob{Host: "test"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.IsNil(); got != tt.want {
				t.Errorf("IsNil() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Result Tests
// =============================================================================

// TestResult_Entity tests Result entity getter
func TestResult_Entity(t *testing.T) {
	t.Parallel()
	entity := ecs.Entity{}
	result := Result{Ent: entity}

	if result.Entity() != entity {
		t.Error("Entity() returned wrong value")
	}
}

// TestResult_Error tests Result error getter
func TestResult_Error(t *testing.T) {
	t.Parallel()
	testErr := errors.New("test error")
	result := Result{Err: testErr}

	if result.Error() != testErr {
		t.Error("Error() returned wrong value")
	}

	nilResult := Result{}
	if nilResult.Error() != nil {
		t.Error("Error() should return nil for no error")
	}
}

// TestResult_Payload tests Result with payload
func TestResult_Payload(t *testing.T) {
	t.Parallel()
	payload := map[string]interface{}{
		"type":   "pulse",
		"driver": "http",
	}
	result := Result{Payload: payload}

	if result.Payload["type"] != "pulse" {
		t.Error("Payload not preserved")
	}
}

// =============================================================================
// Code Job Execute Tests (basic - they mostly just return success)
// =============================================================================

// TestCodeLogJob_Execute tests log job execution
func TestCodeLogJob_Execute(t *testing.T) {
	t.Parallel()
	job := &CodeLogJob{
		Monitor:  "test-monitor",
		Color:    "red",
		Status:   "CRITICAL",
		Severity: "critical",
		Summary:  "Test alert",
		File:     "", // Empty file means stdout
	}

	result := job.Execute(context.Background())
	// Log jobs typically succeed (just write to log)
	if result.Payload == nil {
		t.Error("expected non-nil payload")
	}
}

// =============================================================================
// BaseJob Tests
// =============================================================================

// TestBaseJob_CheckContext tests context checking helper
func TestBaseJob_CheckContext(t *testing.T) {
	t.Parallel()
	base := &BaseJob{}

	// Normal context should return nil
	ctx := context.Background()
	if err := base.CheckContext(ctx); err != nil {
		t.Errorf("expected nil for active context, got %v", err)
	}

	// Cancelled context should return error
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := base.CheckContext(cancelledCtx); err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

// TestBaseJob_GetAttempts tests attempt calculation
func TestBaseJob_GetAttempts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		retries int
		want    int
	}{
		{0, 1},  // 0 retries = 1 attempt
		{1, 2},  // 1 retry = 2 attempts
		{3, 4},  // 3 retries = 4 attempts
		{-1, 1}, // negative = at least 1
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			base := &BaseJob{Retries: tt.retries}
			if got := base.GetAttempts(); got != tt.want {
				t.Errorf("GetAttempts() with %d retries = %d, want %d", tt.retries, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Additional Code Job Execute Tests
// =============================================================================

// TestCodeNotificationJob_Execute tests unified notification job execution with different drivers
func TestCodeNotificationJob_Execute(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		driverName string
		color      string
	}{
		{"pagerduty", "pagerduty", "red"},
		{"slack", "slack", "yellow"},
		{"email", "email", "red"},
		{"webhook", "webhook", "green"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := &CodeNotificationJob{
				Monitor:    "test-monitor",
				Color:      tc.color,
				Message:    "Test alert",
				DriverName: tc.driverName,
			}

			result := job.Execute(context.Background())
			if result.Err != nil {
				t.Errorf("expected nil error, got %v", result.Err)
			}
			if result.Payload == nil {
				t.Error("expected non-nil payload")
			}
			if result.Payload["driver"] != tc.driverName {
				t.Errorf("driver = %v, want %s", result.Payload["driver"], tc.driverName)
			}
		})
	}
}

// =============================================================================
// Code Job Copy Tests
// =============================================================================

// TestCodeNotificationJob_Copy tests unified notification job Copy
func TestCodeNotificationJob_Copy(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		driverName string
		color      string
	}{
		{"pagerduty", "pagerduty", "red"},
		{"slack", "slack", "yellow"},
		{"email", "email", "red"},
		{"webhook", "webhook", "green"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := &CodeNotificationJob{
				Monitor:    "test-monitor",
				Color:      tc.color,
				DriverName: tc.driverName,
				Message:    "Test",
			}
			original.SetEnqueueTime(time.Now())

			copied := original.Copy()
			copiedJob, ok := copied.(*CodeNotificationJob)
			if !ok {
				t.Fatal("Copy returned wrong type")
			}

			original.Monitor = "modified"
			if copiedJob.Monitor != "test-monitor" {
				t.Error("Monitor was modified in copy")
			}
			if copiedJob.DriverName != tc.driverName {
				t.Errorf("DriverName = %v, want %s", copiedJob.DriverName, tc.driverName)
			}
		})
	}
}

// =============================================================================
// Additional Job Tests
// =============================================================================

// TestInterventionDockerJob_Copy tests Docker intervention Copy
func TestInterventionDockerJob_Copy(t *testing.T) {
	t.Parallel()
	original := &InterventionDockerJob{
		Container:  "test-container",
		DockerHost: "unix:///var/run/docker.sock",
		BaseJob: BaseJob{
			Retries: 3,
		},
	}
	original.SetEnqueueTime(time.Now())

	copied := original.Copy()
	copiedDocker, ok := copied.(*InterventionDockerJob)
	if !ok {
		t.Fatal("Copy returned wrong type")
	}

	original.Container = "modified"
	if copiedDocker.Container != "test-container" {
		t.Error("Container was modified in copy")
	}
}

// TestPulseTCPJob_Copy tests TCP job Copy
func TestPulseTCPJob_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseTCPJob{
		Host: "localhost",
		Port: 5432,
		BaseJob: BaseJob{
			Retries: 2,
		},
	}

	copied := original.Copy()
	copiedTCP, ok := copied.(*PulseTCPJob)
	if !ok {
		t.Fatal("Copy returned wrong type")
	}

	original.Host = "modified"
	if copiedTCP.Host != "localhost" {
		t.Error("Host was modified in copy")
	}
}

// TestPulseICMPJob_Copy tests ICMP job Copy
func TestPulseICMPJob_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseICMPJob{
		Host:  "gateway.local",
		Count: 3,
	}

	copied := original.Copy()
	copiedICMP, ok := copied.(*PulseICMPJob)
	if !ok {
		t.Fatal("Copy returned wrong type")
	}

	original.Host = "modified"
	if copiedICMP.Host != "gateway.local" {
		t.Error("Host was modified in copy")
	}
}

// TestPulseHTTPJob_Execute_Methods tests different HTTP methods
func TestPulseHTTPJob_Execute_Methods(t *testing.T) {
	t.Parallel()
	methods := []string{"GET", "HEAD", "POST", "PUT"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("Method = %s, want %s", r.Method, method)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			host, isTLS, _ := ExtractHostFromURL(server.URL)
			job := &PulseHTTPJob{
				URL:    server.URL,
				Method: method,
				Host:   host,
				IsTLS:  isTLS,
				BaseJob: BaseJob{
					Timeout: 5 * time.Second,
				},
			}

			result := job.Execute(context.Background())
			if result.Err != nil {
				t.Errorf("expected nil error for %s, got %v", method, result.Err)
			}
		})
	}
}
