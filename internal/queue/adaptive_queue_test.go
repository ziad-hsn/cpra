package queue

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestAdaptiveQueueBasic verifies single-producer/single-consumer FIFO order.
func TestAdaptiveQueueBasic(t *testing.T) {
	q, err := NewAdaptiveQueue(8)
	if err != nil {
		t.Fatalf("NewAdaptiveQueue: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := q.Enqueue(newTestHybridJob(i)); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	// Queue is full now.
	if err := q.Enqueue(newTestHybridJob(99)); err != ErrQueueFull {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
	for i := 0; i < 8; i++ {
		job, err := q.Dequeue()
		if err != nil {
			t.Fatalf("dequeue %d: %v", i, err)
		}
		if job == nil {
			t.Fatalf("dequeue %d: nil job", i)
		}
		if got := job.(*testHybridJob).id; got != i {
			t.Fatalf("dequeue %d: got id %d, want %d", i, got, i)
		}
	}
	if job, _ := q.Dequeue(); job != nil {
		t.Fatalf("expected empty queue, got job %v", job)
	}
}

// TestAdaptiveQueueConcurrent verifies that under concurrent producers and
// consumers no job is lost or duplicated (the Vyukov sequence numbers must
// establish a happens-before edge between write and read).
func TestAdaptiveQueueConcurrent(t *testing.T) {
	const producers = 8
	const consumers = 8
	const perProducer = 2000
	const total = producers * perProducer

	q, err := NewAdaptiveQueue(1 << 10)
	if err != nil {
		t.Fatalf("NewAdaptiveQueue: %v", err)
	}

	var seen [total]atomic.Int32
	var wg sync.WaitGroup

	// Producers.
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				id := p*perProducer + i
				for {
					if err := q.Enqueue(newTestHybridJob(id)); err == nil {
						break
					} else if err == ErrQueueFull {
						continue // retry until space
					} else {
						t.Errorf("enqueue: %v", err)
						return
					}
				}
			}
		}(p)
	}

	// Consumers.
	var consumed atomic.Int64
	for c := 0; c < consumers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := q.Dequeue()
				if err != nil {
					return
				}
				if job == nil {
					if consumed.Load() >= total {
						return
					}
					continue
				}
				id := job.(*testHybridJob).id
				if id < 0 || id >= total {
					t.Errorf("out-of-range id %d", id)
					return
				}
				if seen[id].Add(1) > 1 {
					t.Errorf("job %d delivered more than once", id)
				}
				consumed.Add(1)
			}
		}()
	}

	wg.Wait()

	if got := consumed.Load(); got != total {
		t.Fatalf("consumed %d jobs, want %d (lost %d)", got, total, total-got)
	}
	for i := 0; i < total; i++ {
		if n := seen[i].Load(); n != 1 {
			t.Fatalf("job %d seen %d times, want exactly 1", i, n)
		}
	}
}
