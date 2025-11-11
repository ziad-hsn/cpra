package systems

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// ErrPulseJobTimeout Tests
// ============================================================================

func TestErrPulseJobTimeout_Error(t *testing.T) {
	t.Parallel()

	err := &ErrPulseJobTimeout{
		Err:       errors.New("connection refused"),
		PulseType: "http",
		Timeout:   5 * time.Second,
		Retries:   3,
	}

	msg := err.Error()

	// Verify the error message contains all expected parts
	if !strings.Contains(msg, "http") {
		t.Error("error message should contain pulse type")
	}
	if !strings.Contains(msg, "5s") {
		t.Error("error message should contain timeout")
	}
	if !strings.Contains(msg, "3") {
		t.Error("error message should contain retry count")
	}
	if !strings.Contains(msg, "connection refused") {
		t.Error("error message should contain underlying error")
	}
}

func TestErrPulseJobTimeout_Error_ZeroValues(t *testing.T) {
	t.Parallel()

	err := &ErrPulseJobTimeout{
		Err:       nil,
		PulseType: "",
		Timeout:   0,
		Retries:   0,
	}

	// Should not panic with zero values
	msg := err.Error()
	if msg == "" {
		t.Error("error message should not be empty")
	}
}

func TestErrPulseJobTimeout_Error_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         *ErrPulseJobTimeout
		contains    []string
		notContains []string
	}{
		{
			name: "HTTP timeout with underlying error",
			err: &ErrPulseJobTimeout{
				Err:       errors.New("dial tcp: connection refused"),
				PulseType: "http",
				Timeout:   10 * time.Second,
				Retries:   5,
			},
			contains: []string{"http", "10s", "5", "dial tcp"},
		},
		{
			name: "TCP timeout",
			err: &ErrPulseJobTimeout{
				Err:       errors.New("i/o timeout"),
				PulseType: "tcp",
				Timeout:   30 * time.Second,
				Retries:   0,
			},
			contains: []string{"tcp", "30s", "0", "timeout"},
		},
		{
			name: "ICMP timeout",
			err: &ErrPulseJobTimeout{
				Err:       errors.New("no reply"),
				PulseType: "icmp",
				Timeout:   2 * time.Second,
				Retries:   10,
			},
			contains: []string{"icmp", "2s", "10", "no reply"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, substr := range tt.contains {
				if !strings.Contains(msg, substr) {
					t.Errorf("expected error message to contain %q, got %q", substr, msg)
				}
			}
			for _, substr := range tt.notContains {
				if strings.Contains(msg, substr) {
					t.Errorf("expected error message NOT to contain %q, got %q", substr, msg)
				}
			}
		})
	}
}

// ============================================================================
// ErrNoPulseJob Tests
// ============================================================================

func TestErrNoPulseJob_ZeroValue(t *testing.T) {
	t.Parallel()

	var err ErrNoPulseJob

	// Zero value should have empty pulse type
	if err.pulseType != "" {
		t.Errorf("zero value ErrNoPulseJob.pulseType should be empty, got %q", err.pulseType)
	}
}

func TestErrNoPulseJob_WithPulseType(t *testing.T) {
	t.Parallel()

	err := ErrNoPulseJob{pulseType: "http"}

	if err.pulseType != "http" {
		t.Errorf("expected pulseType %q, got %q", "http", err.pulseType)
	}
}

func TestErrNoPulseJob_FieldAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pulseType string
	}{
		{"HTTP pulse", "http"},
		{"TCP pulse", "tcp"},
		{"ICMP pulse", "icmp"},
		{"Empty pulse type", ""},
		{"Unknown pulse type", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ErrNoPulseJob{pulseType: tt.pulseType}
			if err.pulseType != tt.pulseType {
				t.Errorf("expected pulseType %q, got %q", tt.pulseType, err.pulseType)
			}
		})
	}
}

// ============================================================================
// Error Unwrapping Tests
// ============================================================================

func TestErrPulseJobTimeout_Unwrap(t *testing.T) {
	t.Parallel()

	underlying := errors.New("root cause: network unreachable")
	err := &ErrPulseJobTimeout{
		Err:       underlying,
		PulseType: "http",
		Timeout:   5 * time.Second,
		Retries:   3,
	}

	// Verify error message contains the underlying error
	if !strings.Contains(err.Error(), "root cause: network unreachable") {
		t.Error("Error() should include underlying error message")
	}
}

func TestErrPulseJobTimeout_NilUnderlying(t *testing.T) {
	t.Parallel()

	err := &ErrPulseJobTimeout{
		Err:       nil,
		PulseType: "http",
		Timeout:   5 * time.Second,
		Retries:   3,
	}

	// Should not panic and should produce valid string
	msg := err.Error()
	if !strings.Contains(msg, "http") {
		t.Error("Error() should still contain pulse type with nil underlying error")
	}
}

func TestErrPulseJobTimeout_WrappedError(t *testing.T) {
	t.Parallel()

	// Create a chain of wrapped errors
	rootErr := errors.New("connection refused")
	wrappedErr := errors.Join(errors.New("dial failed"), rootErr)

	err := &ErrPulseJobTimeout{
		Err:       wrappedErr,
		PulseType: "http",
		Timeout:   5 * time.Second,
		Retries:   3,
	}

	msg := err.Error()
	// The wrapped error's string representation should be included
	if !strings.Contains(msg, "dial failed") && !strings.Contains(msg, "connection refused") {
		t.Error("Error() should include wrapped error messages")
	}
}

// ============================================================================
// Concurrent Access Tests
// ============================================================================

func TestErrPulseJobTimeout_ConcurrentRead(t *testing.T) {
	t.Parallel()

	err := &ErrPulseJobTimeout{
		Err:       errors.New("test error"),
		PulseType: "http",
		Timeout:   5 * time.Second,
		Retries:   3,
	}

	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = err.Error()
			}
			done <- true
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}
	// Test passes if no race detected with -race flag
}
