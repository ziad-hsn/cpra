package controller

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestIdleControllerWakeupBoundAndStop(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Runtime.Storage.Mode = "memory"
		store, err := persistence.Open(t.Context(), cfg.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		cfg.Store = store
		cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
		cfg.WorkerConfig.AdjustmentInterval = 0
		owner := NewController(cfg)
		defer owner.Stop()
		if err := owner.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		updates := func() int64 {
			metrics, ok := owner.metricsAgg.GetSystemMetrics("DurableSystem")
			if !ok {
				t.Fatal("missing owner progress")
			}
			return metrics.TotalUpdates
		}
		before := updates()
		time.Sleep(400 * time.Millisecond)
		synctest.Wait()
		if after := updates(); after != before {
			t.Fatalf("idle owner kept polling: %d -> %d", before, after)
		}
		owner.wakeups <- struct{}{}
		synctest.Wait()
		if updates() != before+1 {
			t.Fatal("owner hint did not wake idle update")
		}
		for range 4 {
			owner.wakeups <- struct{}{}
			synctest.Wait()
		}
		if updates() != before+1 {
			t.Fatal("repeated hints bypassed the fixed update boundary")
		}
		time.Sleep(5 * time.Millisecond)
		synctest.Wait()
		if updates() != before+2 {
			t.Fatal("coalesced hints lost the pending update")
		}
		owner.Stop()
		synctest.Wait()
		if owner.Ready() {
			t.Fatal("stopped owner still ready")
		}
	})
}
