package queue

import (
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHybridRingMakesProgressUnderSustainedOverflow(t *testing.T) {
	q, err := NewHybridQueue(HybridQueueConfig{RingCapacity: 2, OverflowCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for i := 0; i < 4; i++ {
		if err := q.Enqueue(newTestHybridJob(i)); err != nil {
			t.Fatal(err)
		}
	}
	ringSeen := false
	for i := 0; i < 1000; i++ {
		batch, err := q.DequeueBatch(1)
		if err != nil || len(batch) != 1 {
			t.Fatalf("dequeue: %v %v", batch, err)
		}
		if batch[0].(*testHybridJob).id < 2 {
			ringSeen = true
			break
		}
		if err := q.Enqueue(newTestHybridJob(i + 4)); err != nil {
			t.Fatal(err)
		}
	}
	if !ringSeen {
		t.Fatalf("after 1000 successful dequeue/admission cycles, both original ring jobs remain pending; ring=%d overflow=%d", q.RingDepth(), q.OverflowDepth())
	}
}

func TestAdaptiveRejectsUnsupportedCapacities(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		q, err := NewAdaptiveQueue(0)
		if err == nil {
			t.Fatalf("accepted zero capacity: buffer length=%d mask=%d", len(q.buffer), q.mask)
		}
	})
	t.Run("one", func(t *testing.T) {
		q, err := NewAdaptiveQueue(1)
		if err != nil {
			return
		}
		defer q.Close()
		if err := q.Enqueue(newTestHybridJob(1)); err != nil {
			t.Fatal(err)
		}
		if err := q.Enqueue(newTestHybridJob(2)); err != ErrQueueFull {
			t.Fatalf("one-slot queue accepted overwrite: err=%v depth=%d cell job=%d", err, q.Stats().QueueDepth, q.buffer[0].job.(*testHybridJob).id)
		}
	})
}

func TestWorkivaConcurrentExpansionRetainsAcceptedJobs(t *testing.T) {
	q := NewWorkivaQueue(2)
	defer q.Close()
	const producers = 8
	const perProducer = 2000
	var accepted, received atomic.Int64
	var producersDone atomic.Bool
	var consumers sync.WaitGroup
	var writers sync.WaitGroup
	stop := make(chan struct{})
	for n := 0; n < 4; n++ {
		consumers.Add(1)
		go func() {
			defer consumers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				job, err := q.Dequeue()
				if err != nil {
					t.Errorf("dequeue: %v", err)
					return
				}
				if job != nil {
					received.Add(1)
				}
				if producersDone.Load() && received.Load() >= accepted.Load() {
					return
				}
			}
		}()
	}
	for n := 0; n < producers; n++ {
		writers.Add(1)
		go func(n int) {
			defer writers.Done()
			for i := 0; i < perProducer; i++ {
				if err := q.Enqueue(newTestHybridJob(n*perProducer + i)); err != nil {
					t.Errorf("enqueue: %v", err)
					return
				}
				accepted.Add(1)
			}
		}(n)
	}
	writers.Wait()
	producersDone.Store(true)
	until := time.Now().Add(500 * time.Millisecond)
	for received.Load() < accepted.Load() && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	consumers.Wait()
	if got, want := received.Load(), accepted.Load(); got != want {
		t.Fatalf("lost accepted jobs during concurrent expansion: received=%d accepted=%d stats=%+v", got, want, q.Stats())
	}
}

func TestPreallocationRejectsIncompatibleDynamicPool(t *testing.T) {
	q, _ := NewHybridQueue(HybridQueueConfig{RingCapacity: 2})
	defer q.Close()
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 64, 1
	cfg.PreAlloc = true
	if pool, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0)); err == nil {
		pool.DrainAndStop()
		t.Fatal("accepted preallocation that disables worker tuning")
	}
}
