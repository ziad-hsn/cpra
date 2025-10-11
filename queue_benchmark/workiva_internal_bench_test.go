package queue_benchmark

import (
    "testing"

    cpraqueue "cpra/internal/queue"
)

// Compares internal Workiva-style expanding queue against Adaptive in bench_test.go
func BenchmarkWorkivaExpQueueEnqueueDequeue(b *testing.B) {
    q := cpraqueue.NewWorkivaQueue(1 << 18)
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

