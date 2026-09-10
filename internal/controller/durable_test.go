package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cpra/internal/durable"
	"cpra/internal/runtimeconfig"
)

func TestDurableControllerUsesCommittedStateAndResumesChecks(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	var checks, alerts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alert" {
			alerts.Add(1)
			w.WriteHeader(204)
			return
		}
		checks.Add(1)
		w.WriteHeader(503)
	}))
	defer target.Close()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "monitors.yaml")
	text := fmt.Sprintf("monitors:\n  - id: durable-monitor\n    name: Durable monitor\n    pulse_check:\n      type: http\n      interval: 50ms\n      timeout: 1s\n      unhealthy_threshold: 1\n      healthy_threshold: 2\n      config: {url: %q}\n    codes:\n      red:\n        notify: webhook\n        config: {url: %q}\n", target.URL, target.URL+"/alert?token=never-persist-provider-secret")
	if err := os.WriteFile(manifest, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeconfig.Default()
	runtime.Storage.Directory = filepath.Join(dir, "data")
	start := func() (*Controller, *durable.Store) {
		s, err := durable.Open(context.Background(), runtime)
		if err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		cfg.Store = s
		cfg.Runtime = runtime
		cfg.StreamingConfig.PreAllocateCount = 0
		cfg.WorkerConfig.MinWorkers = 1
		cfg.WorkerConfig.MaxWorkers = 4
		cfg.WorkerConfig.NumShards = 1
		cfg.WorkerConfig.ResultBatchTimeout = time.Millisecond
		c := NewController(cfg)
		if err = c.LoadMonitors(context.Background(), manifest); err != nil {
			c.Stop()
			s.Close()
			t.Fatal(err)
		}
		if err = c.Start(); err != nil {
			t.Fatal(err)
		}
		return c, s
	}
	c, s := start()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("controller did not reach expected committed state")
	}
	wait(func() bool { m, ok := s.Get("durable-monitor"); return ok && m.Generation >= 3 && alerts.Load() == 1 })
	if metrics, ok := c.Metrics().GetSystemMetrics("DurableSystem"); !ok || metrics.TotalEntitiesProcessed == 0 || metrics.TotalUpdates == 0 {
		t.Fatal("durable runtime lost the existing system telemetry")
	}
	c.Stop()
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(runtime.Storage.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("never-persist-provider-secret")) {
			t.Fatalf("provider configuration was persisted in %s", entry.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	oldChecks := checks.Load()
	c, s = start()
	defer s.Close()
	defer c.Stop()
	wait(func() bool { return checks.Load() >= oldChecks+3 })
	if alerts.Load() != 1 {
		t.Fatal("repeated committed successful endpoint after restart")
	}
	m, _ := s.Get("durable-monitor")
	if !m.Incident || !m.LatencyAvailable {
		t.Fatalf("lost state or measurement: %+v", m)
	}
	index := c.SnapshotHolder().Index()
	if index == nil {
		t.Fatal("missing incremental projection")
	}
	rows, total := index.Page(0, 1, nil)
	if total != 1 || len(rows) != 1 || rows[0].MonitorID != "durable-monitor" || !rows[0].LatencyAvailable {
		t.Fatal(rows, total)
	}
	view := s.SLO().View(time.Now(), 5*time.Minute, 1)
	if len(view.Reports) != 1 || view.Reports[0].Samples == 0 || view.CoverageComplete {
		t.Fatal("incorrect SLO recovery coverage", view)
	}
}
