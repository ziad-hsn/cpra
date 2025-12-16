package queue

import (
	"context"
	"cpra/internal/jobs"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MockJob implements jobs.Job
type MockJob struct {
	ID          int
	EnqueueTime time.Time
}

func (m *MockJob) Execute(_ context.Context) jobs.Result { return jobs.Result{} }
func (m *MockJob) Type() string                          { return "mock" }
func (m *MockJob) SetEnqueueTime(t time.Time) {
	m.EnqueueTime = t
}
func (m *MockJob) GetEnqueueTime() time.Time {
	return m.EnqueueTime
}
func (m *MockJob) IsNil() bool {
	return m == nil
}
func (m *MockJob) Copy() jobs.Job {
	cp := *m
	return &cp
}
func (m *MockJob) GetStartTime() time.Time  { return time.Time{} }
func (m *MockJob) SetStartTime(t time.Time) {}

func TestAdaptiveQueue_RaceCondition(t *testing.T) {
	// Create a queue with enough capacity that we don't just block on full
	// But small enough to force wrapping if we ran longer.
	// 1024 * 64
	q, err := NewAdaptiveQueue(1024 * 64)
	if err != nil {
		t.Fatalf("Failed to create queue: %v", err)
	}

	const producers = 10
	const itemsPerProducer = 1000
	const consumers = 10
	totalItems := producers * itemsPerProducer

	var wg sync.WaitGroup
	var startBarrier sync.WaitGroup
	startBarrier.Add(1)

	// Producers
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			startBarrier.Wait()
			for i := 0; i < itemsPerProducer; i++ {
				job := &MockJob{ID: pid*100000 + i}
				// Retry until success to simulate high pressure
				for {
					err := q.Enqueue(job)
					if err == nil {
						break
					}
					if err == ErrQueueFull {
						runtime.Gosched()
						continue
					}
					// Verify closed is not happening here
				}
			}
		}(p)
	}

	// Consumers
	var receivedCount atomic.Int64
	var nilCount atomic.Int64

	consumerWg := sync.WaitGroup{}
	for c := 0; c < consumers; c++ {
		consumerWg.Add(1)
		go func() {
			defer consumerWg.Done()
			startBarrier.Wait()
			for {
				if receivedCount.Load() >= int64(totalItems) {
					return
				}

				// Try DequeueBatch
				batch, err := q.DequeueBatch(10)
				if err != nil {
					// Queue closed?
					return
				}

				if len(batch) > 0 {
					receivedCount.Add(int64(len(batch)))
					for _, job := range batch {
						if job == nil {
							nilCount.Add(1)
							fmt.Println("CRITICAL: Read nil job from queue!")
						} else if job.IsNil() { // Double check
							nilCount.Add(1)
							fmt.Println("CRITICAL: Read IsNil job from queue!")
						}
					}
				} else {
					// Check single dequeue if batch returned nothing
					job, err := q.Dequeue()
					if job != nil {
						receivedCount.Add(1)
						if job.IsNil() {
							nilCount.Add(1)
							fmt.Println("CRITICAL: Read IsNil job from single Dequeue!")
						}
					}
					if err == nil && job == nil {
						// Empty
						runtime.Gosched()
					}
				}
			}
		}()
	}

	startBarrier.Done()

	// Wait specifically for producers
	wg.Wait()

	// Give consumers time to drain
	timeout := time.After(5 * time.Second)
	done := make(chan struct{})
	go func() {
		// Poll until done
		for receivedCount.Load() < int64(totalItems) {
			time.Sleep(10 * time.Millisecond)
		}
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-timeout:
		t.Logf("Timed out. Received %d / %d", receivedCount.Load(), totalItems)
	}

	if nilCount.Load() > 0 {
		t.Fatalf("FAILED: Detected %d nil reads due to race condition.", nilCount.Load())
	}

	t.Logf("Test finished. Enqueued: %d, Received: %d", totalItems, receivedCount.Load())
}
