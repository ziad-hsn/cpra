package queue

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

type pendingAgeJob struct{ id int }

func (j *pendingAgeJob) Execute() jobs.Result      { return jobs.Result{} }
func (j *pendingAgeJob) Copy() jobs.Job            { return &pendingAgeJob{id: j.id} }
func (j *pendingAgeJob) GetEnqueueTime() time.Time { return time.Time{} }
func (j *pendingAgeJob) SetEnqueueTime(time.Time)  {}
func (j *pendingAgeJob) GetStartTime() time.Time   { return time.Time{} }
func (j *pendingAgeJob) SetStartTime(time.Time)    {}
func (j *pendingAgeJob) IsNil() bool               { return j == nil }
func (j *pendingAgeJob) TracksEnqueueTime() bool   { return false }

// Keep this benchmark on the production queue path so pending-age bookkeeping
// can be compared with the same workload and compiler before a change.
func BenchmarkHybridPendingAgeRoundTrip(b *testing.B) {
	for _, parallel := range []bool{false, true} {
		name := "serial"
		if parallel {
			name = "parallel"
		}
		b.Run(name, func(b *testing.B) {
			q, err := NewHybridQueue(DefaultHybridQueueConfig())
			if err != nil {
				b.Fatal(err)
			}
			defer q.Close()
			job := &pendingAgeJob{}
			b.ReportAllocs()
			b.ResetTimer()
			roundTrip := func() {
				if err := q.Enqueue(job); err != nil {
					b.Fatal(err)
				}
				if _, err := q.Dequeue(); err != nil {
					b.Fatal(err)
				}
			}
			if parallel {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						roundTrip()
					}
				})
			} else {
				for b.Loop() {
					roundTrip()
				}
			}
		})
	}
}

// A standing backlog makes age useful even while publishers are busy. Report
// coverage rather than assuming that a race-safe observation is always present.
func BenchmarkHybridPendingAgeObservationCoverage(b *testing.B) {
	q, err := NewHybridQueue(DefaultHybridQueueConfig())
	if err != nil {
		b.Fatal(err)
	}
	defer q.Close()
	job := &pendingAgeJob{}
	for i := 0; i < 4096; i++ {
		if err := q.Enqueue(job); err != nil {
			b.Fatal(err)
		}
	}
	var available atomic.Uint64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := q.Enqueue(job); err != nil {
				b.Fatal(err)
			}
			if _, err := q.Dequeue(); err != nil {
				b.Fatal(err)
			}
			if q.Stats().OldestPendingAvailable {
				available.Add(1)
			}
		}
	})
	b.StopTimer()
	b.ReportMetric(100*float64(available.Load())/float64(b.N), "age-available-%")
}
