package queue

import (
	"math"
	"sync"
	"time"
)

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

// ScalingMetrics collects multi-window metrics for intelligent worker scaling.
// It tracks arrival patterns, service times, and calculates variability coefficients
// for use with M/M/c queueing theory and Allen-Cunneen approximations.
type ScalingMetrics struct {
	// Multi-window metrics for different time horizons
	ShortWindow  *RollingWindow // 15s - spike detection, triggers scale-up
	MediumWindow *RollingWindow // 5m  - trend detection
	LongWindow   *RollingWindow // 30m - baseline, used for scale-down decisions

	// Separate windows for different metrics
	enqueueRateShort  *RollingWindow
	enqueueRateMedium *RollingWindow
	enqueueRateLong   *RollingWindow

	queueDepthShort  *RollingWindow
	queueDepthMedium *RollingWindow
	queueDepthLong   *RollingWindow

	utilizationShort  *RollingWindow
	utilizationMedium *RollingWindow
	utilizationLong   *RollingWindow

	// Variance tracking for Allen-Cunneen coefficients
	arrivalTimes []time.Time     // Timestamps of job arrivals
	serviceTimes []time.Duration // Measured service durations
	varianceMu   sync.RWMutex

	// Configuration
	maxVarianceSamples int
}

// ScalingMetricsConfig configures the ScalingMetrics collector.
type ScalingMetricsConfig struct {
	ShortWindowDuration  time.Duration // Default 15s
	MediumWindowDuration time.Duration // Default 5m
	LongWindowDuration   time.Duration // Default 30m
	MaxSamplesPerWindow  int           // Default 1000
	MaxVarianceSamples   int           // Default 1000
}

// DefaultScalingMetricsConfig returns sensible defaults.
func DefaultScalingMetricsConfig() ScalingMetricsConfig {
	return ScalingMetricsConfig{
		ShortWindowDuration:  15 * time.Second,
		MediumWindowDuration: 5 * time.Minute,
		LongWindowDuration:   30 * time.Minute,
		MaxSamplesPerWindow:  1000,
		MaxVarianceSamples:   1000,
	}
}

// NewScalingMetrics creates a new ScalingMetrics collector.
func NewScalingMetrics(cfg ScalingMetricsConfig) *ScalingMetrics {
	return &ScalingMetrics{
		ShortWindow:  NewRollingWindow(cfg.ShortWindowDuration, cfg.MaxSamplesPerWindow),
		MediumWindow: NewRollingWindow(cfg.MediumWindowDuration, cfg.MaxSamplesPerWindow),
		LongWindow:   NewRollingWindow(cfg.LongWindowDuration, cfg.MaxSamplesPerWindow),

		enqueueRateShort:  NewRollingWindow(cfg.ShortWindowDuration, cfg.MaxSamplesPerWindow),
		enqueueRateMedium: NewRollingWindow(cfg.MediumWindowDuration, cfg.MaxSamplesPerWindow),
		enqueueRateLong:   NewRollingWindow(cfg.LongWindowDuration, cfg.MaxSamplesPerWindow),

		queueDepthShort:  NewRollingWindow(cfg.ShortWindowDuration, cfg.MaxSamplesPerWindow),
		queueDepthMedium: NewRollingWindow(cfg.MediumWindowDuration, cfg.MaxSamplesPerWindow),
		queueDepthLong:   NewRollingWindow(cfg.LongWindowDuration, cfg.MaxSamplesPerWindow),

		utilizationShort:  NewRollingWindow(cfg.ShortWindowDuration, cfg.MaxSamplesPerWindow),
		utilizationMedium: NewRollingWindow(cfg.MediumWindowDuration, cfg.MaxSamplesPerWindow),
		utilizationLong:   NewRollingWindow(cfg.LongWindowDuration, cfg.MaxSamplesPerWindow),

		arrivalTimes:       make([]time.Time, 0, cfg.MaxVarianceSamples),
		serviceTimes:       make([]time.Duration, 0, cfg.MaxVarianceSamples),
		maxVarianceSamples: cfg.MaxVarianceSamples,
	}
}

