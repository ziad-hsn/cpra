package queue

import (
	"math"
	"sync"
	"time"
)

const variabilityWindow = 256

// durationMetrics retains a bounded window of observations. The mutex owns
// both the sample ring and the previous arrival timestamp. It is never held
// while a job executes or while queue admission blocks.
type durationMetrics struct {
	mu          sync.Mutex
	samples     [variabilityWindow]float64
	count, next int
	last        time.Time
}

func (m *durationMetrics) add(seconds float64) {
	m.samples[m.next] = seconds
	m.next = (m.next + 1) % variabilityWindow
	if m.count < variabilityWindow {
		m.count++
	}
}

func (m *durationMetrics) recordArrival() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Read the clock under the lock to order concurrent successful admissions.
	now := time.Now()
	if !m.last.IsZero() {
		m.add(now.Sub(m.last).Seconds())
	}
	m.last = now
}

func (m *durationMetrics) observe(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.add(d.Seconds())
	m.mu.Unlock()
}

// snapshot returns mean seconds, coefficient of variation and sample count.
// A CV of one supplies the M/M/c baseline until two samples are available.
func (m *durationMetrics) snapshot() (mean, cv float64, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var m2 float64
	for i := 0; i < m.count; i++ {
		delta := m.samples[i] - mean
		mean += delta / float64(i+1)
		m2 += delta * (m.samples[i] - mean)
	}
	cv = 1
	if m.count >= 2 && mean > 0 {
		cv = math.Sqrt(math.Max(0, m2/float64(m.count))) / mean
	}
	return mean, cv, m.count
}
