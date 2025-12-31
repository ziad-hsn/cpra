package controller

import (
	"context"
	"testing"
	"time"
)

// TestDefaultConfig tests that DefaultConfig returns correct defaults
func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.QueueCapacity != 8192 {
		t.Errorf("QueueCapacity = %d, want 8192", cfg.QueueCapacity)
	}
	if cfg.BatchSize != 1000 {
		t.Errorf("BatchSize = %d, want 1000", cfg.BatchSize)
	}
	if cfg.ShardTargetSweep != 10*time.Second {
		t.Errorf("ShardTargetSweep = %v, want 10s", cfg.ShardTargetSweep)
	}
	// WorkerConfig should have sensible defaults
	if cfg.WorkerConfig.MinWorkers < 1 {
		t.Error("WorkerConfig.MinWorkers should be at least 1")
	}
}

// TestNewController tests basic controller creation
func TestNewController(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	if ctrl == nil {
		t.Fatal("NewController returned nil controller")
	}

	// Check world is initialized
	if ctrl.World() == nil {
		t.Error("World() returned nil")
	}

	// Check queues are initialized
	if ctrl.pulseQueue == nil {
		t.Error("pulseQueue is nil")
	}
	if ctrl.interventionQueue == nil {
		t.Error("interventionQueue is nil")
	}
	if ctrl.codeQueue == nil {
		t.Error("codeQueue is nil")
	}

	// Check worker pools are initialized
	if ctrl.pulsePool == nil {
		t.Error("pulsePool is nil")
	}
	if ctrl.interventionPool == nil {
		t.Error("interventionPool is nil")
	}
	if ctrl.codePool == nil {
		t.Error("codePool is nil")
	}
}

// TestController_Stats tests that Stats returns valid data
func TestController_Stats(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	stats := ctrl.Stats()

	// Stats should have valid values
	if stats.World == nil {
		t.Error("Stats.World is nil")
	}
}

// TestCalculateShardSlots tests shard slot calculation
func TestCalculateShardSlots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		tps         float64
		targetSweep time.Duration
		override    int
		wantMin     int
		wantMax     int
	}{
		{
			name:        "override value",
			tps:         10,
			targetSweep: 10 * time.Second,
			override:    50,
			wantMin:     50,
			wantMax:     50,
		},
		{
			name:        "calculated from TPS",
			tps:         10,
			targetSweep: 10 * time.Second,
			override:    0,
			wantMin:     100,
			wantMax:     100,
		},
		{
			name:        "default sweep if zero",
			tps:         10,
			targetSweep: 0,
			override:    0,
			wantMin:     100,
			wantMax:     100,
		},
		{
			name:        "minimum of 1",
			tps:         0.01,
			targetSweep: 100 * time.Millisecond,
			override:    0,
			wantMin:     1,
			wantMax:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateShardSlots(tt.tps, tt.targetSweep, tt.override)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("calculateShardSlots() = %d, want between %d and %d", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestController_StartStop tests controller lifecycle
func TestController_StartStop(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// Should not be running initially
	if ctrl.running.Load() {
		t.Error("Controller should not be running initially")
	}

	// Stop should be safe to call before start (idempotent)
	ctrl.Stop()

	// After stop (even if never started), should remain stopped
	if ctrl.running.Load() {
		t.Error("Controller should not be running after Stop()")
	}
}

// TestController_DoubleStart tests that double-start returns error
func TestController_DoubleStart(t *testing.T) {
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}
	defer ctrl.Stop()

	// First start should succeed
	if err := ctrl.Start(nil); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}

	// Second start should fail
	if err := ctrl.Start(nil); err == nil {
		t.Error("Expected error on double-start, got nil")
	}
}

// TestConfig_SizingDefaults tests that sizing defaults are correct
func TestConfig_SizingDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	// Default sizing values should be zero (will use env or package defaults)
	if cfg.SizingServiceTime != 0 {
		t.Errorf("SizingServiceTime = %v, want 0 (uses default)", cfg.SizingServiceTime)
	}
	if cfg.SizingSLO != 0 {
		t.Errorf("SizingSLO = %v, want 0 (uses default)", cfg.SizingSLO)
	}
	if cfg.SizingHeadroomPct != 0 {
		t.Errorf("SizingHeadroomPct = %f, want 0 (uses default)", cfg.SizingHeadroomPct)
	}
}

// TestController_World tests World() accessor
func TestController_World(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	world := ctrl.World()
	if world == nil {
		t.Fatal("World() returned nil")
	}

	// World should have valid stats
	stats := world.Stats()
	if stats == nil {
		t.Error("World.Stats() returned nil")
	}
}

// TestController_LoadMonitors tests loading monitors from YAML file
func TestController_LoadMonitors(t *testing.T) {
	// Note: Not using t.Parallel() to avoid race with controller internal state
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx := context.Background()
	err = ctrl.LoadMonitors(ctx, "testdata/test_monitors.yaml")
	if err != nil {
		t.Fatalf("LoadMonitors failed: %v", err)
	}

	// Check that entities were created
	worldStats := ctrl.World().Stats()
	if worldStats.Entities.Used < 2 {
		t.Errorf("Expected at least 2 entities (monitors), got %d", worldStats.Entities.Used)
	}
}