// Record records a snapshot of queue statistics to all windows.
func (m *ScalingMetrics) Record(stats Stats) {
	// Record enqueue rate
	m.enqueueRateShort.Add(stats.EnqueueRate)
	m.enqueueRateMedium.Add(stats.EnqueueRate)
	m.enqueueRateLong.Add(stats.EnqueueRate)

	// Record queue depth
	m.queueDepthShort.Add(float64(stats.QueueDepth))
	m.queueDepthMedium.Add(float64(stats.QueueDepth))
	m.queueDepthLong.Add(float64(stats.QueueDepth))
}

// RecordUtilization records worker utilization (running/capacity).
func (m *ScalingMetrics) RecordUtilization(running, capacity int) {
	if capacity <= 0 {
		return
	}
	util := float64(running) / float64(capacity)
	m.utilizationShort.Add(util)
	m.utilizationMedium.Add(util)
	m.utilizationLong.Add(util)
}

// RecordArrival records a job arrival time for variance calculation.
func (m *ScalingMetrics) RecordArrival(t time.Time) {
	m.varianceMu.Lock()
	defer m.varianceMu.Unlock()

	m.arrivalTimes = append(m.arrivalTimes, t)
	// Keep only recent samples
	if len(m.arrivalTimes) > m.maxVarianceSamples {
		m.arrivalTimes = m.arrivalTimes[1:]
	}
}

// RecordServiceTime records a job service duration for variance calculation.
func (m *ScalingMetrics) RecordServiceTime(d time.Duration) {
	m.varianceMu.Lock()
	defer m.varianceMu.Unlock()

	m.serviceTimes = append(m.serviceTimes, d)
	// Keep only recent samples
	if len(m.serviceTimes) > m.maxVarianceSamples {
		m.serviceTimes = m.serviceTimes[1:]
	}
}

// GetAverageServiceTime returns the average measured service time in seconds.
// Returns 0 if insufficient data (fewer than 3 samples).
func (m *ScalingMetrics) GetAverageServiceTime() float64 {
	m.varianceMu.RLock()
	defer m.varianceMu.RUnlock()

	if len(m.serviceTimes) < 3 {
		return 0
	}

	sum := 0.0
	for _, d := range m.serviceTimes {
		sum += d.Seconds()
	}
	return sum / float64(len(m.serviceTimes))
}

// GetServiceTimeSampleCount returns the number of service time samples collected.
func (m *ScalingMetrics) GetServiceTimeSampleCount() int {
	m.varianceMu.RLock()
	defer m.varianceMu.RUnlock()
	return len(m.serviceTimes)
}

// GetVariabilityCoefficients calculates the coefficient of variation for
// inter-arrival times (Ca) and service times (Cs) for Allen-Cunneen approximation.
//
// Ca = σ_arrival / μ_arrival  (stddev / mean of inter-arrival times)
// Cs = σ_service / μ_service  (stddev / mean of service times)
//
// Returns (1.0, 1.0) if insufficient data (defaults to M/M/c exponential assumption).
func (m *ScalingMetrics) GetVariabilityCoefficients() (ca, cs float64) {
	m.varianceMu.RLock()
	defer m.varianceMu.RUnlock()

	// Calculate Ca from inter-arrival times
	ca = m.calculateArrivalCV()

	// Calculate Cs from service times
	cs = m.calculateServiceCV()

	// Default to 1.0 (exponential) if we couldn't calculate
	if ca <= 0 {
		ca = 1.0
	}
	if cs <= 0 {
		cs = 1.0
	}

	return ca, cs
}

// calculateArrivalCV calculates the coefficient of variation for inter-arrival times.
// Must be called with varianceMu held.
func (m *ScalingMetrics) calculateArrivalCV() float64 {
	if len(m.arrivalTimes) < 3 {
		return 0 // Insufficient data
	}

	// Calculate inter-arrival times
	interarrivals := make([]float64, 0, len(m.arrivalTimes)-1)
	for i := 1; i < len(m.arrivalTimes); i++ {
		delta := m.arrivalTimes[i].Sub(m.arrivalTimes[i-1]).Seconds()
		if delta > 0 {
			interarrivals = append(interarrivals, delta)
		}
	}

	if len(interarrivals) < 2 {
		return 0
	}

	return coefficientOfVariation(interarrivals)
}

