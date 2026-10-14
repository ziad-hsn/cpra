package controller

import (
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func controllerTestConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Runtime.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), cfg.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg.Store = store
	return cfg
}

func TestControllerRequiresInitializedStore(t *testing.T) {
	defer func() {
		if got := recover(); got != "controller requires an initialized store" {
			t.Fatalf("missing store must fail before allocating workers: %v", got)
		}
	}()
	NewController(DefaultConfig())
}

func TestConcurrentStopJoinsStartedAndUnstartedController(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	for _, start := range []bool{false, true} {
		cfg := controllerTestConfig(t)
		cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
		cfg.WorkerConfig.AdjustmentInterval = 0
		c := NewController(cfg)
		if start {
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
		}
		var callers sync.WaitGroup
		for i := 0; i < 4; i++ {
			callers.Add(1)
			go func() { defer callers.Done(); c.Stop() }()
		}
		joined := make(chan struct{})
		go func() { callers.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Stop calls did not join")
		}
		if err := c.Start(); err == nil {
			t.Fatal("restarted a finalized controller")
		}
	}
}
