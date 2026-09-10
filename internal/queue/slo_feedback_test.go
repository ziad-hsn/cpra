package queue

import (
	"testing"
	"time"

	"cpra/internal/runtimeconfig"
	"cpra/internal/slo"
)

func TestPercentileFeedbackBoundsAndHealthyHold(t *testing.T) {
	now := time.Unix(1700000400, 0)
	cfg := runtimeconfig.Default().SLO
	r := slo.New(now.Add(-time.Hour), cfg.QueueTarget, cfg.ResultTarget)
	f := feedback{recorder: r, config: cfg}
	for n := 0; n < 1000; n++ {
		r.Observe("http", now, now.Add(time.Second), now.Add(1100*time.Millisecond), now.Add(1200*time.Millisecond), false, 0)
	}
	if got := f.adjust(100, 80, 200, 100, now.Add(2*time.Second)); got != 125 {
		t.Fatalf("burst response %d", got)
	}
	for step := 1; step <= 20; step++ {
		at := now.Add(time.Duration(step) * 5 * time.Second)
		for n := 0; n < 1000; n++ {
			r.Observe("http", at, at, at.Add(time.Millisecond), at.Add(2*time.Millisecond), false, 0)
		}
		got := f.adjust(125, 80, 200, 0, at.Add(time.Second))
		if step < 18 && got < 125 {
			t.Fatalf("shrunk before healthy hold at %d", step)
		}
		if got < 113 {
			t.Fatalf("shrunk by over ten percent: %d", got)
		}
	}
}

func TestSlowTargetsDoNotTriggerWorkerFeedbackGrowth(t *testing.T) {
	now := time.Unix(1700000400, 0)
	cfg := runtimeconfig.Default().SLO
	r := slo.New(now.Add(-time.Hour), cfg.QueueTarget, cfg.ResultTarget)
	for n := 0; n < 1000; n++ {
		r.Observe("http", now, now.Add(time.Second), now.Add(10*time.Second), now.Add(10*time.Second), true, 0)
	}
	f := feedback{recorder: r, config: cfg}
	if got := f.adjust(100, 80, 1000, 1000, now.Add(11*time.Second)); got != 100 {
		t.Fatal(got)
	}
	if c := f.condition.Load(); c == nil || *c != "downstream_limited" {
		t.Fatal(c)
	}
}
