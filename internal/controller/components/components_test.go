package components

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"cpra/internal/loader/schema"
)

func TestColorCode_Priority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		color    ColorCode
		expected uint8
	}{
		{ColorRed, 5},
		{ColorOrange, 4},
		{ColorYellow, 4},
		{ColorGreen, 2},
		{ColorCyan, 2},
		{ColorBlue, 1},
		{ColorPurple, 1},
		{ColorGray, 0},
		{ColorNone, 0},     // Out of range
		{MaxColors, 0},     // Out of range
		{MaxColors + 1, 0}, // Out of range
	}

	for _, tt := range tests {
		got := tt.color.Priority()
		if got != tt.expected {
			t.Errorf("ColorCode(%d).Priority() = %d, want %d", tt.color, got, tt.expected)
		}
	}
}

func TestColorCode_HigherPriorityThan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b     ColorCode
		expected bool
	}{
		{ColorRed, ColorGray, true},
		{ColorRed, ColorYellow, true},
		{ColorYellow, ColorRed, false},
		{ColorYellow, ColorOrange, false}, // Same priority
		{ColorGray, ColorRed, false},
		{ColorGreen, ColorCyan, false}, // Same priority
	}

	for _, tt := range tests {
		got := tt.a.HigherPriorityThan(tt.b)
		if got != tt.expected {
			t.Errorf("%s.HigherPriorityThan(%s) = %v, want %v",
				tt.a.String(), tt.b.String(), got, tt.expected)
		}
	}
}

func TestColorCode_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		color    ColorCode
		expected string
	}{
		{ColorRed, "red"},
		{ColorOrange, "orange"},
		{ColorYellow, "yellow"},
		{ColorGreen, "green"},
		{ColorCyan, "cyan"},
		{ColorBlue, "blue"},
		{ColorPurple, "purple"},
		{ColorGray, "gray"},
		{ColorNone, "none"},
		{MaxColors, "unknown"},
		{MaxColors + 1, "unknown"},
	}

	for _, tt := range tests {
		got := tt.color.String()
		if got != tt.expected {
			t.Errorf("ColorCode(%d).String() = %q, want %q", tt.color, got, tt.expected)
		}
	}
}

func TestColorToIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		color    string
		expected ColorCode
	}{
		{"red", ColorRed},
		{"orange", ColorOrange},
		{"yellow", ColorYellow},
		{"green", ColorGreen},
		{"cyan", ColorCyan},
		{"blue", ColorBlue},
		{"purple", ColorPurple},
		{"gray", ColorGray},
		{"unknown", ColorNone},
		{"", ColorNone},
		{"RED", ColorNone}, // Case sensitive
	}

	for _, tt := range tests {
		got := ColorToIndex(tt.color)
		if got != tt.expected {
			t.Errorf("ColorToIndex(%q) = %d, want %d", tt.color, got, tt.expected)
		}
	}
}

