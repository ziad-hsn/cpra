package queue

import (
	"math"
	"testing"
	"time"
)

func TestDurationMetricsWindowAndZeroVariability(t *testing.T) {
	var m durationMetrics
	if _, cv, n := m.snapshot(); cv != 1 || n != 0 {
		t.Fatal(cv, n)
	}
	m.observe(time.Hour)
	for i := 0; i < variabilityWindow; i++ {
		m.observe(10 * time.Millisecond)
	}
	mean, cv, n := m.snapshot()
	if math.Abs(mean-.01) > 1e-12 || cv != 0 || n != variabilityWindow {
		t.Fatal(mean, cv, n)
	}
}
