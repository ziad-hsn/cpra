package systems

import (
	"strings"
	"sync"
	"testing"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

func TestNewStateLogger_DebugMode(t *testing.T) {
	t.Parallel()

	logger := NewStateLogger(true)
	if logger == nil {
		t.Fatal("NewStateLogger returned nil")
	}
	if !logger.debugMode {
		t.Error("debugMode should be true")
	}
	if logger.logger == nil {
		t.Error("logger should not be nil")
	}
}

func TestNewStateLogger_ProductionMode(t *testing.T) {
	t.Parallel()

	logger := NewStateLogger(false)
	if logger == nil {
		t.Fatal("NewStateLogger returned nil")
	}
	if logger.debugMode {
		t.Error("debugMode should be false")
	}
	if logger.logger == nil {
		t.Error("logger should not be nil (uses noop handler)")
	}
}

func TestStateLogger_LogTransition_DebugMode(t *testing.T) {
	t.Parallel()

	logger := NewStateLogger(true)

	entity := ecs.Entity{}
	oldState := components.MonitorState{Name: "test", Flags: 0}
	newState := components.MonitorState{Name: "test", Flags: components.StatePulseNeeded}

	// Should not panic
	logger.LogTransition(entity, oldState, newState)
}

func TestStateLogger_LogTransition_ProductionMode(t *testing.T) {
	t.Parallel()

	logger := NewStateLogger(false)

	entity := ecs.Entity{}
	oldState := components.MonitorState{Name: "test", Flags: 0}
	newState := components.MonitorState{Name: "test", Flags: components.StatePulseNeeded}

	// Should not panic - no-op in production mode
	logger.LogTransition(entity, oldState, newState)
}

func TestFormatState_Empty(t *testing.T) {
	t.Parallel()

	result := formatState(0)
	if result != "Idle" {
		t.Errorf("formatState(0) = %q, want %q", result, "Idle")
	}
}

func TestFormatState_SingleFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flag     uint32
		expected string
	}{
		{components.StatePulseNeeded, "[PulseNeeded]"},
		{components.StatePulsePending, "[PulsePending]"},
		{components.StateInterventionNeeded, "[InterventionNeeded]"},
		{components.StateInterventionPending, "[InterventionPending]"},
		{components.StateCodeNeeded, "[CodeNeeded]"},
		{components.StateCodePending, "[CodePending]"},
		{components.StateIncidentOpen, "[IncidentOpen]"},
		{components.StateVerifying, "[Verifying]"},
	}

	for _, tt := range tests {
		result := formatState(tt.flag)
		if result != tt.expected {
			t.Errorf("formatState(%d) = %q, want %q", tt.flag, result, tt.expected)
		}
	}
}

func TestFormatState_MultipleFlags(t *testing.T) {
	t.Parallel()

	flags := components.StatePulseNeeded | components.StateCodeNeeded | components.StateIncidentOpen
	result := formatState(flags)

	// Should contain all three states
	if result == "Idle" {
		t.Error("expected non-Idle result for multiple flags")
	}
	// The result is formatted as "[state1 state2 state3]"
	// We just check it's not empty/Idle
}

func TestNoopWriter_Write(t *testing.T) {
	t.Parallel()

	w := &noopWriter{}

	data := []byte("test data")
	n, err := w.Write(data)

	if err != nil {
		t.Errorf("noopWriter.Write returned error: %v", err)
	}
	if n != len(data) {
		t.Errorf("noopWriter.Write returned %d, want %d", n, len(data))
	}
}

func TestNoopWriter_WriteEmpty(t *testing.T) {
	t.Parallel()

	w := &noopWriter{}

	n, err := w.Write(nil)

	if err != nil {
		t.Errorf("noopWriter.Write returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("noopWriter.Write returned %d, want 0", n)
	}
}

// ============================================================================
// CONCURRENT ACCESS TESTS
// ============================================================================

// TestStateLogger_ConcurrentLogTransition tests concurrent logging
func TestStateLogger_ConcurrentLogTransition(t *testing.T) {
	t.Parallel()

	// Use production mode (no-op logger) to avoid stdout spam
	logger := NewStateLogger(false)

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				entity := ecs.Entity{}
				oldState := components.MonitorState{Name: "monitor", Flags: uint32(i % 8)}
				newState := components.MonitorState{Name: "monitor", Flags: uint32((i + 1) % 8)}
				logger.LogTransition(entity, oldState, newState)
			}
		}(g)
	}

	wg.Wait()
	// Test passes if no race detected with -race flag
}