func TestMonitorState_Flags(t *testing.T) {
	t.Parallel()

	t.Run("PulseNeeded", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsPulseNeeded() {
			t.Error("expected IsPulseNeeded() = false initially")
		}
		m.SetPulseNeeded(true)
		if !m.IsPulseNeeded() {
			t.Error("expected IsPulseNeeded() = true after SetPulseNeeded(true)")
		}
		m.SetPulseNeeded(false)
		if m.IsPulseNeeded() {
			t.Error("expected IsPulseNeeded() = false after SetPulseNeeded(false)")
		}
	})

	t.Run("PulsePending", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsPulsePending() {
			t.Error("expected IsPulsePending() = false initially")
		}
		m.SetPulsePending(true)
		if !m.IsPulsePending() {
			t.Error("expected IsPulsePending() = true after SetPulsePending(true)")
		}
		m.SetPulsePending(false)
		if m.IsPulsePending() {
			t.Error("expected IsPulsePending() = false after SetPulsePending(false)")
		}
	})

	t.Run("PulseFirstCheck", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsPulseFirstCheck() {
			t.Error("expected IsPulseFirstCheck() = false initially")
		}
		m.SetPulseFirstCheck(true)
		if !m.IsPulseFirstCheck() {
			t.Error("expected IsPulseFirstCheck() = true after SetPulseFirstCheck(true)")
		}
	})

	t.Run("InterventionNeeded", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsInterventionNeeded() {
			t.Error("expected IsInterventionNeeded() = false initially")
		}
		m.SetInterventionNeeded(true)
		if !m.IsInterventionNeeded() {
			t.Error("expected IsInterventionNeeded() = true")
		}
	})

	t.Run("InterventionPending", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsInterventionPending() {
			t.Error("expected IsInterventionPending() = false initially")
		}
		m.SetInterventionPending(true)
		if !m.IsInterventionPending() {
			t.Error("expected IsInterventionPending() = true")
		}
	})

	t.Run("CodeNeeded", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsCodeNeeded() {
			t.Error("expected IsCodeNeeded() = false initially")
		}
		m.SetCodeNeeded(true)
		if !m.IsCodeNeeded() {
			t.Error("expected IsCodeNeeded() = true")
		}
	})

	t.Run("CodePending", func(t *testing.T) {
		m := &MonitorState{}
		if m.IsCodePending() {
			t.Error("expected IsCodePending() = false initially")
		}
		m.SetCodePending(true)
		if !m.IsCodePending() {
			t.Error("expected IsCodePending() = true")
		}
	})

	t.Run("MultipleFlags", func(t *testing.T) {
		m := &MonitorState{}
		m.SetPulseNeeded(true)
		m.SetCodeNeeded(true)
		m.SetInterventionPending(true)

		if !m.IsPulseNeeded() || !m.IsCodeNeeded() || !m.IsInterventionPending() {
			t.Error("expected all set flags to be true")
		}
		if m.IsPulsePending() || m.IsCodePending() || m.IsInterventionNeeded() {
			t.Error("expected unset flags to be false")
		}
	})
}

func TestColorCodeStatus(t *testing.T) {
	t.Parallel()

	t.Run("SetSuccess", func(t *testing.T) {
		s := &ColorCodeStatus{}
		now := time.Now()
		s.SetSuccess(now)

		if !s.IsSuccess() {
			t.Error("expected IsSuccess() = true after SetSuccess")
		}
		if s.ConsecutiveFailures != 0 {
			t.Errorf("expected ConsecutiveFailures = 0, got %d", s.ConsecutiveFailures)
		}
		if s.GetLastSuccessTime().Unix() != now.Unix() {
			t.Error("expected LastSuccessTime to match")
		}
		if s.GetLastAlertTime().Unix() != now.Unix() {
			t.Error("expected LastAlertTime to match")
		}
	})

	t.Run("SetFailure", func(t *testing.T) {
		s := &ColorCodeStatus{}
		s.SetFailure(nil)

		if s.IsSuccess() {
			t.Error("expected IsSuccess() = false after SetFailure")
		}
		if s.ConsecutiveFailures != 1 {
			t.Errorf("expected ConsecutiveFailures = 1, got %d", s.ConsecutiveFailures)
		}

		// Multiple failures
		for i := 0; i < 10; i++ {
			s.SetFailure(nil)
		}
		if s.ConsecutiveFailures != 11 {
			t.Errorf("expected ConsecutiveFailures = 11, got %d", s.ConsecutiveFailures)
		}
	})

	t.Run("ConsecutiveFailuresMax", func(t *testing.T) {
		s := &ColorCodeStatus{ConsecutiveFailures: 65534}
		s.SetFailure(nil)
		if s.ConsecutiveFailures != 65535 {
			t.Errorf("expected ConsecutiveFailures = 65535, got %d", s.ConsecutiveFailures)
		}
		// Should not overflow
		s.SetFailure(nil)
		if s.ConsecutiveFailures != 65535 {
			t.Errorf("expected ConsecutiveFailures to cap at 65535, got %d", s.ConsecutiveFailures)
		}
	})

	t.Run("GetLastAlertTime_Zero", func(t *testing.T) {
		s := &ColorCodeStatus{}
		if !s.GetLastAlertTime().IsZero() {
			t.Error("expected zero time when LastAlertTime is 0")
		}
	})

	t.Run("GetLastSuccessTime_Zero", func(t *testing.T) {
		s := &ColorCodeStatus{}
		if !s.GetLastSuccessTime().IsZero() {
			t.Error("expected zero time when LastSuccessTime is 0")
		}
	})

	t.Run("Copy", func(t *testing.T) {
		s := &ColorCodeStatus{
			LastAlertTime:       12345,
			LastSuccessTime:     67890,
			ConsecutiveFailures: 5,
			Flags:               StatusSuccess,
		}
		cpy := s.Copy()

		if cpy == s {
			t.Error("Copy() should return a different pointer")
		}
		if cpy.LastAlertTime != s.LastAlertTime {
			t.Error("LastAlertTime mismatch")
		}
		if cpy.ConsecutiveFailures != s.ConsecutiveFailures {
			t.Error("ConsecutiveFailures mismatch")
		}
		if cpy.Flags != s.Flags {
			t.Error("Flags mismatch")
		}
	})

	t.Run("CopyNil", func(t *testing.T) {
		var s *ColorCodeStatus
		if s.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})
}

