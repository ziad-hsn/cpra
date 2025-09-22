package queue_benchmark

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"

	cprajobs "cpra/internal/jobs"
	cpraqueue "cpra/internal/queue"
)

type noopJob struct{}

func (n *noopJob) Execute() cprajobs.Result  { return cprajobs.Result{} }
func (n *noopJob) Copy() cprajobs.Job        { return &noopJob{} }
func (n *noopJob) GetEnqueueTime() time.Time { return time.Time{} }
func (n *noopJob) SetEnqueueTime(time.Time)  {}
func (n *noopJob) GetStartTime() time.Time   { return time.Time{} }
func (n *noopJob) SetStartTime(time.Time)    {}

func BenchmarkAdaptiveQueueEnqueueDequeue(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(1 << 18)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}
	job := &noopJob{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.Enqueue(job); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			b.Fatalf("dequeue: %v", err)
		}
	}
}

func BenchmarkAsynqClientEnqueue(b *testing.B) {
	redisServer := miniredis.RunT(b)
	defer redisServer.Close()

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	defer client.Close()

	payload := []byte("{}")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := asynq.NewTask("bench", payload)
		if _, err := client.Enqueue(task); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
	}
}
