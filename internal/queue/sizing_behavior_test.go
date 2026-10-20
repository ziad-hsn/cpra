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

func TestConfiguredDemandSurvivesHistoricalFastSamples(t *testing.T) {
	for _, warm := range []bool{false, true} {
		q, _ := NewFairQueue(2000, "http", "tcp")
		cfg := DefaultWorkerPoolConfig()
		cfg.FairDispatch, cfg.MinWorkers, cfg.MaxWorkers = true, 2, 1000
		p, err := NewDynamicWorkerPool(q, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		p.SetServiceTime(.004)
		if warm {
			for i := 0; i < 1000; i++ {
				p.serviceMetrics.observe(time.Millisecond)
			}
		}
		p.SetArrivalRate(100)
		p.SetTargetWorkers(2)
		if err := p.SetDriverDemand([]DriverDemand{{Driver: "http", Monitors: 300, ArrivalRate: 100, Occupancy: 290}}); err != nil {
			t.Fatal(err)
		}
		for _, current := range []int{2, 4, 8, 16, 32, 64, 128} {
			p.antsPool.Tune(current)
			if got := p.desiredCapacity(Stats{}); got != current*2 {
				t.Fatalf("warm=%t current=%d desired=%d; new demand erased or growth bound exceeded", warm, current, got)
			}
		}
		if err := p.SetDriverDemand(nil); err != nil {
			t.Fatal(err)
		}
		if got := p.desiredCapacity(Stats{}); got >= 128 {
			t.Fatalf("observed cold demand retained: %d", got)
		}
		p.DrainAndStop()
	}
}