func TestCodeStatus_Get(t *testing.T) {
	t.Parallel()

	s := &CodeStatus{}
	s.Status[ColorRed].ConsecutiveFailures = 5

	got := s.Get("red")
	if got == nil {
		t.Fatal("expected non-nil result for 'red'")
	}
	if got.ConsecutiveFailures != 5 {
		t.Errorf("expected ConsecutiveFailures = 5, got %d", got.ConsecutiveFailures)
	}

	// Invalid color
	if s.Get("invalid") != nil {
		t.Error("expected nil for invalid color")
	}
}

func TestCodeStatus_Copy(t *testing.T) {
	t.Parallel()

	s := &CodeStatus{}
	s.Status[ColorRed].ConsecutiveFailures = 3
	s.Status[ColorGreen].Flags = StatusSuccess

	cpy := s.Copy()
	if cpy == s {
		t.Error("Copy() should return different pointer")
	}
	if cpy.Status[ColorRed].ConsecutiveFailures != 3 {
		t.Error("Red status not copied correctly")
	}
	if cpy.Status[ColorGreen].Flags != StatusSuccess {
		t.Error("Green status not copied correctly")
	}

	// Verify independence
	cpy.Status[ColorRed].ConsecutiveFailures = 99
	if s.Status[ColorRed].ConsecutiveFailures == 99 {
		t.Error("Copy should be independent")
	}
}

func TestPulseConfig_Copy(t *testing.T) {
	t.Parallel()

	t.Run("NilCopy", func(t *testing.T) {
		var c *PulseConfig
		if c.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})

	t.Run("WithConfig", func(t *testing.T) {
		c := &PulseConfig{
			Type:               "http",
			Timeout:            5 * time.Second,
			Interval:           30 * time.Second,
			Retries:            3,
			UnhealthyThreshold: 3,
			HealthyThreshold:   2,
			Config:             &schema.PulseHTTPConfig{Url: "http://example.com"},
		}

		cpy := c.Copy()
		if cpy == c {
			t.Error("Copy() should return different pointer")
		}
		if cpy.Type != c.Type {
			t.Error("Type not copied")
		}
		if cpy.Timeout != c.Timeout {
			t.Error("Timeout not copied")
		}
		if cpy.Config == nil {
			t.Error("Config should be copied")
		}
	})

	t.Run("NilConfig", func(t *testing.T) {
		c := &PulseConfig{Type: "tcp"}
		cpy := c.Copy()
		if cpy.Config != nil {
			t.Error("nil Config should remain nil after copy")
		}
	})
}

func TestInterventionConfig_Copy(t *testing.T) {
	t.Parallel()

	t.Run("NilCopy", func(t *testing.T) {
		var c *InterventionConfig
		if c.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})

	t.Run("WithTarget", func(t *testing.T) {
		c := &InterventionConfig{
			Action:      "restart",
			MaxFailures: 5,
			Target:      &schema.InterventionTargetDocker{Container: "abc123", Type: "restart"},
		}

		cpy := c.Copy()
		if cpy == c {
			t.Error("Copy() should return different pointer")
		}
		if cpy.Action != c.Action {
			t.Error("Action not copied")
		}
		if cpy.MaxFailures != c.MaxFailures {
			t.Error("MaxFailures not copied")
		}
		if cpy.Target == nil {
			t.Error("Target should be copied")
		}
	})

	t.Run("NilTarget", func(t *testing.T) {
		c := &InterventionConfig{Action: "restart"}
		cpy := c.Copy()
		if cpy.Target != nil {
			t.Error("nil Target should remain nil after copy")
		}
	})
}

