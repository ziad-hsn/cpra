package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpra/internal/runtime/jobs"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type testHybridJob struct {
	enqueueTime time.Time
	startTime   time.Time
	id          int
}

func newTestHybridJob(id int) *testHybridJob {
	return &testHybridJob{id: id}
}

func (j *testHybridJob) Execute(_ context.Context) jobs.Result { return jobs.Result{} }
func (j *testHybridJob) Copy() jobs.Job                        { copy := *j; return &copy }
func (j *testHybridJob) GetEnqueueTime() time.Time             { return j.enqueueTime }
func (j *testHybridJob) SetEnqueueTime(t time.Time)            { j.enqueueTime = t }
func (j *testHybridJob) GetStartTime() time.Time               { return j.startTime }
func (j *testHybridJob) SetStartTime(t time.Time)              { j.startTime = t }
func (j *testHybridJob) IsNil() bool                           { return false }

func TestHybridQueueFastPath(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "fast",
		RingCapacity:     8,
		OverflowCapacity: 4,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	for i := 0; i < 4; i++ {
		if err := queue.Enqueue(newTestHybridJob(i)); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	for i := 0; i < 4; i++ {
		job, err := queue.Dequeue()
		if err != nil {
			t.Fatalf("dequeue failed: %v", err)
		}
		if job == nil {
			t.Fatalf("expected job, got nil at index %d", i)
		}
		if got := job.(*testHybridJob).id; got != i {
			t.Fatalf("expected id %d, got %d", i, got)
		}
	}

	stats := queue.Stats()
	if stats.QueueDepth != 0 {
		t.Fatalf("expected empty queue, depth=%d", stats.QueueDepth)
	}
}

func TestHybridQueueOverflowDrainOrder(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "overflow",
		RingCapacity:     2,
		OverflowCapacity: 4,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	for i := 0; i < 6; i++ {
		if err := queue.Enqueue(newTestHybridJob(i)); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	expected := []int{2, 3, 4, 5, 0, 1}
	for idx, want := range expected {
		job, err := queue.Dequeue()
		if err != nil {
			t.Fatalf("dequeue failed: %v", err)
		}
		if job == nil {
			t.Fatalf("expected job at position %d", idx)
		}
		if got := job.(*testHybridJob).id; got != want {
			t.Fatalf("unexpected dequeue order at %d: want %d, got %d", idx, want, got)
		}
	}
}

func TestHybridQueueDropPolicies(t *testing.T) {
	tests := []struct {
		name          string
		expectedFront []int
		policy        DropPolicy
		expectedDrop  int64
		expectErr     bool
	}{
		{
			name:          "reject",
			policy:        DropPolicyReject,
			expectErr:     true,
			expectedFront: []int{1, 0},
			expectedDrop:  1,
		},
		{
			name:          "drop_newest",
			policy:        DropPolicyDropNewest,
			expectErr:     true,
			expectedFront: []int{1, 0},
			expectedDrop:  1,
		},
		{
			name:          "drop_oldest",
			policy:        DropPolicyDropOldest,
			expectErr:     false,
			expectedFront: []int{2, 0},
			expectedDrop:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := HybridQueueConfig{
				Name:             tt.name,
				RingCapacity:     1,
				OverflowCapacity: 1,
				DropPolicy:       tt.policy,
				Logger:           zap.NewNop(),
			}
			queue, err := NewHybridQueue(cfg)
			if err != nil {
				t.Fatalf("failed to create hybrid queue: %v", err)
			}
			t.Cleanup(queue.Close)

			if err := queue.Enqueue(newTestHybridJob(0)); err != nil {
				t.Fatalf("enqueue 0 failed: %v", err)
			}
			if err := queue.Enqueue(newTestHybridJob(1)); err != nil {
				t.Fatalf("enqueue 1 failed: %v", err)
			}

			err = queue.Enqueue(newTestHybridJob(2))
			if tt.expectErr {
				if !errors.Is(err, ErrQueueFull) {
					t.Fatalf("expected ErrQueueFull, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected enqueue error: %v", err)
			}

			gotOrder := make([]int, 0, len(tt.expectedFront))
			for range tt.expectedFront {
				job, err := queue.Dequeue()
				if err != nil {
					t.Fatalf("dequeue failed: %v", err)
				}
				if job == nil {
					t.Fatalf("expected job during drain")
				}
				gotOrder = append(gotOrder, job.(*testHybridJob).id)
			}
			if len(gotOrder) != len(tt.expectedFront) {
				t.Fatalf("drain mismatch, expected %d items got %d", len(tt.expectedFront), len(gotOrder))
			}
			for i, want := range tt.expectedFront {
				if gotOrder[i] != want {
					t.Fatalf("unexpected order[%d]: want %d, got %d", i, want, gotOrder[i])
				}
			}

			stats := queue.Stats()
			if stats.Dropped != tt.expectedDrop {
				t.Fatalf("unexpected drop count: want %d got %d", tt.expectedDrop, stats.Dropped)
			}
		})
	}
}

func TestHybridQueueBatchOperations(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "batch",
		RingCapacity:     4,
		OverflowCapacity: 4,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	batch := []interface{}{newTestHybridJob(0), newTestHybridJob(1), newTestHybridJob(2)}
	if err := queue.EnqueueBatch(batch); err != nil {
		t.Fatalf("enqueue batch failed: %v", err)
	}

	jobs, err := queue.DequeueBatch(3)
	if err != nil {
		t.Fatalf("dequeue batch failed: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}
}

func TestHybridQueueConcurrentAccess(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "concurrency",
		RingCapacity:     64,
		OverflowCapacity: 64,
		DropPolicy:       DropPolicyDropNewest,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	const (
		producers    = 4
		consumers    = 3
		perProducer  = 100
		total        = producers * perProducer
		retryBackoff = 50 * time.Microsecond
	)

	var produced atomic.Int64
	var produceWG sync.WaitGroup
	errCh := make(chan error, 1)
	produceWG.Add(producers)
	for p := 0; p < producers; p++ {
		go func(offset int) {
			defer produceWG.Done()
			for i := 0; i < perProducer; i++ {
				job := newTestHybridJob(offset*perProducer + i)
				for {
					if err := queue.Enqueue(job); err != nil {
						if errors.Is(err, ErrQueueFull) {
							time.Sleep(retryBackoff)
							continue
						}
						if errors.Is(err, ErrQueueClosed) {
							return
						}
						select {
						case errCh <- fmt.Errorf("unexpected enqueue error: %w", err):
						default:
						}
						return
					}
					produced.Add(1)
					break
				}
			}
		}(p)
	}

	var consumed atomic.Int64
	var consumeWG sync.WaitGroup
	consumeWG.Add(consumers)
	for c := 0; c < consumers; c++ {
		go func() {
			defer consumeWG.Done()
			for {
				if consumed.Load() >= int64(total) {
					return
				}
				job, err := queue.Dequeue()
				if err != nil {
					if errors.Is(err, ErrQueueClosed) {
						return
					}
					select {
					case errCh <- fmt.Errorf("unexpected dequeue error: %w", err):
					default:
					}
					return
				}
				if job == nil {
					time.Sleep(retryBackoff)
					continue
				}
				consumed.Add(1)
			}
		}()
	}

	produceWG.Wait()
	for consumed.Load() < int64(total) {
		jobs, err := queue.DequeueBatch(16)
		if err != nil {
			if errors.Is(err, ErrQueueClosed) {
				break
			}
			t.Fatalf("unexpected batch dequeue error: %v", err)
		}
		if len(jobs) == 0 {
			time.Sleep(retryBackoff)
			continue
		}
		consumed.Add(int64(len(jobs)))
	}

	queue.Close()
	consumeWG.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrency error: %v", err)
	default:
	}

	if produced.Load() != int64(total) {
		t.Fatalf("expected produced %d, got %d", total, produced.Load())
	}
	if consumed.Load() != int64(total) {
		t.Fatalf("expected consumed %d, got %d", total, consumed.Load())
	}
}

func TestHybridQueueWatermarkAlerts(t *testing.T) {
	core, recorded := observer.New(zap.WarnLevel)
	logger := zap.New(core)

	cfg := HybridQueueConfig{
		Name:             "watermarks",
		RingCapacity:     1,
		OverflowCapacity: 2,
		SoftWatermark:    0.5,
		HardWatermark:    0.75,
		DropPolicy:       DropPolicyReject,
		Logger:           logger,
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	// First enqueue fills ring, second triggers soft watermark (overflow depth 1 of 2).
	if err := queue.Enqueue(newTestHybridJob(1)); err != nil {
		t.Fatalf("enqueue 1 failed: %v", err)
	}
	if err := queue.Enqueue(newTestHybridJob(2)); err != nil {
		t.Fatalf("enqueue 2 failed: %v", err)
	}
	// Third enqueue pushes overflow to capacity, should trigger hard watermark log.
	if err := queue.Enqueue(newTestHybridJob(3)); err != nil {
		t.Fatalf("enqueue 3 failed: %v", err)
	}

	if recorded.Len() == 0 {
		t.Fatalf("expected watermark alerts, saw none")
	}

	var sawSoft, sawHard bool
	for _, entry := range recorded.All() {
		msg := entry.Message
		if msg == "hybrid queue overflow above soft watermark" {
			sawSoft = true
		}
		if msg == "hybrid queue overflow above hard watermark" {
			sawHard = true
		}
	}
	if !sawSoft {
		t.Fatalf("expected soft watermark alert")
	}
	if !sawHard {
		t.Fatalf("expected hard watermark alert")
	}
}

func TestHybridQueueStatsAccuracy(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "stats",
		RingCapacity:     4,
		OverflowCapacity: 2,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}
	t.Cleanup(queue.Close)

	for i := 0; i < 3; i++ {
		if err := queue.Enqueue(newTestHybridJob(i)); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	for i := 0; i < 2; i++ {
		job, err := queue.Dequeue()
		if err != nil || job == nil {
			t.Fatalf("dequeue failed: %v", err)
		}
	}

	stats := queue.Stats()
	if stats.QueueDepth != 1 {
		t.Fatalf("queue depth = %d, want 1", stats.QueueDepth)
	}
	if stats.Enqueued != 3 {
		t.Fatalf("enqueued = %d, want 3", stats.Enqueued)
	}
	if stats.Dequeued != 2 {
		t.Fatalf("dequeued = %d, want 2", stats.Dequeued)
	}
	if stats.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", stats.Dropped)
	}
}

func TestHybridQueueCloseSemantics(t *testing.T) {
	cfg := HybridQueueConfig{
		Name:             "close",
		RingCapacity:     2,
		OverflowCapacity: 0,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
	queue, err := NewHybridQueue(cfg)
	if err != nil {
		t.Fatalf("failed to create hybrid queue: %v", err)
	}

	queue.Close()

	if err := queue.Enqueue(newTestHybridJob(1)); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed, got %v", err)
	}

	if _, err := queue.Dequeue(); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed on dequeue, got %v", err)
	}
}
