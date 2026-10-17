package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
)

func TestStartPreservesLatencyAwareInitialCapacity(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	t.Setenv("CPRA_SIZING_TAU_MS", "")
	t.Setenv("CPRA_SIZING_SLO_MS", "")
	t.Setenv("CPRA_SIZING_HEADROOM_PCT", "")
	cfg := controllerTestConfig(t)
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 64, 1
	cfg.WorkerConfig.AdjustmentInterval = 0
	cfg.SizingServiceTime = 100 * time.Millisecond
	cfg.SizingSLO = 110 * time.Millisecond
	c := NewController(cfg)
	defer c.Stop()
	file := filepath.Join(t.TempDir(), "monitors.yaml")
	data := []byte("monitors:\n  - name: sizing\n    pulse_check:\n      type: http\n      interval: 100ms\n      timeout: 50ms\n      config: {url: 'http://127.0.0.1:1/'}\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadMonitors(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got := c.pulsePool.Stats().CurrentCapacity; got != min(64, 4*len(c.pulseQueue.Stats().Drivers)) {
		t.Fatalf("loaded target=%d", got)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if got := c.pulsePool.Stats().CurrentCapacity; got != min(64, 4*len(c.pulseQueue.Stats().Drivers)) {
		t.Fatalf("Start replaced target with %d", got)
	}
}

func TestInitialSizingAccountsForUnobservedTimeouts(t *testing.T) {
	world := ecs.NewWorld()
	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](&world)
	for i := 0; i < 1000; i++ {
		timeout := 2 * time.Second
		if i < 300 {
			timeout = 5 * time.Second
		}
		mapper.NewEntity(&components.MonitorState{}, &components.PulseConfig{Interval: 10 * time.Second, Timeout: timeout})
	}
	if got := initialPulseServiceTime(&world); got < 2899*time.Millisecond || got > 2901*time.Millisecond {
		t.Fatalf("cold timeout-weighted occupancy estimate=%s", got)
	}
	// A timeout beyond the cadence cannot create overlapping executions of one
	// monitor and must not inflate the initial reservation above its one seat.
	other := ecs.NewWorld()
	ecs.NewMap2[components.MonitorState, components.PulseConfig](&other).
		NewEntity(&components.MonitorState{}, &components.PulseConfig{Interval: time.Second, Timeout: time.Hour})
	if got := initialPulseServiceTime(&other); got != time.Second {
		t.Fatalf("non-overlapping timeout occupancy=%s", got)
	}
}