func TestColorCodeConfig_Copy(t *testing.T) {
	t.Parallel()

	t.Run("NilCopy", func(t *testing.T) {
		var c *ColorCodeConfig
		if c.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})

	t.Run("WithConfig", func(t *testing.T) {
		c := &ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Dispatch:    true,
			Config:      &schema.CodeNotificationLog{File: "/var/log/alerts.log"},
		}

		cpy := c.Copy()
		if cpy == c {
			t.Error("Copy() should return different pointer")
		}
		if cpy.Notify != c.Notify {
			t.Error("Notify not copied")
		}
		if cpy.Dispatch != c.Dispatch {
			t.Error("Dispatch not copied")
		}
		if cpy.Config == nil {
			t.Error("Config should be copied")
		}
	})
}

func TestCodeConfig_Copy(t *testing.T) {
	t.Parallel()

	t.Run("NilCopy", func(t *testing.T) {
		var c *CodeConfig
		if c.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})

	t.Run("WithConfigs", func(t *testing.T) {
		c := &CodeConfig{}
		c.Configs[ColorRed] = 1
		c.Configs[ColorGreen] = 2

		cpy := c.Copy()
		if cpy == c {
			t.Error("Copy() should return different pointer")
		}
		if cpy.Configs[ColorRed] != 1 || cpy.Configs[ColorGreen] != 2 {
			t.Error("Configs not copied correctly")
		}
	})
}

func TestCodeConfig_Get(t *testing.T) {
	t.Parallel()

	c := &CodeConfig{}
	// Get is deprecated and always returns nil
	if c.Get(ColorRed) != nil {
		t.Error("Get() should return nil (deprecated)")
	}
}

func TestJobStorage_Copy(t *testing.T) {
	t.Parallel()

	t.Run("NilCopy", func(t *testing.T) {
		var j *JobStorage
		if j.Copy() != nil {
			t.Error("Copy() of nil should return nil")
		}
	})

	t.Run("Empty", func(t *testing.T) {
		j := &JobStorage{}
		cpy := j.Copy()
		if cpy == j {
			t.Error("Copy() should return different pointer")
		}
		if cpy.PulseJob != nil || cpy.InterventionJob != nil {
			t.Error("Empty JobStorage should have nil jobs after copy")
		}
	})
}

func TestIndexToColor(t *testing.T) {
	t.Parallel()

	expected := []string{"red", "orange", "yellow", "green", "cyan", "blue", "purple", "gray"}
	for i, exp := range expected {
		if IndexToColor[i] != exp {
			t.Errorf("IndexToColor[%d] = %q, want %q", i, IndexToColor[i], exp)
		}
	}
}

// ============================================================================
// CONCURRENT FLAG MODIFICATION TESTS
// ============================================================================

