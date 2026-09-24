package persistence_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestControllerRetainsInFlightPulseAcrossRaftElection(t *testing.T) {
	controller.InitializeLoggers(false)
	t.Cleanup(controller.CloseLoggers)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var invocations atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invocations.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	t.Cleanup(unblock)
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	store, err := persistence.Open(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := controller.DefaultConfig()
	cfg.Store, cfg.Runtime = store, settings
	cfg.StreamingConfig.PreAllocateCount = 0
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
	cfg.WorkerConfig.ResultBatchSize = 1
	cfg.WorkerConfig.ResultBatchTimeout = time.Millisecond
	owner := controller.NewController(cfg)
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := owner.StopContext(ctx); err != nil {
			t.Errorf("controller shutdown: %v", err)
		}
	})
	manifest := filepath.Join(t.TempDir(), "monitors.yaml")
	// This interval gives the first entity a two-second startup phase and keeps
	// its next scheduled check outside the bounded election test.
	configuration := fmt.Sprintf("monitors:\n  - id: election-pulse\n    name: Election pulse\n    pulse_check:\n      type: http\n      interval: 41s\n      timeout: 15s\n      unhealthy_threshold: 1\n      healthy_threshold: 1\n      config: {url: %q}\n", target.URL)
	if err := os.WriteFile(manifest, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := owner.LoadMonitors(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	if err := owner.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("scheduled check never entered the provider")
	}
	restoreElection := persistence.ForceFollowerForTest(t, store)
	if owner.Ready() {
		t.Fatal("controller remained ready while its store was a follower")
	}
	unblock()
	waitControllerRecovery(t, "provider completion while follower", func() bool {
		return owner.PulsePool().Stats().TasksCompleted == 1
	})
	metrics, ok := owner.Metrics().GetSystemMetrics("DurableSystem")
	if !ok {
		t.Fatal("durable owner telemetry missing")
	}
	completedAtUpdate := metrics.TotalUpdates
	waitControllerRecovery(t, "owner progress while follower", func() bool {
		current, ok := owner.Metrics().GetSystemMetrics("DurableSystem")
		return ok && current.TotalUpdates >= completedAtUpdate+10
	})
	if !persistence.IsLeadershipUnavailable(store.ControllerHealth()) || owner.Ready() {
		t.Fatal("follower health lost its transient classification")
	}
	before, ok := store.Get("election-pulse")
	if !ok || before.Generation != 0 || before.TotalChecks != 0 || before.Incident || before.ConsecutiveFailures != 0 {
		t.Fatalf("leadership interruption synthesized a target failure: %+v", before)
	}
	restoreElection()
	waitControllerRecovery(t, "committed pulse and recovered readiness", func() bool {
		current, found := store.Get("election-pulse")
		view := store.SLO().View(time.Now(), 5*time.Minute, 1)
		return found && current.Generation == 1 && owner.Ready() && len(view.Reports) == 1 && view.Reports[0].Samples == 1
	})
	current, _ := store.Get("election-pulse")
	if current.TotalChecks != 1 || current.SuccessfulChecks != 1 || current.LastOutcome != "success" || current.Incident || current.ConsecutiveFailures != 0 || !current.LatencyAvailable {
		t.Fatalf("recovered pulse did not preserve the provider outcome: %+v", current)
	}
	view := store.SLO().View(time.Now(), 5*time.Minute, 1)
	if len(view.Reports) != 1 || view.Reports[0].Driver != "http" || view.Reports[0].Expected != 1 || view.Reports[0].Samples != 1 || view.Reports[0].Timeouts != 0 || view.Reports[0].Missed != 0 {
		t.Fatalf("recovery lost or duplicated SLO accounting: %+v", view)
	}
	rows, total := owner.SnapshotHolder().Index().Page(0, 1, nil)
	if total != 1 || len(rows) != 1 || rows[0].MonitorID != current.ID || rows[0].Status != "up" || rows[0].Incident || rows[0].ConsecutiveFailures != 0 || !rows[0].LastCheck.Equal(current.LastCheck) {
		t.Fatalf("dashboard did not project the recovered success: %+v", rows)
	}
	if invocations.Load() != 1 {
		t.Fatalf("provider invoked %d times for one retained check", invocations.Load())
	}
}

func TestControllerStartupFollowerRemainsUnavailableAfterElection(t *testing.T) {
	controller.InitializeLoggers(false)
	t.Cleanup(controller.CloseLoggers)
	var invocations atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invocations.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	store, err := persistence.Open(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := controller.DefaultConfig()
	cfg.Store, cfg.Runtime = store, settings
	cfg.StreamingConfig.PreAllocateCount = 0
	cfg.WorkerConfig.MinWorkers, cfg.WorkerConfig.MaxWorkers, cfg.WorkerConfig.NumShards = 1, 1, 1
	owner := controller.NewController(cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := owner.StopContext(ctx); err != nil {
			t.Errorf("controller shutdown: %v", err)
		}
	})
	manifest := filepath.Join(t.TempDir(), "monitors.yaml")
	configuration := fmt.Sprintf("monitors:\n  - id: startup-follower\n    name: Startup follower\n    pulse_check:\n      type: http\n      interval: 50ms\n      timeout: 1s\n      unhealthy_threshold: 1\n      healthy_threshold: 1\n      config: {url: %q}\n", target.URL)
	if err := os.WriteFile(manifest, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := owner.LoadMonitors(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	restoreElection := persistence.ForceFollowerForTest(t, store)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	if err := owner.WaitReady(ctx); err == nil || owner.Ready() {
		t.Fatal("follower initialization reported ready")
	}
	restoreElection()
	waitControllerRecovery(t, "Raft re-election after failed initialization", func() bool {
		return persistence.RaftLeaderForTest(store)
	})
	metrics, ok := owner.Metrics().GetSystemMetrics("DurableSystem")
	if !ok {
		t.Fatal("durable owner telemetry missing")
	}
	initialUpdates := metrics.TotalUpdates
	waitControllerRecovery(t, "owner progress after failed initialization", func() bool {
		if owner.Ready() {
			t.Fatal("failed startup resumed admission after re-election")
		}
		current, ok := owner.Metrics().GetSystemMetrics("DurableSystem")
		return ok && current.TotalUpdates >= initialUpdates+10
	})
	if err := owner.WaitReady(ctx); err == nil || owner.Ready() || store.Status().Ready {
		t.Fatal("re-election cleared the permanent startup failure")
	}
	if invocations.Load() != 0 || owner.PulsePool().Stats().TasksSubmitted != 0 {
		t.Fatal("failed initialization admitted provider work")
	}
	current, ok := store.Get("startup-follower")
	if !ok || current.Generation != 0 || current.TotalChecks != 0 || current.Incident || current.ConsecutiveFailures != 0 {
		t.Fatalf("failed startup created a target observation: %+v", current)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := owner.StopContext(stopCtx); err != nil {
		t.Fatalf("failed initialization with no accepted work could not stop: %v", err)
	}
	if invocations.Load() != 0 {
		t.Fatal("shutdown invoked a provider after failed startup")
	}
}

func waitControllerRecovery(t *testing.T, stage string, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("controller did not reach %s", stage)
		case <-ticker.C:
		}
	}
}
