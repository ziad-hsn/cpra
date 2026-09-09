package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartPreservesLatencyAwareInitialCapacity(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	t.Setenv("CPRA_SIZING_TAU_MS", "")
	t.Setenv("CPRA_SIZING_SLO_MS", "")
	t.Setenv("CPRA_SIZING_HEADROOM_PCT", "")
	cfg := DefaultConfig()
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
	if got := c.pulsePool.Stats().CurrentCapacity; got != 4 {
		t.Fatalf("loaded target=%d", got)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if got := c.pulsePool.Stats().CurrentCapacity; got != 4 {
		t.Fatalf("Start replaced target with %d", got)
	}
}