// TestMonitorState_ConcurrentFlagModifications documents that MonitorState flag
// operations are NOT thread-safe by design. In ECS architecture, components are
// accessed single-threaded within the system update loop. Adding atomic operations
// would add unnecessary overhead for the 1M+ monitor scale requirement.
//
// This test is skipped because it would intentionally detect races in the
// non-thread-safe code (which is expected behavior for ECS components).
func TestMonitorState_ConcurrentFlagModifications(t *testing.T) {
	t.Skip("MonitorState is not designed for concurrent access - ECS components are accessed single-threaded within the update loop")

	t.Parallel()

	const goroutines = 10
	const iterations = 1000

	var wg sync.WaitGroup
	m := &MonitorState{}

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Different goroutines toggle different flags
				switch id % 3 {
				case 0:
					m.SetPulseNeeded(j%2 == 0)
				case 1:
					m.SetInterventionNeeded(j%2 == 0)
				case 2:
					m.SetCodeNeeded(j%2 == 0)
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestMonitorState_AllFlagsSet tests all flags set simultaneously
func TestMonitorState_AllFlagsSet(t *testing.T) {
	t.Parallel()

	m := &MonitorState{}

	// Set all flags
	m.SetPulseNeeded(true)
	m.SetPulsePending(true)
	m.SetPulseFirstCheck(true)
	m.SetInterventionNeeded(true)
	m.SetInterventionPending(true)
	m.SetCodeNeeded(true)
	m.SetCodePending(true)

	// Verify all are set
	if !m.IsPulseNeeded() || !m.IsPulsePending() || !m.IsPulseFirstCheck() ||
		!m.IsInterventionNeeded() || !m.IsInterventionPending() ||
		!m.IsCodeNeeded() || !m.IsCodePending() {
		t.Error("Not all flags set correctly")
	}

	// Clear one flag, verify others remain
	m.SetPulseNeeded(false)
	if m.IsPulseNeeded() {
		t.Error("PulseNeeded should be cleared")
	}
	if !m.IsPulsePending() || !m.IsPulseFirstCheck() {
		t.Error("Clearing one flag should not affect others")
	}
}

// TestMonitorState_WithError tests MonitorState with error field
func TestMonitorState_WithError(t *testing.T) {
	t.Parallel()

	m := &MonitorState{}
	testErr := fmt.Errorf("connection timeout")
	m.LastError = testErr

	if m.LastError == nil {
		t.Error("LastError should not be nil")
	}
	if m.LastError.Error() != "connection timeout" {
		t.Errorf("Expected error message 'connection timeout', got %q", m.LastError.Error())
	}
}

// ============================================================================
// TIME BOUNDARY TESTS
// ============================================================================

// TestColorCodeStatus_TimeBoundaries tests time boundary conditions
func TestColorCodeStatus_TimeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		timestamp   time.Time
		expectEmpty bool // true if we expect GetLastSuccessTime to return zero time
	}{
		// Zero time: stored as Unix() which is -62135596800, but GetLastSuccessTime
		// returns time.Time{} for LastSuccessTime == 0, so this behaves unexpectedly
		{"Zero Time", time.Time{}, true},
		// Unix Epoch: time.Unix(0, 0).Unix() == 0, GetLastSuccessTime treats 0 as "not set"
		{"Unix Epoch", time.Unix(0, 0), true},
		{"Far Future", time.Unix(1<<31-1, 0), false}, // Max int32 for Unix timestamp
		{"Recent Past", time.Now().Add(-24 * time.Hour), false},
		{"Now", time.Now(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ColorCodeStatus{}
			s.SetSuccess(tt.timestamp)

			retrieved := s.GetLastSuccessTime()
			if tt.expectEmpty {
				// These edge cases result in empty time due to how GetLastSuccessTime
				// treats 0 as "not set"
				if !retrieved.IsZero() {
					t.Errorf("Expected zero time for %s, got %v", tt.name, retrieved)
				}
			} else {
				if retrieved.Unix() != tt.timestamp.Unix() {
					t.Errorf("Time mismatch: got %v, want %v", retrieved.Unix(), tt.timestamp.Unix())
				}
			}
		})
	}
}

// TestColorCodeStatus_CopyDeepEquality verifies deep equality after copy
func TestColorCodeStatus_CopyDeepEquality(t *testing.T) {
	t.Parallel()

	s := &ColorCodeStatus{
		LastAlertTime:       12345,
		LastSuccessTime:     67890,
		ConsecutiveFailures: 5,
		Flags:               StatusSuccess,
	}
	cpy := s.Copy()

	// Verify pointer inequality
	if cpy == s {
		t.Error("Copy() should return a different pointer")
	}

	// Verify ALL fields are equal
	if cpy.LastAlertTime != s.LastAlertTime {
		t.Errorf("LastAlertTime mismatch: got %d, want %d", cpy.LastAlertTime, s.LastAlertTime)
	}
	if cpy.LastSuccessTime != s.LastSuccessTime {
		t.Errorf("LastSuccessTime mismatch: got %d, want %d", cpy.LastSuccessTime, s.LastSuccessTime)
	}
	if cpy.ConsecutiveFailures != s.ConsecutiveFailures {
		t.Errorf("ConsecutiveFailures mismatch: got %d, want %d", cpy.ConsecutiveFailures, s.ConsecutiveFailures)
	}
	if cpy.Flags != s.Flags {
		t.Errorf("Flags mismatch: got %d, want %d", cpy.Flags, s.Flags)
	}

	// Verify independence - modifying copy doesn't affect original
	cpy.ConsecutiveFailures = 999
	if s.ConsecutiveFailures == 999 {
		t.Error("Copy should be independent of original")
	}
}

