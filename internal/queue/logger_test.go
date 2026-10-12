package queue

import (
	"log"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestWorkerPoolNilLogger(t *testing.T) {
	var standard *log.Logger
	for _, tc := range []struct {
		name   string
		logger Logger
	}{
		{"untyped", nil},
		{"standard", standard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := NewHybridQueue(HybridQueueConfig{RingCapacity: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			cfg := DefaultWorkerPoolConfig()
			cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 1, 1
			cfg.AdjustmentInterval = 0
			cfg.DrainTimeout = time.Second
			pool, err := NewDynamicWorkerPool(q, cfg, tc.logger)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.DrainAndStop()
			pool.Start()
			pool.Start()
		})
	}
}

func TestResultRouterNilLogger(t *testing.T) {
	var standard *log.Logger
	for _, sink := range []Logger{nil, standard} {
		router := NewResultRouter(DefaultWorkerPoolConfig(), sink)
		router.RouteResults([]jobs.Result{{Type: "unsupported"}, {Type: "pulse"}})
		if batch := <-router.PulseResultChan; len(batch) != 1 || batch[0].Type != "pulse" {
			t.Fatalf("lost valid result with nil logger: %v", batch)
		}
		router.Close()
	}
}
