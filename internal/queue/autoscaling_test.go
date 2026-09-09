package queue

import (
	"cpra/internal/jobs"
	"io"
	"log"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type sizingQueue struct {
	Queue
	mu    sync.Mutex
	stats Stats
}

func (q *sizingQueue) Stats() Stats                         { q.mu.Lock(); defer q.mu.Unlock(); return q.stats }
func (q *sizingQueue) setCV(cv float64)                     { q.mu.Lock(); q.stats.ArrivalCV = cv; q.mu.Unlock() }
func (q *sizingQueue) DequeueBatch(int) ([]jobs.Job, error) { return nil, nil }
func (q *sizingQueue) Close()                               {}

func newSizingPool(t *testing.T, q Queue) *DynamicWorkerPool {
	t.Helper()
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 64, 1
	cfg.AdjustmentInterval = time.Second
	p, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.DrainAndStop)
	p.SetTargetWorkers(32)
	p.SetArrivalRate(10)
	p.SetServiceTime(0.1)
	return p
}

func TestDesiredCapacityUsesLatencyVariabilityAndHeadroom(t *testing.T) {
	q := &sizingQueue{stats: Stats{EnqueueRate: 10, DequeueRate: 10, ArrivalCV: 1, ArrivalSamples: 256}}
	p := newSizingPool(t, q)
	// With lambda=10 and tau=.1, M/M/2 has mean W=.1333333,
	// M/M/3 has W=.10454545, and M/M/4 has W=.10068027.
	// These known values make the expected capacities independent of the solver.
	p.SetSizingPolicy(200*time.Millisecond, 0.15)
	if got := p.desiredCapacity(q.Stats()); got != 3 {
		t.Fatalf("loose target: got %d, want ceil(2*1.15)=3", got)
	}
	p.SetSizingPolicy(110*time.Millisecond, 0.15)
	if got := p.desiredCapacity(q.Stats()); got != 4 {
		t.Fatalf("tight target: got %d, want ceil(3*1.15)=4", got)
	}
	q.setCV(5)
	if got := p.desiredCapacity(q.Stats()); got != 5 {
		t.Fatalf("bursty arrivals: got %d, want ceil(4*1.15)=5", got)
	}
	q.setCV(1)
	// Nine 1ms jobs and one 991ms job preserve the 100ms mean but increase Cs.
	for i := 0; i < 9; i++ {
		p.serviceMetrics.observe(time.Millisecond)
	}
	p.serviceMetrics.observe(991 * time.Millisecond)
	if got := p.desiredCapacity(q.Stats()); got != 5 {
		t.Fatalf("variable service: got %d, want 5", got)
	}
	ca, cs := p.GetVariabilityCoefficients()
	if ca != 1 || cs < 2.9 {
		t.Fatalf("observed coefficients: ca=%v cs=%v", ca, cs)
	}
	if got := p.Stats().SizingModel; got != "erlang_c_allen_cunneen" {
		t.Fatal(got)
	}
}

func TestDesiredCapacityBoundsAndFallback(t *testing.T) {
	q := &sizingQueue{stats: Stats{EnqueueRate: 10, DequeueRate: 10, ArrivalCV: 1, ArrivalSamples: 256}}
	p := newSizingPool(t, q)
	p.SetSizingPolicy(50*time.Millisecond, .15) // below the service time
	if got := p.desiredCapacity(q.Stats()); got != 2 {
		t.Fatalf("unattainable service-time target must not request maximum workers: %d", got)
	}
	if p.Stats().SizingModel != "slo_unattainable" {
		t.Fatal(p.Stats())
	}
	p.SetArrivalRate(1000)
	p.SetSizingPolicy(200*time.Millisecond, .15)
	if got := p.desiredCapacity(q.Stats()); got != 64 {
		t.Fatalf("capacity-constrained target: %d", got)
	}
	p.SetTargetWorkers(2)
	if got := p.desiredCapacity(q.Stats()); got != 4 {
		t.Fatalf("growth limit: %d", got)
	}
	p.SetTargetWorkers(32)
	p.SetArrivalRate(10)
	p.SetSizingPolicy(110*time.Millisecond, .15)
	q.setCV(math.NaN())
	if got := p.desiredCapacity(q.Stats()); got != 2 {
		t.Fatalf("invalid model data fallback: %d", got)
	}
	if p.Stats().SizingModel != "little_law_fallback" {
		t.Fatal(p.Stats())
	}
}

func TestAutoScalerConsumesVariabilityAtRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &sizingQueue{stats: Stats{EnqueueRate: 10, DequeueRate: 10, ArrivalCV: 1, ArrivalSamples: 256}}
		p := newSizingPool(t, q)
		p.SetSizingPolicy(110*time.Millisecond, .15)
		p.Start()
		time.Sleep(time.Second)
		synctest.Wait()
		if got := p.Stats().CurrentCapacity; got != 4 {
			t.Fatalf("first scaling tick: %d", got)
		}
		q.setCV(5)
		time.Sleep(time.Second)
		synctest.Wait()
		if got := p.Stats().CurrentCapacity; got != 5 {
			t.Fatalf("variability scaling tick: %d", got)
		}
		p.DrainAndStop()
	})
}

type timedJob struct{ duration time.Duration }

func (j *timedJob) Execute() jobs.Result      { time.Sleep(j.duration); return jobs.Result{Type: "pulse"} }
func (j *timedJob) Copy() jobs.Job            { return j }
func (j *timedJob) GetEnqueueTime() time.Time { return time.Time{} }
func (j *timedJob) SetEnqueueTime(time.Time)  {}
func (j *timedJob) GetStartTime() time.Time   { return time.Time{} }
func (j *timedJob) SetStartTime(time.Time)    {}
func (j *timedJob) IsNil() bool               { return j == nil }

func TestRuntimeMeasuresArrivalsAndExecutionForBothQueues(t *testing.T) {
	for _, kind := range []string{"hybrid", "adaptive"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var q Queue
				if kind == "hybrid" {
					q, _ = NewHybridQueue(HybridQueueConfig{RingCapacity: 16})
				} else {
					q, _ = NewAdaptiveQueue(16)
				}
				q = NewHandle(q)
				p := newSizingPool(t, q)
				p.config.AdjustmentInterval = 0
				for _, gap := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
					time.Sleep(gap)
					if err := q.Enqueue(&timedJob{duration: 20 * time.Millisecond}); err != nil {
						t.Fatal(err)
					}
				}
				stats := q.Stats()
				if stats.ArrivalSamples != 2 || math.Abs(stats.ArrivalCV-.2) > 1e-9 {
					t.Fatalf("admission observations: %+v", stats)
				}
				p.Start()
				received := 0
				for received < 3 {
					received += len(<-p.router.PulseResultChan)
				}
				mean, cv, count := p.serviceMetrics.snapshot()
				if count != 3 || math.Abs(mean-.02) > 1e-9 || cv > 1e-9 {
					t.Fatalf("execution observations: mean=%v cv=%v n=%d", mean, cv, count)
				}
				if got := p.desiredCapacity(q.Stats()); got != 2 {
					t.Fatalf("measured execution time must replace bootstrap estimate: %d", got)
				}
				p.DrainAndStop()
			})
		})
	}
}

func TestObservedRateRetainsBacklogRelief(t *testing.T) {
	q := &sizingQueue{stats: Stats{EnqueueRate: 10, DequeueRate: 10, ArrivalCV: 1, ArrivalSamples: 256, QueueDepth: 100}}
	p := newSizingPool(t, q)
	p.SetSizingPolicy(110*time.Millisecond, .15)
	if got := p.desiredCapacity(q.Stats()); got != 4 {
		t.Fatalf("external demand target: %d", got)
	}
	p.SetArrivalRate(0)
	if got := p.desiredCapacity(q.Stats()); got != 64 {
		t.Fatalf("backlog after quiet period should raise observed-rate target: %d", got)
	}
}
