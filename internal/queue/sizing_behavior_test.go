package queue

import (
	"io"
	"log"
	"testing"
	"time"
)

func TestSizingModelHonorsLatencyTarget(t *testing.T) {
	q, _ := NewHybridQueue(HybridQueueConfig{RingCapacity: 16})
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers = 1
	cfg.MaxWorkers = 64
	cfg.TargetQueueLatency = 10 * time.Millisecond
	p, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer p.DrainAndStop()
	p.SetTargetWorkers(32)
	p.SetArrivalRate(10)
	p.SetServiceTime(.1)
	if got := p.desiredCapacity(Stats{EnqueueRate: 10, DequeueRate: 10}); got != 4 {
		t.Fatalf("capacity=%d; want 4 from c=3 plus 15 percent headroom", got)
	}
}