// TestColorCode_PriorityOrdering tests priority ordering invariants
func TestColorCode_PriorityOrdering(t *testing.T) {
	t.Parallel()

	// Red should be highest priority
	if !ColorRed.HigherPriorityThan(ColorOrange) {
		t.Error("Red should have higher priority than Orange")
	}
	if !ColorRed.HigherPriorityThan(ColorGray) {
		t.Error("Red should have higher priority than Gray")
	}

	// Same priority colors should not report higher priority
	if ColorYellow.HigherPriorityThan(ColorOrange) {
		t.Error("Equal priority colors (Yellow/Orange) should not be 'higher' than each other")
	}
	if ColorOrange.HigherPriorityThan(ColorYellow) {
		t.Error("Equal priority colors (Orange/Yellow) should not be 'higher' than each other")
	}

	// Lower priority should not be higher than higher priority
	if ColorGray.HigherPriorityThan(ColorRed) {
		t.Error("Gray should not have higher priority than Red")
	}
}

// ============================================================================
// ZERO VALUE TESTS
// ============================================================================

// TestMonitorState_ZeroValue tests operations on zero-valued MonitorState
func TestMonitorState_ZeroValue(t *testing.T) {
	t.Parallel()

	var m MonitorState

	// All flags should be false initially
	if m.IsPulseNeeded() || m.IsPulsePending() || m.IsPulseFirstCheck() ||
		m.IsInterventionNeeded() || m.IsInterventionPending() ||
		m.IsCodeNeeded() || m.IsCodePending() {
		t.Error("Zero-valued MonitorState should have all flags false")
	}

	// Name should be empty
	if m.Name != "" {
		t.Errorf("Zero-valued MonitorState.Name should be empty, got %q", m.Name)
	}

	// Counters should be zero
	if m.ConsecutiveFailures != 0 || m.PulseFailures != 0 || m.InterventionFailures != 0 {
		t.Error("Zero-valued MonitorState should have zero counters")
	}

	// Times should be zero
	if !m.LastPulseCheckTime.IsZero() || !m.LastEventTime.IsZero() {
		t.Error("Zero-valued MonitorState should have zero times")
	}
}

// TestCodeStatus_CopyIndependence verifies copy is truly independent
func TestCodeStatus_CopyIndependence(t *testing.T) {
	t.Parallel()

	original := &CodeStatus{}
	original.Status[ColorRed].ConsecutiveFailures = 5
	original.Status[ColorGreen].Flags = StatusSuccess

	cpy := original.Copy()

	// Modify copy
	cpy.Status[ColorRed].ConsecutiveFailures = 999
	cpy.Status[ColorGreen].Flags = StatusHasError

	// Original should be unchanged
	if original.Status[ColorRed].ConsecutiveFailures != 5 {
		t.Error("Original ColorRed status was modified by copy")
	}
	if original.Status[ColorGreen].Flags != StatusSuccess {
		t.Error("Original ColorGreen flags was modified by copy")
	}
}

// ============================================================================
// BENCHMARK TESTS
// ============================================================================

func BenchmarkMonitorState_SetFlags(b *testing.B) {
	m := &MonitorState{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.SetPulseNeeded(true)
		m.SetPulsePending(true)
		m.SetCodeNeeded(true)
		m.SetPulseNeeded(false)
	}
}

func BenchmarkColorCodeStatus_Copy(b *testing.B) {
	s := &ColorCodeStatus{
		LastAlertTime:       12345,
		LastSuccessTime:     67890,
		ConsecutiveFailures: 5,
		Flags:               StatusSuccess,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Copy()
	}
}

func BenchmarkColorCode_Priority(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ColorRed.Priority()
		_ = ColorRed.HigherPriorityThan(ColorGray)
	}
}
