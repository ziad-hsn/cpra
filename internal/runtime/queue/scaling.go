package queue

import (
	"math"
	"sync"
	"time"
)

// scaling.go houses the M/M/c scaling logic and metrics collection for the DynamicWorkerPool.

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

// Tune adjusts the worker pool capacity to the specified number of workers.
// This is used for initial pre-sizing based on M/M/c calculations.
// The capacity is clamped between MinWorkers and MaxWorkers.
func (p *DynamicWorkerPool) Tune(capacity int) {
	if capacity < p.config.MinWorkers {
		capacity = p.config.MinWorkers
	}
	if capacity > p.config.MaxWorkers {
		capacity = p.config.MaxWorkers
	}
	current := p.antsPool.Cap()
	if capacity != current {
		p.antsPool.Tune(capacity)
		p.lastTarget.Store(int64(capacity))
		p.lastScaleTime.Store(time.Now().UnixNano())
		p.scalingEvents.Add(1)
		if p.logger != nil {
			p.logger.Printf("Pre-sized worker pool from %d to %d workers", current, capacity)
		}
	}
}

// autoScale periodically tunes the ants pool capacity using M/M/c queueing theory.
// Implements hysteresis and asymmetric cooldowns to prevent oscillation.
func (p *DynamicWorkerPool) autoScale() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.AdjustmentInterval)
	defer ticker.Stop()

	warmupExited := false

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			stats := p.queue.Stats()

			// Adaptive warmup: exit early if queue is drowning
			inWarmup := !warmupExited && time.Since(p.startTime) < p.config.WarmupDuration
			if inWarmup {
				// Critical threshold: queue depth > 50% of capacity or > 1000 items
				criticalDepth := stats.Capacity / 2
				if criticalDepth < 1000 {
					criticalDepth = 1000
				}
				if stats.QueueDepth > criticalDepth {
					warmupExited = true
					if p.logger != nil {
						p.logger.Printf("[WorkerPool] warmup exited early: queue depth %d > critical %d",
							stats.QueueDepth, criticalDepth)
					}
				} else {
					// Still in warmup, skip scaling
					continue
				}
			}

			// Record metrics to rolling windows
			p.metrics.Record(stats)
			p.metrics.RecordUtilization(p.antsPool.Running(), p.antsPool.Cap())

			desired := p.desiredCapacity(stats)
			current := p.antsPool.Cap()

			// No change needed
			if desired == current {
				continue
			}

			now := time.Now()

			if desired > current {
				// SCALE UP: Check hysteresis threshold
				ratio := float64(desired) / float64(current)
				if ratio < p.config.ScaleUpThreshold {
					// Change too small - skip to prevent oscillation
					continue
				}

				// Check cooldown period
				if now.Sub(p.lastScaleUpTime) < p.config.ScaleUpCooldown {
					continue
				}

				// Apply scale up
				p.antsPool.Tune(desired)
				p.lastScaleUpTime = now
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(now.UnixNano())
				p.scalingEvents.Add(1)

				if p.logger != nil {
					p.logger.Printf("Scaled UP worker pool: %d → %d (ratio=%.2f, queue=%d)",
						current, desired, ratio, stats.QueueDepth)
				}
			} else {
				// SCALE DOWN: More conservative - check hysteresis threshold
				ratio := float64(desired) / float64(current)
				if ratio > p.config.ScaleDownThreshold {
					// Change too small - skip to prevent oscillation
					continue
				}

				// Use long window to ensure sustained low utilization
				longMetrics := p.metrics.GetLongWindowMetrics()
				if longMetrics.SampleCount < 10 {
					// Not enough history - don't scale down yet
					continue
				}
				if longMetrics.AvgUtilization > 0.25 {
					// Still moderately utilized over long term - don't scale down
					continue
				}
				if longMetrics.AvgQueueDepth > float64(p.config.MinWorkers) {
					// Queue not consistently empty - don't scale down
					continue
				}

				// Check longer cooldown period for scale down
				if now.Sub(p.lastScaleDownTime) < p.config.ScaleDownCooldown {
					continue
				}

				// Apply scale down
				p.antsPool.Tune(desired)
				p.lastScaleDownTime = now
				p.lastTarget.Store(int64(desired))
				p.lastScaleTime.Store(now.UnixNano())
				p.scalingEvents.Add(1)

				if p.logger != nil {
					p.logger.Printf("Scaled DOWN worker pool: %d → %d (ratio=%.2f, longUtil=%.2f)",
						current, desired, ratio, longMetrics.AvgUtilization)
				}
			}
		}
	}
}

