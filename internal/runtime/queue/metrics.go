package queue

import (
	"math"
	"sort"
	"sync"
	"time"
)

// defaultRollingWaitSize bounds memory for rolling wait metrics.
const defaultRollingWaitSize = 1024

// RollingWait is a fixed-size ring buffer for recent wait times.
// It is intentionally simple: O(window) reads, constant memory, stdlib-only.
type RollingWait struct {
	mu     sync.RWMutex
	buf    []time.Duration
	idx    int
	filled bool
}

// NewRollingWait creates a rolling wait buffer with the given size.
func NewRollingWait(size int) *RollingWait {
	if size <= 0 {
		size = defaultRollingWaitSize
	}
	return &RollingWait{
		buf: make([]time.Duration, size),
	}
}

// Record adds a wait duration to the buffer.
func (r *RollingWait) Record(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.buf[r.idx] = d
	r.idx = (r.idx + 1) % len(r.buf)
	if r.idx == 0 {
		r.filled = true
	}
	r.mu.Unlock()
}

// Snapshot returns avg, p50, p95, and sample count of buffered waits.
func (r *RollingWait) Snapshot() (avg time.Duration, p50 time.Duration, p95 time.Duration, count int) {
	r.mu.RLock()
	n := len(r.buf)
	if !r.filled {
		n = r.idx
	}
	if n == 0 {
		r.mu.RUnlock()
		return 0, 0, 0, 0
	}
	tmp := make([]time.Duration, n)
	copy(tmp, r.buf[:n])
	r.mu.RUnlock()

	sort.Slice(tmp, func(i, j int) bool { return tmp[i] < tmp[j] })

	var total time.Duration
	for _, v := range tmp {
		total += v
	}
	avg = total / time.Duration(n)
	p50 = percentile(tmp, 0.50)
	p95 = percentile(tmp, 0.95)
	return avg, p50, p95, n
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// Ceil to avoid underestimating tail percentiles on small samples.
	idx := int(math.Ceil(p*float64(len(sorted))) - 1)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// EMAWait tracks exponential moving average of wait times for responsive drift detection.
// Alpha controls responsiveness: higher = more weight on recent samples (0.1-0.3 typical).
type EMAWait struct {
	mu    sync.RWMutex
	value float64 // nanoseconds
	alpha float64
	count int64
}

// NewEMAWait creates an EMA tracker with the given alpha (default 0.2 if <= 0).
func NewEMAWait(alpha float64) *EMAWait {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &EMAWait{alpha: alpha}
}

// Record updates the EMA with a new wait duration.
func (e *EMAWait) Record(d time.Duration) {
	if d <= 0 {
		return
	}
	e.mu.Lock()
	ns := float64(d.Nanoseconds())
	if e.count == 0 {
		e.value = ns
	} else {
		e.value = e.alpha*ns + (1-e.alpha)*e.value
	}
	e.count++
	e.mu.Unlock()
}

// Value returns the current EMA as a Duration.
func (e *EMAWait) Value() time.Duration {
	e.mu.RLock()
	v := e.value
	e.mu.RUnlock()
	return time.Duration(v)
}

// Count returns the number of samples recorded.
func (e *EMAWait) Count() int64 {
	e.mu.RLock()
	c := e.count
	e.mu.RUnlock()
	return c
}

// TimeBucket stores aggregated wait stats for a single time bucket.
type TimeBucket struct {
	Sum   int64 // total wait in nanoseconds
	Max   int64 // max wait in nanoseconds
	Count int64
}

// TimeBucketedWait tracks wait times in fixed-duration buckets for time-scoped analysis.
// Useful for comparing "first minute" vs "last minute" latencies.
type TimeBucketedWait struct {
	mu         sync.RWMutex
	buckets    []TimeBucket
	bucketDur  time.Duration
	numBuckets int
	startTime  time.Time
	curIdx     int
	curStart   time.Time
}

// NewTimeBucketedWait creates a bucketed tracker with given bucket duration and count.
// Default: 1-minute buckets, 10 buckets (10-minute history).
func NewTimeBucketedWait(bucketDuration time.Duration, numBuckets int) *TimeBucketedWait {
	if bucketDuration <= 0 {
		bucketDuration = time.Minute
	}
	if numBuckets <= 0 {
		numBuckets = 10
	}
	now := time.Now()
	return &TimeBucketedWait{
		buckets:    make([]TimeBucket, numBuckets),
		bucketDur:  bucketDuration,
		numBuckets: numBuckets,
		startTime:  now,
		curStart:   now,
	}
}

// Record adds a wait duration to the current bucket.
func (t *TimeBucketedWait) Record(d time.Duration) {
	if d <= 0 {
		return
	}
	t.mu.Lock()
	t.advanceBucket()
	ns := d.Nanoseconds()
	t.buckets[t.curIdx].Sum += ns
	t.buckets[t.curIdx].Count++
	if ns > t.buckets[t.curIdx].Max {
		t.buckets[t.curIdx].Max = ns
	}
	t.mu.Unlock()
}

// advanceBucket moves to a new bucket if the current one has expired. Must hold lock.
func (t *TimeBucketedWait) advanceBucket() {
	now := time.Now()
	elapsed := now.Sub(t.curStart)
	if elapsed < t.bucketDur {
		return
	}
	// How many buckets to advance
	steps := int(elapsed / t.bucketDur)
	for i := 0; i < steps && i < t.numBuckets; i++ {
		t.curIdx = (t.curIdx + 1) % t.numBuckets
		t.buckets[t.curIdx] = TimeBucket{} // reset
	}
	t.curStart = now
}

// BucketStats returns avg and max wait for the bucket at the given age (0 = current, 1 = previous, etc).
// Returns zero values if bucket is out of range or empty.
func (t *TimeBucketedWait) BucketStats(age int) (avg time.Duration, max time.Duration, count int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if age < 0 || age >= t.numBuckets {
		return 0, 0, 0
	}
	idx := (t.curIdx - age + t.numBuckets) % t.numBuckets
	b := t.buckets[idx]
	if b.Count == 0 {
		return 0, 0, 0
	}
	return time.Duration(b.Sum / b.Count), time.Duration(b.Max), b.Count
}

// RecentStats returns aggregate stats for the last N buckets (including current).
func (t *TimeBucketedWait) RecentStats(n int) (avg time.Duration, max time.Duration, count int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n <= 0 {
		n = 1
	}
	if n > t.numBuckets {
		n = t.numBuckets
	}
	var totalSum, totalMax int64
	var totalCount int64
	for i := 0; i < n; i++ {
		idx := (t.curIdx - i + t.numBuckets) % t.numBuckets
		b := t.buckets[idx]
		totalSum += b.Sum
		totalCount += b.Count
		if b.Max > totalMax {
			totalMax = b.Max
		}
	}
	if totalCount == 0 {
		return 0, 0, 0
	}
	return time.Duration(totalSum / totalCount), time.Duration(totalMax), totalCount
}

// RollingWindow maintains a time-windowed collection of samples for metric calculation.
// It is thread-safe and automatically prunes samples outside the window duration.
type RollingWindow struct {
	samples    []windowSample
	duration   time.Duration
	maxSamples int
	mu         sync.RWMutex
}

type windowSample struct {
	value     float64
	timestamp time.Time
}

// NewRollingWindow creates a new rolling window with the specified duration.
func NewRollingWindow(duration time.Duration, maxSamples int) *RollingWindow {
	if maxSamples <= 0 {
		maxSamples = 1000
	}
	return &RollingWindow{
		samples:    make([]windowSample, 0, maxSamples),
		duration:   duration,
		maxSamples: maxSamples,
	}
}

// Add adds a new sample to the window.
func (w *RollingWindow) Add(value float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	w.pruneOldSamples(now)

	// If at capacity, remove oldest
	if len(w.samples) >= w.maxSamples {
		w.samples = w.samples[1:]
	}

	w.samples = append(w.samples, windowSample{
		value:     value,
		timestamp: now,
	})
}

// pruneOldSamples removes samples outside the window. Must be called with lock held.
func (w *RollingWindow) pruneOldSamples(now time.Time) {
	cutoff := now.Add(-w.duration)
	idx := 0
	for i, s := range w.samples {
		if s.timestamp.After(cutoff) {
			idx = i
			break
		}
		idx = i + 1
	}
	if idx > 0 && idx <= len(w.samples) {
		w.samples = w.samples[idx:]
	}
}

// Average returns the average of all samples in the window.
func (w *RollingWindow) Average() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.samples) == 0 {
		return 0
	}

	now := time.Now()
	cutoff := now.Add(-w.duration)

	sum := 0.0
	count := 0
	for _, s := range w.samples {
		if s.timestamp.After(cutoff) {
			sum += s.value
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// Count returns the number of samples in the window.
func (w *RollingWindow) Count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-w.duration)

	count := 0
	for _, s := range w.samples {
		if s.timestamp.After(cutoff) {
			count++
		}
	}
	return count
}

// Sum returns the sum of all samples in the window.
func (w *RollingWindow) Sum() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-w.duration)

	sum := 0.0
	for _, s := range w.samples {
		if s.timestamp.After(cutoff) {
			sum += s.value
		}
	}
	return sum
}

// StdDev returns the standard deviation of samples in the window.
func (w *RollingWindow) StdDev() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-w.duration)

	// Collect valid samples
	var values []float64
	for _, s := range w.samples {
		if s.timestamp.After(cutoff) {
			values = append(values, s.value)
		}
	}

	if len(values) < 2 {
		return 0
	}

	// Calculate mean
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))

	// Calculate variance
	variance := 0.0
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	variance /= float64(len(values))

	return math.Sqrt(variance)
}
