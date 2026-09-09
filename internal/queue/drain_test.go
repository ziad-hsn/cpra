package queue

import (
	"cpra/internal/jobs"
	"io"
	"log"
	"testing"
	"time"
)

type blockingJob struct {
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (j *blockingJob) Execute() jobs.Result {
	close(j.entered)
	<-j.release
	close(j.finished)
	return jobs.Result{Type: "pulse"}
}

func (j *blockingJob) Copy() jobs.Job            { return j }
func (j *blockingJob) GetEnqueueTime() time.Time { return time.Time{} }
func (j *blockingJob) SetEnqueueTime(time.Time)  {}
func (j *blockingJob) GetStartTime() time.Time   { return time.Time{} }
func (j *blockingJob) SetStartTime(time.Time)    {}
func (j *blockingJob) IsNil() bool               { return j == nil }

func TestDrainWaitsForExecutingJobs(t *testing.T) {
	q, err := NewHybridQueue(HybridQueueConfig{RingCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 1, 1
	cfg.AdjustmentInterval = 0
	pool, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	job := &blockingJob{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	pool.Start()
	select {
	case <-job.entered:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	stopped := make(chan struct{})
	go func() { pool.DrainAndStop(); close(stopped) }()
	select {
	case <-stopped:
		close(job.release)
		<-job.finished
		t.Fatal("DrainAndStop returned before the running job finished; job executed after shutdown returned")
	case <-time.After(100 * time.Millisecond):
		close(job.release)
		<-job.finished
		<-stopped
	}
}
