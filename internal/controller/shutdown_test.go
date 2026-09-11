package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/durable"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStopDeadlineCancelsWorkAndRetainsUnknownAction(t *testing.T) {
	InitializeLoggers(false)
	defer CloseLoggers()
	entered := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notify" {
			_, _ = io.Copy(io.Discard, r.Body)
			once.Do(func() { close(entered) })
			<-r.Context().Done()
			return
		}
		w.WriteHeader(503)
	}))
	defer target.Close()
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	manifest := fmt.Sprintf("monitors:\n  - id: shutdown-test\n    name: Shutdown test\n    pulse_check:\n      type: http\n      interval: 10ms\n      timeout: 1s\n      unhealthy_threshold: 1\n      config: {url: %q}\n    codes:\n      red:\n        notify: webhook\n        config: {url: %q, timeout: 10s}\n", target.URL, target.URL+"/notify")
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeConfig := runtimeconfig.Default()
	runtimeConfig.Storage.Mode = "memory"
	store, err := durable.Open(context.Background(), runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Store = store
	cfg.Runtime = runtimeConfig
	cfg.StreamingConfig.PreAllocateCount = 0
	cfg.WorkerConfig.MinWorkers = 1
	cfg.WorkerConfig.MaxWorkers = 2
	cfg.WorkerConfig.NumShards = 1
	cfg.WorkerConfig.DrainTimeout = time.Minute
	c := NewController(cfg)
	defer c.Stop()
	if err = c.LoadMonitors(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("notification did not start")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopCancel()
	if err = c.StopContext(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not observed: %v", err)
	}
	if c.Ready() {
		t.Fatal("draining controller reported ready")
	}
	joined := make(chan struct{})
	go func() { c.Stop(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("cooperative operation/owner leaked after cancellation")
	}
	m, ok := store.Get("shutdown-test")
	if !ok {
		t.Fatal("missing monitor")
	}
	unknown := 0
	for _, action := range m.Actions {
		if action.State == durable.Unknown {
			unknown++
		}
	}
	if unknown != 1 {
		t.Fatalf("ambiguous interrupted delivery must be unknown, actions=%+v", m.Actions)
	}
}

func TestReadinessRequiresRecentOwnerProgress(t *testing.T) {
	c := &Controller{}
	c.initializedState.Store(true)
	c.lastProgress.Store(time.Now().Add(-time.Minute).UnixNano())
	if c.Ready() {
		t.Fatal("stalled owner reported ready")
	}
	c.lastProgress.Store(time.Now().UnixNano())
	if !c.Ready() {
		t.Fatal("active initialized owner was not ready")
	}
	c.stoppingState.Store(true)
	if c.Ready() {
		t.Fatal("draining owner reported ready")
	}
}
