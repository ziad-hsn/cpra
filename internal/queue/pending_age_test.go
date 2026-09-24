package queue

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pendingQueue(t *testing.T, kind string) Queue {
	t.Helper()
	var q Queue
	var err error
	switch kind {
	case "hybrid":
		q, err = NewHybridQueue(HybridQueueConfig{RingCapacity: 2, OverflowCapacity: 2})
	case "adaptive":
		q, err = NewAdaptiveQueue(4)
	case "workiva":
		q = NewWorkivaQueue(2)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	return q
}

func agesFor(q Queue) *pendingAges {
	switch q := q.(type) {
	case *HybridQueue:
		return &q.pending
	case *AdaptiveQueue:
		return &q.pending
	case *WorkivaQueue:
		return &q.pending
	default:
		panic("unsupported test queue")
	}
}

func agePending(q Queue, age time.Duration) {
	p := agesFor(q)
	p.mu.Lock()
	p.first.at = time.Since(p.epoch) - age
	p.mu.Unlock()
}

func requirePendingAge(t *testing.T, q Queue, minimum time.Duration) {
	t.Helper()
	s := q.Stats()
	if !s.OldestPendingAvailable || s.OldestPendingAge < minimum || s.OldestPendingAge > minimum+time.Second {
		t.Fatalf("pending age: available=%v age=%v reason=%q want=[%v,%v]", s.OldestPendingAvailable, s.OldestPendingAge, s.OldestPendingReason, minimum, minimum+time.Second)
	}
}

func requireEmptyAge(t *testing.T, q Queue) {
	t.Helper()
	s := q.Stats()
	if s.OldestPendingAvailable || s.OldestPendingAge != 0 || s.OldestPendingReason != "queue_empty" {
		t.Fatalf("empty queue claimed an age: %+v", s)
	}
}

func TestPendingAgeAdmissionSettlementAndConsumerOwnership(t *testing.T) {
	var ages pendingAges
	first := ages.begin(&pendingAgeJob{id: 1})
	second := ages.begin(&pendingAgeJob{id: 2})
	ages.finish(second, true)
	if _, available, reason := ages.observe(); available || reason != "admission_in_progress" {
		t.Fatal("provisional oldest candidate was measured")
	}
	ages.finish(first, false)
	if _, available, _ := ages.observe(); !available {
		t.Fatal("rejected candidate hid accepted work")
	}
	third := ages.begin(&pendingAgeJob{id: 3})
	if _, available, _ := ages.observe(); !available {
		t.Fatal("newer provisional candidate hid the admitted oldest")
	}
	if ages.take(second).(*pendingAgeJob).id != 2 {
		t.Fatal("wrong pending job removed")
	}
	// A fast consumer may remove the published copy before its producer returns.
	if ages.take(third).(*pendingAgeJob).id != 3 || third.listed || third.settled {
		t.Fatal("premature producer completion")
	}
	fourth := ages.begin(&pendingAgeJob{id: 4})
	if third == fourth {
		t.Fatal("entry reused while the producer still owned it")
	}
	ages.finish(third, true)
	ages.finish(fourth, false)
	if _, available, reason := ages.observe(); available || reason != "queue_empty" {
		t.Fatal("completed admissions remained pending")
	}
}

func TestPendingAgeClocksAreNotTakenFromMutableJobs(t *testing.T) {
	for _, kind := range []string{"hybrid", "adaptive", "workiva"} {
		t.Run(kind, func(t *testing.T) {
			q := pendingQueue(t, kind)
			requireEmptyAge(t, q)
			// These immutable jobs deliberately report no enqueue timestamp.
			if err := q.Enqueue(&pendingAgeJob{id: 1}); err != nil {
				t.Fatal(err)
			}
			agePending(q, 10*time.Second)
			if err := q.Enqueue(&pendingAgeJob{id: 2}); err != nil {
				t.Fatal(err)
			}
			requirePendingAge(t, q, 10*time.Second)
			job, err := q.Dequeue()
			if err != nil || job.(*pendingAgeJob).id != 1 {
				t.Fatal("unexpected dequeue", err)
			}
			// The removed copy may execute forever; only the still-pending copy ages.
			requirePendingAge(t, q, 0)
			if _, err := q.Dequeue(); err != nil {
				t.Fatal(err)
			}
			requireEmptyAge(t, q)
		})
	}
}

func TestPendingAgeDropPoliciesAndRejectedAdmissions(t *testing.T) {
	for _, policy := range []DropPolicy{DropPolicyReject, DropPolicyDropNewest, DropPolicyDropOldest} {
		t.Run(policy.String(), func(t *testing.T) {
			q, err := NewHybridQueue(HybridQueueConfig{RingCapacity: 1, OverflowCapacity: 1, DropPolicy: policy})
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			for _, id := range []int{1, 2} {
				if err := q.Enqueue(&pendingAgeJob{id: id}); err != nil {
					t.Fatal(err)
				}
			}
			q.dequeueTurn.Store(1) // Remove the ring copy, leaving the older overflow copy.
			if job, err := q.Dequeue(); err != nil || job.(*pendingAgeJob).id != 1 {
				t.Fatal("wrong fair-path removal", err)
			}
			agePending(q, 10*time.Second)
			if err := q.Enqueue(&pendingAgeJob{id: 3}); err != nil {
				t.Fatal(err)
			}
			err = q.Enqueue(&pendingAgeJob{id: 4})
			if policy == DropPolicyDropOldest {
				if err != nil {
					t.Fatal(err)
				}
				requirePendingAge(t, q, 0)
			} else {
				if !errors.Is(err, ErrQueueFull) {
					t.Fatal("full queue accepted candidate", err)
				}
				requirePendingAge(t, q, 10*time.Second)
			}
			batch, err := q.DequeueBatch(10)
			if err != nil || len(batch) != 2 {
				t.Fatal("drop changed retained queue size", err, len(batch))
			}
			requireEmptyAge(t, q)
		})
	}
	q := pendingQueue(t, "adaptive")
	for i := 0; i < 4; i++ {
		if err := q.Enqueue(&pendingAgeJob{id: i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(&pendingAgeJob{}); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if _, err := q.DequeueBatch(4); err != nil {
		t.Fatal(err)
	}
	requireEmptyAge(t, q)
}

func TestPendingAgeWorkivaExpansionCloseAndQueueReplacement(t *testing.T) {
	q := pendingQueue(t, "workiva")
	for i := 0; i < 20; i++ {
		if err := q.Enqueue(&pendingAgeJob{id: i}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		job, err := q.Dequeue()
		if err != nil || job.(*pendingAgeJob).id != i {
			t.Fatal("expanded FIFO changed", err)
		}
	}
	requireEmptyAge(t, q)
	if err := q.Enqueue(&pendingAgeJob{}); err != nil {
		t.Fatal(err)
	}
	q.Close()
	if s := q.Stats(); s.OldestPendingAvailable || s.OldestPendingReason != "queue_closed" {
		t.Fatal("disposed work reported as pending", s)
	}
	for _, kind := range []string{"hybrid", "adaptive"} {
		old := pendingQueue(t, kind)
		handle := NewHandle(old)
		if err := handle.Enqueue(&pendingAgeJob{}); err != nil {
			t.Fatal(err)
		}
		agePending(old, 10*time.Second)
		if err := handle.ReplaceEmpty(pendingQueue(t, kind)); err == nil {
			t.Fatal("nonempty replacement was permitted")
		}
		old.Close()
		requirePendingAge(t, old, 10*time.Second) // These implementations permit drain after Close.
		if _, err := handle.Dequeue(); err != nil {
			t.Fatal(err)
		}
		if err := handle.ReplaceEmpty(pendingQueue(t, kind)); err != nil {
			t.Fatal(err)
		}
		requireEmptyAge(t, handle)
	}
}

func TestPendingAgeInvalidClockObservation(t *testing.T) {
	q := pendingQueue(t, "hybrid")
	if err := q.Enqueue(&pendingAgeJob{}); err != nil {
		t.Fatal(err)
	}
	p := agesFor(q)
	p.mu.Lock()
	p.first.at = time.Since(p.epoch) + time.Hour
	p.mu.Unlock()
	if s := q.Stats(); s.OldestPendingAvailable || s.OldestPendingReason != "invalid_clock_observation" {
		t.Fatal("negative age fabricated", s)
	}
}

func TestPendingAgeConcurrentStatsAndAdmissions(t *testing.T) {
	for _, kind := range []string{"hybrid", "adaptive", "workiva"} {
		t.Run(kind, func(t *testing.T) {
			q := pendingQueue(t, kind)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var accepted, removed atomic.Int64
			var done atomic.Bool
			var producers, consumers sync.WaitGroup
			for n := 0; n < 4; n++ {
				producers.Add(1)
				go func() {
					defer producers.Done()
					for i := 0; i < 1000; i++ {
						for ctx.Err() == nil {
							err := q.Enqueue(&pendingAgeJob{id: i})
							if err == nil {
								accepted.Add(1)
								break
							}
							if !errors.Is(err, ErrQueueFull) {
								t.Error(err)
								cancel()
								return
							}
							runtime.Gosched()
						}
					}
				}()
			}
			for n := 0; n < 2; n++ {
				consumers.Add(1)
				go func() {
					defer consumers.Done()
					for ctx.Err() == nil {
						job, err := q.Dequeue()
						if err != nil {
							t.Error(err)
							cancel()
							return
						}
						if job != nil {
							removed.Add(1)
						}
						s := q.Stats()
						if s.OldestPendingAvailable && s.OldestPendingAge < 0 {
							t.Error("negative pending age")
							cancel()
							return
						}
						if done.Load() && removed.Load() == accepted.Load() {
							return
						}
						runtime.Gosched()
					}
				}()
			}
			producers.Wait()
			done.Store(true)
			consumers.Wait()
			if ctx.Err() != nil || accepted.Load() != 4000 || removed.Load() != 4000 {
				t.Fatal("concurrent queue did not drain", ctx.Err(), accepted.Load(), removed.Load())
			}
			requireEmptyAge(t, q)
		})
	}
}
