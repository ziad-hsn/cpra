package slo

import (
	"math"
	"testing"
	"time"
)

func TestPauseExposureDoesNotEraseMissesOrInventHealthyChecks(t *testing.T) {
	at := time.Unix(1700000400, 0)
	r := New(at.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	r.Expect("http", at)
	r.Missed("http", at, 3)
	r.ChangePaused("http", 1, at.Add(time.Second))
	r.ChangePaused("http", 1, at.Add(3*time.Second))
	r.ChangePaused("http", -1, at.Add(8*time.Second))
	v := r.View(at.Add(11*time.Second), 5*time.Minute, 1).Reports[0]
	if v.PausedMonitors != 1 || v.PausedMonitorSeconds != 15 || v.Samples != 0 || v.Missed != 3 || v.Overdue != 1 || v.Expected != 4 || *v.ResultAttainment != 0 {
		t.Fatalf("pause changed check evidence: %+v", v)
	}
	r.ChangePaused("http", -1, at.Add(11*time.Second))
	if v = r.View(at.Add(20*time.Second), 5*time.Minute, 1).Reports[0]; v.PausedMonitors != 0 || v.PausedMonitorSeconds != 15 {
		t.Fatal(v)
	}
}

func TestPauseExposureRestartAndWindowBounds(t *testing.T) {
	at := time.Unix(1700000400, 0)
	r := New(at.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	r.ChangePaused("tcp", 1, at)
	snapshot := r.Snapshot(at.Add(10 * time.Second))
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	restored := New(at, 250*time.Millisecond, 5*time.Second)
	restored.Restore(snapshot, at.Add(20*time.Second))
	v := restored.View(at.Add(21*time.Second), 5*time.Minute, 1)
	if v.CoverageComplete || v.Reports[0].PausedMonitors != 0 || v.Reports[0].PausedMonitorSeconds != 10 {
		t.Fatal(v)
	}
	// Only the new owner may re-establish membership. The recovery gap is not
	// silently counted as measured exposure or as successful checks.
	restored.ChangePaused("tcp", 1, at.Add(21*time.Second))
	v = restored.View(at.Add(24*time.Hour), 5*time.Minute, 1)
	if v.Reports[0].PausedMonitors != 1 || v.Reports[0].PausedMonitorSeconds != 295 || v.Reports[0].Expected != 0 || v.Reports[0].ResultAttainment != nil {
		t.Fatal(v)
	}
	// Existing five-second buckets exclude the partially overlapping oldest
	// bucket; exposure follows precisely the same boundary as check samples.
	if len(restored.state.ByDriver) != 1 || len(restored.pauses) != 1 {
		t.Fatal("pause exposure grew with elapsed duration")
	}
}

func TestPauseExposureClockAndCorruptSnapshot(t *testing.T) {
	at := time.Unix(1700000400, 0)
	r := New(at.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	r.ChangePaused("http", -1, at) // No underflow on an absent membership.
	r.ChangePaused("http", 1, at)
	r.Snapshot(at.Add(10 * time.Second))
	r.View(at.Add(5*time.Second), 5*time.Minute, 1)
	v := r.View(at.Add(12*time.Second), 5*time.Minute, 1).Reports[0]
	if v.PausedMonitors != 1 || v.PausedMonitorSeconds != 12 {
		t.Fatal(v)
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		s := r.Snapshot(at.Add(12 * time.Second))
		s.ByDriver["http"][0].PausedSeconds = bad
		if s.Validate() == nil {
			t.Fatal("accepted corrupt pause duration")
		}
	}
}
