package slo

import (
	"testing"
	"time"
)

func TestPercentilesAndExactAttainment(t *testing.T) {
	if len(bounds) >= BucketCount {
		t.Fatal("histogram exceeds fixed storage")
	}
	now := time.Unix(1700000400, 0)
	r := New(now.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	for n := 0; n < 1000; n++ {
		delay := 100 * time.Millisecond
		if n >= 990 {
			delay = 300 * time.Millisecond
		}
		r.Observe("http", now, now.Add(delay), now.Add(delay+time.Millisecond), now.Add(delay+2*time.Millisecond), false, 0)
	}
	v := r.View(now.Add(time.Second), 5*time.Minute, 1000)
	s := v.Reports[0]
	if s.QueueMet != 990 || s.ResultMet != 1000 || s.Queue.P99 == nil || *s.Queue.P99 > 110 || *s.Queue.P99 < 100 {
		t.Fatalf("incorrect distribution: %+v", s)
	}
	r.Observe("http", now, now.Add(time.Millisecond), now.Add(6*time.Second), now.Add(6*time.Second), true, 10)
	s = r.View(now.Add(10*time.Second), 5*time.Minute, 1000).Reports[0]
	if s.Expected != 1011 || s.Timeouts != 1 || s.ResultMet != 1000 || *s.ResultAttainment >= .99 {
		t.Fatalf("omissions improved attainment: %+v", s)
	}
}

func TestWindowExpiryAndRecoveryGap(t *testing.T) {
	now := time.Unix(1700000400, 0)
	r := New(now, 250*time.Millisecond, 5*time.Second)
	r.Observe("tcp", now, now, now.Add(time.Millisecond), now.Add(time.Millisecond), false, 0)
	s := r.Snapshot(now.Add(time.Second))
	copy := s.Clone()
	copy.ByDriver["tcp"] = &[Slots]Bucket{}
	r.Restore(s, now.Add(10*time.Second))
	v := r.View(now.Add(11*time.Second), 5*time.Minute, 1)
	if v.CoverageComplete || v.Reports[0].Samples != 1 {
		t.Fatal(v)
	}
	v = r.View(now.Add(6*time.Minute), 5*time.Minute, 1)
	if !v.CoverageComplete || v.Reports[0].Samples != 0 {
		t.Fatal(v)
	}
}

func TestUnfinishedScheduledWorkCannotImproveAttainment(t *testing.T) {
	now := time.Unix(1700000400, 0)
	r := New(now.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	for n := 0; n < 1000; n++ {
		r.Expect("http", now)
		if n < 980 {
			r.Observe("http", now, now.Add(time.Millisecond), now.Add(2*time.Millisecond), now.Add(3*time.Millisecond), false, 0)
		}
	}
	v := r.View(now.Add(11*time.Second), 5*time.Minute, 1).Reports[0]
	if v.Expected != 1000 || v.Overdue != 20 || v.Pending != 0 || *v.ResultAttainment != .98 || v.Condition == "healthy" {
		t.Fatal(v)
	}
	r.Missed("http", now.Add(12*time.Second), 5)
	// A late completion replaces its existing obligation, never a second sample.
	r.Observe("http", now, now.Add(11*time.Second), now.Add(12*time.Second), now.Add(13*time.Second), false, 0)
	v = r.View(now.Add(15*time.Second), 5*time.Minute, 1).Reports[0]
	if v.Expected != 1005 || v.Overdue != 19 || v.Samples != 981 || v.Missed != 5 {
		t.Fatal(v)
	}
	restored := New(now, 250*time.Millisecond, 5*time.Second)
	restored.Restore(r.Snapshot(now.Add(15*time.Second)), now.Add(16*time.Second))
	if got := restored.View(now.Add(17*time.Second), 5*time.Minute, 1).Reports[0]; got.Overdue != 19 {
		t.Fatal(got)
	}
}

func BenchmarkObserve(b *testing.B) {
	now := time.Now()
	r := New(now, 250*time.Millisecond, 5*time.Second)
	r.Expect("http", now)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		r.Observe("http", now, now.Add(time.Millisecond), now.Add(2*time.Millisecond), now.Add(3*time.Millisecond), false, 0)
	}
}
