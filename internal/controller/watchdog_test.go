package controller

import (
	"context"
	"testing"
	"time"

	"cpra/internal/logger"
)

// TestDefaultWatchdogConfig tests that defaults are correct
func TestDefaultWatchdogConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultWatchdogConfig()

	if !cfg.Enabled {
		t.Error("Enabled should be true by default")
	}
	if cfg.CheckInterval != 10*time.Second {
		t.Errorf("CheckInterval = %v, want 10s", cfg.CheckInterval)
	}
	if cfg.TickStallThreshold != 20 {
		t.Errorf("TickStallThreshold = %d, want 20", cfg.TickStallThreshold)
	}
	if cfg.PoolStallThreshold != 6 {
		t.Errorf("PoolStallThreshold = %d, want 6", cfg.PoolStallThreshold)
	}
	if cfg.RestartEnabled {
		t.Error("RestartEnabled should be false by default (v1)")
	}
}

// TestComponentStatus tests status constants
func TestComponentStatus(t *testing.T) {
	t.Parallel()
	// Verify status values are distinct
	if StatusHealthy == StatusDegraded {
		t.Error("StatusHealthy should not equal StatusDegraded")
	}
	if StatusDegraded == StatusUnhealthy {
		t.Error("StatusDegraded should not equal StatusUnhealthy")
	}
	if StatusHealthy == StatusUnhealthy {
		t.Error("StatusHealthy should not equal StatusUnhealthy")
	}

	// Verify ordering (Healthy < Degraded < Unhealthy)
	if StatusHealthy >= StatusDegraded {
		t.Error("StatusHealthy should be less than StatusDegraded")
	}
	if StatusDegraded >= StatusUnhealthy {
		t.Error("StatusDegraded should be less than StatusUnhealthy")
	}
}

// TestNewWatchdog tests watchdog creation
func TestNewWatchdog(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// Create test logger
	// Create test logger
	testLogger := createTestLogger(t, "WATCHDOG-TEST")

	heartbeat := make(chan struct{}, 1)
	watchdogCfg := DefaultWatchdogConfig()

	wd := NewWatchdog(ctrl, watchdogCfg, heartbeat, testLogger)
	if wd == nil {
		t.Fatal("NewWatchdog returned nil")
	}

	if wd.controller != ctrl {
		t.Error("Watchdog controller not set correctly")
	}
	if wd.config.Enabled != watchdogCfg.Enabled {
		t.Error("Watchdog config not set correctly")
	}
	if len(wd.poolStates) != 3 {
		t.Errorf("Expected 3 pool states, got %d", len(wd.poolStates))
	}
}

// TestWatchdog_Stop tests that Stop is idempotent
func TestWatchdog_Stop(t *testing.T) {
	// Note: Not using t.Parallel() to avoid race with controller internal state
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	testLogger := createTestLogger(t, "WATCHDOG-TEST")
	heartbeat := make(chan struct{}, 1)

	wd := NewWatchdog(ctrl, DefaultWatchdogConfig(), heartbeat, testLogger)

	// Stop before Run should be safe (no-op)
	wd.Stop()
	wd.Stop() // Double stop should also be safe
}

// TestWatchdog_RunContextCancel tests that Run respects context cancellation
func TestWatchdog_RunContextCancel(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	testLogger := createTestLogger(t, "WATCHDOG-TEST")
	heartbeat := make(chan struct{}, 10)

	watchdogCfg := DefaultWatchdogConfig()
	watchdogCfg.CheckInterval = 50 * time.Millisecond // Fast for testing

	wd := NewWatchdog(ctrl, watchdogCfg, heartbeat, testLogger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- wd.Run(ctx)
	}()

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()

	// Should exit cleanly
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Watchdog did not stop after context cancel")
	}

	// Ensure watchdog is fully stopped
	wd.Stop()
}

// TestWatchdog_Heartbeat tests that heartbeat signals are sent
func TestWatchdog_Heartbeat(t *testing.T) {
	// Note: Not using t.Parallel() to avoid race with controller internal state
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	testLogger := createTestLogger(t, "WATCHDOG-TEST")
	heartbeat := make(chan struct{}, 10)

	watchdogCfg := DefaultWatchdogConfig()
	watchdogCfg.CheckInterval = 50 * time.Millisecond // Fast for testing

	wd := NewWatchdog(ctrl, watchdogCfg, heartbeat, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go wd.Run(ctx)

	// Wait for at least one heartbeat
	select {
	case <-heartbeat:
		// Good, got a heartbeat
	case <-time.After(500 * time.Millisecond):
		t.Fatal("No heartbeat received within timeout")
	}
}

// TestWatchdog_DoubleRun tests that double-run is handled
func TestWatchdog_DoubleRun(t *testing.T) {
	// Note: Not using t.Parallel() to avoid race with controller internal state
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	testLogger := createTestLogger(t, "WATCHDOG-TEST")
	heartbeat := make(chan struct{}, 10)

	watchdogCfg := DefaultWatchdogConfig()
	watchdogCfg.CheckInterval = 100 * time.Millisecond

	wd := NewWatchdog(ctrl, watchdogCfg, heartbeat, testLogger)

	ctx, cancel := context.WithCancel(context.Background())

	// First run in background
	firstDone := make(chan struct{})
	go func() {
		wd.Run(ctx)
		close(firstDone)
	}()
	time.Sleep(50 * time.Millisecond)

	// Second run should return immediately (already running)
	secondDone := make(chan struct{})
	go func() {
		wd.Run(ctx) // Should return immediately
		close(secondDone)
	}()

	select {
	case <-secondDone:
		// Good, second Run returned
	case <-time.After(200 * time.Millisecond):
		t.Error("Second Run did not return immediately")
	}

	// Clean shutdown
	cancel()
	<-firstDone
	wd.Stop()
}

// TestPoolState tests pool state initialization
func TestPoolState(t *testing.T) {
	t.Parallel()
	state := &poolState{}

	// Initial values should be zero
	if state.lastCompleted != 0 {
		t.Error("lastCompleted should be 0 initially")
	}
	if state.stallCount != 0 {
		t.Error("stallCount should be 0 initially")
	}
	if state.lastStatus != StatusHealthy {
		t.Error("lastStatus should be StatusHealthy initially (zero value)")
	}
}

func createTestLogger(t *testing.T, component string) logger.Logger {
	l, err := logger.NewZapLogger(logger.DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	return l.With(logger.Field{Key: "component", Value: component})
}