// TestController_LoadMonitors_NonExistent tests loading from non-existent file
func TestController_LoadMonitors_NonExistent(t *testing.T) {
	// Note: Not using t.Parallel() to avoid race with controller internal state
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx := context.Background()
	err = ctrl.LoadMonitors(ctx, "testdata/nonexistent.yaml")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

// TestController_Stats_QueueStats tests queue statistics
func TestController_Stats_QueueStats(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	stats := ctrl.Stats()

	// All queues should have non-negative capacity
	if stats.PulseQueue.Capacity <= 0 {
		t.Error("PulseQueue.Capacity should be positive")
	}
	if stats.InterventionQueue.Capacity <= 0 {
		t.Error("InterventionQueue.Capacity should be positive")
	}
	if stats.CodeQueue.Capacity <= 0 {
		t.Error("CodeQueue.Capacity should be positive")
	}
}

// TestController_Stats_WorkerStats tests worker pool statistics
func TestController_Stats_WorkerStats(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	stats := ctrl.Stats()

	// Worker pools should have min workers configured
	if stats.PulseWorkers.MinWorkers <= 0 {
		t.Error("PulseWorkers.MinWorkers should be positive")
	}
	if stats.InterventionWorkers.MinWorkers <= 0 {
		t.Error("InterventionWorkers.MinWorkers should be positive")
	}
	if stats.CodeWorkers.MinWorkers <= 0 {
		t.Error("CodeWorkers.MinWorkers should be positive")
	}
}

// TestCalculateShardSlots_MaxSlots tests shard slot clamping
func TestCalculateShardSlots_MaxSlots(t *testing.T) {
	t.Parallel()
	// Very high TPS should clamp to maxSlots (20000)
	got := calculateShardSlots(10000, 10*time.Second, 0)
	if got > 20000 {
		t.Errorf("calculateShardSlots() = %d, should be clamped to 20000", got)
	}
}

// TestController_StartStop_WithContext tests start with context
func TestController_StartStop_WithContext(t *testing.T) {
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with context
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Should be running
	if !ctrl.running.Load() {
		t.Error("Controller should be running after Start()")
	}

	// Give the controller time to fully initialize its goroutines
	time.Sleep(100 * time.Millisecond)

	// Stop
	ctrl.Stop()

	// Should not be running after stop
	if ctrl.running.Load() {
		t.Error("Controller should not be running after Stop()")
	}
}

// TestController_EntityManager tests entity manager access
func TestController_EntityManager(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// Entity manager should be accessible
	em := ctrl.mapper
	if em == nil {
		t.Error("EntityManager is nil")
	}
}

// TestController_QueueTypes tests queue types are initialized correctly
func TestController_QueueTypes(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// All queues should be non-nil
	if ctrl.pulseQueue == nil {
		t.Error("pulseQueue is nil")
	}
	if ctrl.interventionQueue == nil {
		t.Error("interventionQueue is nil")
	}
	if ctrl.codeQueue == nil {
		t.Error("codeQueue is nil")
	}
}

// TestController_PoolConfigs tests worker pool configurations
func TestController_PoolConfigs(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// Check pulse pool is initialized
	if ctrl.pulsePool == nil {
		t.Error("PulsePool is nil")
	}
	if ctrl.interventionPool == nil {
		t.Error("InterventionPool is nil")
	}
	if ctrl.codePool == nil {
		t.Error("CodePool is nil")
	}
}

// TestDefaultConfig_WorkerConfig tests worker config defaults
func TestDefaultConfig_WorkerConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.WorkerConfig.MinWorkers < 1 {
		t.Error("WorkerConfig.MinWorkers should be at least 1")
	}
	if cfg.WorkerConfig.MaxWorkers < cfg.WorkerConfig.MinWorkers {
		t.Error("WorkerConfig.MaxWorkers should be >= MinWorkers")
	}
	if cfg.WorkerConfig.AdjustmentInterval <= 0 {
		t.Error("WorkerConfig.AdjustmentInterval should be positive")
	}
	if cfg.WorkerConfig.ResultBatchSize <= 0 {
		t.Error("WorkerConfig.ResultBatchSize should be positive")
	}
}

// TestController_StatsFields tests Stats returns all expected fields
func TestController_StatsFields(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	stats := ctrl.Stats()

	// World should be initialized
	if stats.World == nil {
		t.Error("Stats.World is nil")
	}

	// Queue stats should have valid capacities
	if stats.PulseQueue.Capacity == 0 {
		t.Error("Stats.PulseQueue.Capacity is 0")
	}
	if stats.InterventionQueue.Capacity == 0 {
		t.Error("Stats.InterventionQueue.Capacity is 0")
	}
	if stats.CodeQueue.Capacity == 0 {
		t.Error("Stats.CodeQueue.Capacity is 0")
	}
}