// calculateServiceCV calculates the coefficient of variation for service times.
// Must be called with varianceMu held.
func (m *ScalingMetrics) calculateServiceCV() float64 {
	if len(m.serviceTimes) < 3 {
		return 0 // Insufficient data
	}

	// Convert to float64 seconds
	services := make([]float64, len(m.serviceTimes))
	for i, d := range m.serviceTimes {
		services[i] = d.Seconds()
	}

	return coefficientOfVariation(services)
}

// coefficientOfVariation calculates CV = stddev / mean.
func coefficientOfVariation(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}

	// Calculate mean
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))

	if mean <= 0 {
		return 0
	}

	// Calculate variance
	variance := 0.0
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	variance /= float64(len(values))

	stddev := math.Sqrt(variance)
	return stddev / mean
}

// WindowMetrics provides a summary of metrics from a specific time window.
type WindowMetrics struct {
	AvgEnqueueRate float64
	AvgQueueDepth  float64
	AvgUtilization float64
	SampleCount    int
}

// GetShortWindowMetrics returns metrics from the short (15s) window.
func (m *ScalingMetrics) GetShortWindowMetrics() WindowMetrics {
	return WindowMetrics{
		AvgEnqueueRate: m.enqueueRateShort.Average(),
		AvgQueueDepth:  m.queueDepthShort.Average(),
		AvgUtilization: m.utilizationShort.Average(),
		SampleCount:    m.enqueueRateShort.Count(),
	}
}

// GetMediumWindowMetrics returns metrics from the medium (5m) window.
func (m *ScalingMetrics) GetMediumWindowMetrics() WindowMetrics {
	return WindowMetrics{
		AvgEnqueueRate: m.enqueueRateMedium.Average(),
		AvgQueueDepth:  m.queueDepthMedium.Average(),
		AvgUtilization: m.utilizationMedium.Average(),
		SampleCount:    m.enqueueRateMedium.Count(),
	}
}

// GetLongWindowMetrics returns metrics from the long (30m) window.
func (m *ScalingMetrics) GetLongWindowMetrics() WindowMetrics {
	return WindowMetrics{
		AvgEnqueueRate: m.enqueueRateLong.Average(),
		AvgQueueDepth:  m.queueDepthLong.Average(),
		AvgUtilization: m.utilizationLong.Average(),
		SampleCount:    m.enqueueRateLong.Count(),
	}
}

// HasSufficientData returns true if we have enough samples for reliable scaling decisions.
func (m *ScalingMetrics) HasSufficientData() bool {
	// Need at least 3 samples in short window for any decision
	return m.enqueueRateShort.Count() >= 3
}

// ShouldScaleUp returns true if short-window metrics indicate scale-up is needed.
// This reacts quickly to spikes.
func (m *ScalingMetrics) ShouldScaleUp(currentWorkers int, targetLatency time.Duration) bool {
	short := m.GetShortWindowMetrics()

	// Trigger scale-up if:
	// 1. Queue depth is growing (> 1.5x workers)
	if short.AvgQueueDepth > float64(currentWorkers)*1.5 {
		return true
	}

	// 2. Utilization is very high
	if short.AvgUtilization > 0.85 {
		return true
	}

	return false
}

// ShouldScaleDown returns true if long-window metrics indicate scale-down is safe.
// This is conservative and only triggers after sustained low utilization.
func (m *ScalingMetrics) ShouldScaleDown(currentWorkers, minWorkers int) bool {
	long := m.GetLongWindowMetrics()

	// Only scale down if:
	// 1. We have enough samples (long window is meaningful)
	if long.SampleCount < 10 {
		return false
	}

	// 2. Sustained low utilization
	if long.AvgUtilization > 0.25 {
		return false
	}

	// 3. Queue is consistently empty or near-empty
	if long.AvgQueueDepth > float64(minWorkers) {
		return false
	}

	return true
}
