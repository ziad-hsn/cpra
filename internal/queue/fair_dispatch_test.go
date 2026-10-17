package queue

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

type cancelFairJob struct{ jobs.Execution }

func (j *cancelFairJob) Execute() jobs.Result {
	<-j.Context().Done()
	return jobs.Result{Type: "pulse", Err: j.Context().Err()}
}
func (j *cancelFairJob) Copy() jobs.Job            { return &cancelFairJob{} }
func (j *cancelFairJob) GetEnqueueTime() time.Time { return time.Time{} }
func (j *cancelFairJob) SetEnqueueTime(time.Time)  {}
func (j *cancelFairJob) GetStartTime() time.Time   { return time.Time{} }
func (j *cancelFairJob) SetStartTime(time.Time)    {}
func (j *cancelFairJob) IsNil() bool               { return j == nil }

func TestFairDispatchCancellationBackpressureAndDrain(t *testing.T) {
	q, err := NewFairQueue(100, "http", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultWorkerPoolConfig()
	cfg.FairDispatch = true
	cfg.MinWorkers = 2
	cfg.MaxWorkers = 4
	cfg.NumShards = 1
	cfg.AdjustmentInterval = 0
	cfg.ResultChannelDepth = 1
	cfg.ResultBatchSize = 1
	cfg.ResultBatchTimeout = time.Millisecond
	p, err := NewDynamicWorkerPool(NewHandle(q), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 40; n++ {
		d := jobs.NewDispatch(&cancelFairJob{}, ecs.Entity{}, "pulse", "", 1, 0)
		d.Driver = "http"
		if n%2 == 1 {
			d.Driver = "tcp"
		}
		d.FlowKey = uint64(n)
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	p.SetTargetWorkers(4)
	p.Start()
	deadline := time.Now().Add(time.Second)
	for p.Stats().ActiveExecutions < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.Stats().ActiveExecutions < 2 {
		p.CancelWork()
		t.Fatal("fair dispatcher did not execute both drivers")
	}
	p.SetTargetWorkers(2) // Existing reservations drain; reduction grants no new excess work.
	p.CancelWork()
	time.Sleep(20 * time.Millisecond) // Let the one-slot result route exert backpressure.
	stopped := make(chan struct{})
	go func() { p.DrainAndStop(); close(stopped) }()
	received := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		select {
		case batch, ok := <-p.GetRouter().PulseResultChan:
			if !ok {
				<-stopped
				if received != 40 || q.Stats().QueueDepth != 0 || p.Stats().ActiveExecutions != 0 || p.Stats().PendingSubmissions != 0 {
					t.Fatalf("drain lost accepted work: %d", received)
				}
				q.mu.Lock()
				defer q.mu.Unlock()
				if q.active != 0 || len(q.drivers["http"].flows) != 0 || len(q.drivers["tcp"].flows) != 0 {
					t.Fatal("flow reservations survived drain")
				}
				return
			}
			received += len(batch)
		case <-ctx.Done():
			t.Fatal("fair dispatcher failed to drain")
		}
	}
}
