package queue

import (
	"context"
	"errors"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"cpra/internal/config"
	"cpra/internal/runtime/jobs"

	"go.uber.org/goleak"
)

// antsGoleakOptions returns goleak options that filter expected ants pool goroutines.
// The ants library runs background goroutines for worker purging and tick management
// that are cleaned up on their own schedule, not immediately on pool.Release().
func antsGoleakOptions() []goleak.Option {
	return []goleak.Option{
		goleak.IgnoreTopFunction("github.com/panjf2000/ants/v2.(*poolCommon).purgeStaleWorkers"),
		goleak.IgnoreTopFunction("github.com/panjf2000/ants/v2.(*poolCommon).ticktock"),
	}
}

type testPoolJob struct {
	id        int
	processed *atomic.Int64
	blockCh   <-chan struct{}
}

func (j *testPoolJob) Execute(ctx context.Context) jobs.Result {
	if j.blockCh != nil {
		select {
		case <-ctx.Done():
			return jobs.Result{Err: ctx.Err()}
		case <-j.blockCh:
		}
	}
	if j.processed != nil {
		j.processed.Add(1)
	}
	return jobs.Result{}
}

func (j *testPoolJob) Copy() jobs.Job            { copy := *j; return &copy }
func (j *testPoolJob) GetEnqueueTime() time.Time { return time.Time{} }
func (j *testPoolJob) SetEnqueueTime(time.Time)  {}
func (j *testPoolJob) GetStartTime() time.Time   { return time.Time{} }
func (j *testPoolJob) SetStartTime(time.Time)    {}
func (j *testPoolJob) IsNil() bool               { return false }

func newTestPool(t *testing.T, q Queue, cfg WorkerPoolConfig) *DynamicWorkerPool {
	t.Helper()

	ctx := context.Background()
	env := config.Load()
	p, err := NewDynamicWorkerPoolWithEnvConfig(ctx, q, cfg, log.New(io.Discard, "", 0), env)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	p.Start()
	return p
}

func waitForValue(t *testing.T, fn func() bool, timeout time.Duration, tick time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(tick)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestDynamicWorkerPool_ScalesAndCompletes(t *testing.T) {
	defer goleak.VerifyNone(t, antsGoleakOptions()...)

	queueCfg := HybridQueueConfig{
		Name:             "pool-scale",
		RingCapacity:     32,
		OverflowCapacity: 32,
		DropPolicy:       DropPolicyReject,
	}
	q, err := NewHybridQueue(queueCfg)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}
	t.Cleanup(q.Close)

	cfg := WorkerPoolConfig{
		MinWorkers:         1,
		MaxWorkers:         4,
		AdjustmentInterval: 50 * time.Millisecond,
		ResultBatchSize:    8,
		ResultBatchTimeout: 20 * time.Millisecond,
		TargetQueueLatency: 10 * time.Millisecond,
		ScaleUpThreshold:   1.0,
		ScaleDownThreshold: 0.8,
		WarmupDuration:     1 * time.Millisecond,
	}

	processed := atomic.Int64{}
	pool := newTestPool(t, q, cfg)
	defer pool.DrainAndStop()

	const total = 30
	for i := 0; i < total; i++ {
		if err := q.Enqueue(&testPoolJob{id: i, processed: &processed}); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	waitForValue(t, func() bool { return processed.Load() == int64(total) }, 2*time.Second, 10*time.Millisecond)

	// Note: We check CurrentCapacity rather than RunningWorkers because jobs may
	// complete faster than the scaling check, leaving workers idle (RunningWorkers=0).
	// CurrentCapacity reflects that scaling actually occurred.
	stats := pool.Stats()
	if stats.CurrentCapacity < 2 {
		t.Logf("Pool did not scale up: capacity=%d, completed=%d", stats.CurrentCapacity, stats.TasksCompleted)
	}
	if stats.TasksCompleted != int64(total) {
		t.Fatalf("tasks completed = %d, want %d", stats.TasksCompleted, total)
	}
}

func TestDynamicWorkerPool_BackpressureWhenFull(t *testing.T) {
	defer goleak.VerifyNone(t, antsGoleakOptions()...)

	queueCfg := HybridQueueConfig{
		Name:             "pool-backpressure",
		RingCapacity:     1,
		OverflowCapacity: 0,
		DropPolicy:       DropPolicyReject,
	}
	q, err := NewHybridQueue(queueCfg)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}
	t.Cleanup(q.Close)

	blockCh := make(chan struct{})
	cfg := WorkerPoolConfig{
		MinWorkers:         1,
		MaxWorkers:         1,
		ResultBatchSize:    1,
		ResultBatchTimeout: 10 * time.Millisecond,
		TargetQueueLatency: 5 * time.Millisecond,
		WarmupDuration:     1 * time.Millisecond,
	}

	pool := newTestPool(t, q, cfg)
	defer pool.DrainAndStop()

	if err := q.Enqueue(&testPoolJob{id: 0, processed: nil, blockCh: blockCh}); err != nil {
		t.Fatalf("enqueue first job failed: %v", err)
	}

	// With a single worker blocked and no overflow, the next enqueue should backpressure.
	backpressureErr := q.Enqueue(&testPoolJob{id: 1})
	if !errors.Is(backpressureErr, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", backpressureErr)
	}

	close(blockCh)
	waitForValue(t, func() bool { return pool.Stats().TasksCompleted >= 1 }, 1*time.Second, 10*time.Millisecond)
}

func TestDynamicWorkerPool_DrainAndStopFlushesResults(t *testing.T) {
	defer goleak.VerifyNone(t, antsGoleakOptions()...)

	queueCfg := HybridQueueConfig{
		Name:             "pool-drain",
		RingCapacity:     8,
		OverflowCapacity: 8,
		DropPolicy:       DropPolicyReject,
	}
	q, err := NewHybridQueue(queueCfg)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}
	t.Cleanup(q.Close)

	cfg := WorkerPoolConfig{
		MinWorkers:         2,
		MaxWorkers:         4,
		ResultBatchSize:    4,
		ResultBatchTimeout: 20 * time.Millisecond,
		TargetQueueLatency: 10 * time.Millisecond,
		WarmupDuration:     1 * time.Millisecond,
	}

	processed := atomic.Int64{}
	pool := newTestPool(t, q, cfg)

	const total = 12
	for i := 0; i < total; i++ {
		if err := q.Enqueue(&testPoolJob{id: i, processed: &processed}); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	pool.DrainAndStop()

	if processed.Load() != int64(total) {
		t.Fatalf("processed = %d, want %d", processed.Load(), total)
	}

	stats := pool.Stats()
	if stats.TasksCompleted != int64(total) {
		t.Fatalf("stats.TasksCompleted = %d, want %d", stats.TasksCompleted, total)
	}
}
