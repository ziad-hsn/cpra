package queue

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestWorkerMetricsSeparateAttemptsFromServiceOccupancy(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	q, err := NewAdaptiveQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 1, 1
	cfg.ResultBatchSize = 1
	pool, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.DrainAndStop()
	check := &jobs.PulseHTTPJob{URL: target.URL, Method: "GET", Timeout: time.Second, Retries: 2}
	world := ecs.NewWorld()
	if err := q.Enqueue(jobs.NewDispatch(check, world.NewEntity(), "pulse", "", 1, 0)); err != nil {
		t.Fatal(err)
	}
	pool.Start()
	select {
	case <-pool.GetRouter().PulseResultChan:
	case <-time.After(2 * time.Second):
		t.Fatal("missing result")
	}
	stats := pool.Stats()
	if stats.Attempts != 2 || stats.AttemptTimeouts != 0 || stats.RetryDelay <= 0 || stats.AttemptDuration <= 0 {
		t.Fatalf("missing attempt accounting: %+v", stats)
	}
	if stats.ServiceSamples != 1 || stats.ServiceTime < stats.AttemptDuration+stats.RetryDelay {
		t.Fatalf("retry occupancy removed from sizing tau: %+v", stats)
	}
}