// TestFormatState_AllFlagsSet tests formatState with all flags set
func TestFormatState_AllFlagsSet(t *testing.T) {
	t.Parallel()

	allFlags := components.StatePulseNeeded |
		components.StatePulsePending |
		components.StatePulseFirstCheck |
		components.StateInterventionNeeded |
		components.StateInterventionPending |
		components.StateCodeNeeded |
		components.StateCodePending |
		components.StateIncidentOpen |
		components.StateVerifying

	result := formatState(allFlags)

	if result == "Idle" {
		t.Error("formatState with all flags should not return Idle")
	}

	// Verify all state names are present
	expectedStates := []string{
		"PulseNeeded",
		"PulsePending",
		"InterventionNeeded",
		"InterventionPending",
		"CodeNeeded",
		"CodePending",
		"IncidentOpen",
		"Verifying",
	}

	for _, state := range expectedStates {
		if !strings.Contains(result, state) {
			t.Errorf("formatState result %q should contain %q", result, state)
		}
	}
}

// TestFormatState_UnknownFlags tests formatState with unknown flag values
func TestFormatState_UnknownFlags(t *testing.T) {
	t.Parallel()

	// Use a high flag value that doesn't correspond to any known flag
	unknownFlag := uint32(1 << 20) // A flag beyond the defined ones

	result := formatState(unknownFlag)

	// Should return Idle since unknown flag is not in the known list
	if result != "Idle" {
		t.Errorf("formatState with unknown flag should return Idle, got %q", result)
	}
}

// TestFormatState_CombinedWithUnknown tests known flags combined with unknown
func TestFormatState_CombinedWithUnknown(t *testing.T) {
	t.Parallel()

	// Combine known flags with an unknown flag
	flags := components.StatePulseNeeded | uint32(1<<20)

	result := formatState(flags)

	// Should still show the known flag
	if !strings.Contains(result, "PulseNeeded") {
		t.Errorf("formatState should contain PulseNeeded, got %q", result)
	}
}

// TestNoopWriter_ConcurrentWrite tests concurrent writes to noopWriter
func TestNoopWriter_ConcurrentWrite(t *testing.T) {
	t.Parallel()

	w := &noopWriter{}
	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				data := []byte("concurrent test data")
				n, err := w.Write(data)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if n != len(data) {
					t.Errorf("expected %d, got %d", len(data), n)
				}
			}
		}()
	}

	wg.Wait()
}

// TestStateLogger_MixedModes tests creating loggers in different modes
func TestStateLogger_MixedModes(t *testing.T) {
	t.Parallel()

	// Create multiple loggers with different modes
	debugLogger := NewStateLogger(true)
	prodLogger := NewStateLogger(false)

	entity := ecs.Entity{}
	oldState := components.MonitorState{Name: "test", Flags: 0}
	newState := components.MonitorState{Name: "test", Flags: components.StatePulseNeeded}

	// Both should work without panicking
	prodLogger.LogTransition(entity, oldState, newState)
	debugLogger.LogTransition(entity, oldState, newState)
}

// ============================================================================
// BENCHMARKS
// ============================================================================

func BenchmarkFormatState_Empty(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = formatState(0)
	}
}

func BenchmarkFormatState_SingleFlag(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = formatState(components.StatePulseNeeded)
	}
}

func BenchmarkFormatState_MultipleFlags(b *testing.B) {
	flags := components.StatePulseNeeded | components.StateCodeNeeded | components.StateIncidentOpen
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = formatState(flags)
	}
}

func BenchmarkStateLogger_LogTransition_Production(b *testing.B) {
	logger := NewStateLogger(false)
	entity := ecs.Entity{}
	oldState := components.MonitorState{Name: "test", Flags: 0}
	newState := components.MonitorState{Name: "test", Flags: components.StatePulseNeeded}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.LogTransition(entity, oldState, newState)
	}
}

func BenchmarkNoopWriter_Write(b *testing.B) {
	w := &noopWriter{}
	data := []byte("benchmark test data for noop writer")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = w.Write(data)
	}
}