func (p *DynamicWorkerPool) desiredCapacity(stats Stats) int {
	current := p.antsPool.Cap()
	if current <= 0 {
		current = p.config.MinWorkers
	}

	minWorkers := p.config.MinWorkers
	maxWorkers := p.config.MaxWorkers
	if maxWorkers < minWorkers {
		maxWorkers = minWorkers
	}

	// 1. Get observed arrival rate from short window (reacts to spikes)
	lambda := p.metrics.GetShortWindowMetrics().AvgEnqueueRate
	if lambda <= 0 {
		lambda = stats.EnqueueRate
	}
	if lambda <= 0 {
		// No load - return minimum
		return minWorkers
	}

	// 2. Estimate service time (τ) from observed throughput
	tau := p.estimateServiceTime(stats, current)
	if tau <= 0 {
		// No throughput data yet - use heuristic from Little's Law fallback
		return p.littleLawFallback(lambda, stats, current)
	}

	// 3. Get variability coefficients for Allen-Cunneen
	ca, cs := p.metrics.GetVariabilityCoefficients()
	// Defaults already set to 1.0 by GetVariabilityCoefficients() if insufficient data

	// 4. Calculate optimal worker count using M/M/c + Allen-Cunneen
	// TargetQueueLatency is the target for QUEUE wait time (Wq), not total time (W).
	// Total time W = Wq + tau, so wTarget = TargetQueueLatency + tau.
	// This prevents impossible SLOs when tau > TargetQueueLatency.
	wqTarget := p.config.TargetQueueLatency.Seconds()
	wTarget := wqTarget + tau // Total latency target = queue wait + service time
	cTheory, _, err := FindCForSLO(lambda, tau, wTarget, ca, cs, maxWorkers)

	// 5. Fallback if M/M/c calculation fails (e.g., unstable system)
	if err != nil {
		if p.logger != nil {
			p.logger.Printf("M/M/c calculation failed (λ=%.2f, τ=%.3fs): %v; using Little's Law fallback",
				lambda, tau, err)
		}
		return p.littleLawFallback(lambda, stats, current)
	}

	// 6. Add 15% headroom for safety margin and clamp
	desired := int(math.Ceil(float64(cTheory) * 1.15))
	return max(min(desired, maxWorkers), minWorkers)
}

// estimateServiceTime estimates the average service time per job from observed metrics.
// It prefers actual measured service times when available, falling back to throughput-based
// estimation only when insufficient measurement data exists.
func (p *DynamicWorkerPool) estimateServiceTime(stats Stats, currentWorkers int) float64 {
	// Prefer actual measured service times (recorded in workerFunc)
	measuredTau := p.metrics.GetAverageServiceTime()
	if measuredTau > 0 {
		// Sanity bounds: tau should be between 1ms and 30s
		if measuredTau < 0.001 {
			measuredTau = 0.001
		}
		if measuredTau > 30.0 {
			measuredTau = 30.0
		}
		return measuredTau
	}

	// Fallback: estimate from throughput if no measured data yet
	if currentWorkers <= 0 || stats.DequeueRate <= 0 {
		return 0
	}

	running := p.antsPool.Running()
	if running <= 0 {
		running = 1
	}

	// Service time = active workers / throughput
	// This is only used during initial warmup before we have measurements
	tau := float64(running) / stats.DequeueRate

	// Conservative bounds for fallback estimation
	if tau < 0.001 {
		tau = 0.001
	}
	if tau > 1.0 {
		// Cap fallback at 1s - if jobs really take longer, measurements will show it
		tau = 1.0
	}
	return tau
}

// littleLawFallback uses Little's Law (L = λW) as a fallback scaling heuristic.
func (p *DynamicWorkerPool) littleLawFallback(lambda float64, stats Stats, current int) int {
	minWorkers := p.config.MinWorkers
	maxWorkers := p.config.MaxWorkers
	targetLatency := p.config.TargetQueueLatency
	if targetLatency <= 0 {
		targetLatency = 100 * time.Millisecond
	}

	desired := current

	// Estimate per-worker throughput
	perWorker := 0.0
	if current > 0 && stats.DequeueRate > 0 {
		perWorker = stats.DequeueRate / float64(current)
	}
	if perWorker > 0 && lambda > 0 {
		desired = int(math.Ceil(lambda / perWorker))
	}

	// Enforce latency budget: target queue depth = λ * W_target
	targetDepth := lambda * targetLatency.Seconds()
	if targetDepth < float64(minWorkers) {
		targetDepth = float64(minWorkers)
	}

	depth := float64(stats.QueueDepth)
	if depth > targetDepth && targetDepth > 0 {
		// Queue building up - scale up proportionally
		scale := depth / targetDepth
		desired = int(math.Ceil(float64(desired) * scale))
	}

	return max(min(desired, maxWorkers), minWorkers)
}

// WorkerPoolStats contains runtime statistics for the worker pool.
type WorkerPoolStats struct {
	RunningWorkers  int
	IdleWorkers     int
	Capacity        int
	TasksCompleted  int64
	TasksSubmitted  int64
	MinWorkers      int
	MaxWorkers      int
	CurrentCapacity int
	WaitingTasks    int
	TargetWorkers   int
	ScalingEvents   int64
	LastScaleTime   time.Time
	PendingResults  int
}
