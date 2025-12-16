package queue

import (
	"math"
	"testing"
	"time"
)

func TestRollingWindowAdd(t *testing.T) {
	w := NewRollingWindow(100*time.Millisecond, 100)

	// Add samples
	w.Add(1.0)
	w.Add(2.0)
	w.Add(3.0)

	if w.Count() != 3 {
		t.Errorf("expected count 3, got %d", w.Count())
	}

	avg := w.Average()
	if avg != 2.0 {
		t.Errorf("expected average 2.0, got %f", avg)
	}

	sum := w.Sum()
	if sum != 6.0 {
		t.Errorf("expected sum 6.0, got %f", sum)
	}
}

func TestRollingWindowExpiry(t *testing.T) {
	w := NewRollingWindow(50*time.Millisecond, 100)

	// Add samples
	w.Add(10.0)
	w.Add(20.0)

	// Verify samples are present
	if w.Count() != 2 {
		t.Errorf("expected count 2, got %d", w.Count())
	}

	// Wait for samples to expire
	time.Sleep(60 * time.Millisecond)

	// Add new sample to trigger prune
	w.Add(30.0)

	if w.Count() != 1 {
		t.Errorf("expected count 1 after expiry, got %d", w.Count())
	}

	avg := w.Average()
	if avg != 30.0 {
		t.Errorf("expected average 30.0 after expiry, got %f", avg)
	}
}

func TestRollingWindowStdDev(t *testing.T) {
	w := NewRollingWindow(1*time.Second, 100)

	// Add values with known variance: 1, 2, 3, 4, 5
	// Mean = 3, Variance = 2, StdDev = sqrt(2) ≈ 1.414
	w.Add(1.0)
	w.Add(2.0)
	w.Add(3.0)
	w.Add(4.0)
	w.Add(5.0)

	stddev := w.StdDev()
	expected := math.Sqrt(2.0)
	if math.Abs(stddev-expected) > 0.001 {
		t.Errorf("expected stddev %f, got %f", expected, stddev)
	}
}

func TestRollingWindowMaxSamples(t *testing.T) {
	w := NewRollingWindow(10*time.Second, 5)

	// Add more samples than maxSamples
	for i := 0; i < 10; i++ {
		w.Add(float64(i))
	}

	// Should only have 5 samples (most recent)
	if w.Count() > 5 {
		t.Errorf("expected count <= 5, got %d", w.Count())
	}
}

func TestScalingMetricsRecord(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	cfg.ShortWindowDuration = 100 * time.Millisecond
	m := NewScalingMetrics(cfg)

	stats := Stats{
		EnqueueRate: 100.0,
		DequeueRate: 95.0,
		QueueDepth:  50,
	}

	m.Record(stats)
	m.Record(stats)
	m.Record(stats)

	short := m.GetShortWindowMetrics()
	if short.AvgEnqueueRate != 100.0 {
		t.Errorf("expected avg enqueue rate 100.0, got %f", short.AvgEnqueueRate)
	}
	if short.AvgQueueDepth != 50.0 {
		t.Errorf("expected avg queue depth 50.0, got %f", short.AvgQueueDepth)
	}
}

func TestScalingMetricsRecordUtilization(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	m := NewScalingMetrics(cfg)

	// 80% utilization
	m.RecordUtilization(80, 100)
	m.RecordUtilization(80, 100)

	short := m.GetShortWindowMetrics()
	if short.AvgUtilization != 0.8 {
		t.Errorf("expected avg utilization 0.8, got %f", short.AvgUtilization)
	}
}

func TestScalingMetricsVariabilityCoefficients(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	m := NewScalingMetrics(cfg)

	// Default should be 1.0, 1.0 (exponential assumption)
	ca, cs := m.GetVariabilityCoefficients()
	if ca != 1.0 || cs != 1.0 {
		t.Errorf("expected default CV (1.0, 1.0), got (%f, %f)", ca, cs)
	}

	// Add some arrival times with regular intervals (10ms apart)
	// Note: Perfectly regular arrivals have CV=0, which defaults to 1.0
	base := time.Now()
	for i := 0; i < 10; i++ {
		m.RecordArrival(base.Add(time.Duration(i*10) * time.Millisecond))
	}

	// Add some service times with varying durations to get a measurable CV
	// Use a mix of fast and slow service times
	serviceTimes := []time.Duration{
		10 * time.Millisecond,
		30 * time.Millisecond,
		15 * time.Millisecond,
		25 * time.Millisecond,
		20 * time.Millisecond,
		35 * time.Millisecond,
		12 * time.Millisecond,
		28 * time.Millisecond,
		18 * time.Millisecond,
		22 * time.Millisecond,
	}
	for _, st := range serviceTimes {
		m.RecordServiceTime(st)
	}

	ca, cs = m.GetVariabilityCoefficients()
	// With perfectly regular arrivals, CV=0 which defaults to 1.0 (M/M/c exponential assumption)
	// This is intentional - we never deflate below exponential assumption for safety
	if ca != 1.0 {
		// Zero variance defaults to 1.0
		t.Errorf("expected CV 1.0 for regular intervals (conservative default), got %f", ca)
	}
	// With varying service times, we should get a non-zero CV
	// The test values have stddev ~8.2ms, mean ~21.5ms, CV ~0.38
	if cs < 0.2 || cs > 0.6 {
		t.Errorf("expected service CV between 0.2 and 0.6 for varied service times, got %f", cs)
	}
}

func TestScalingMetricsVariabilityWithVariance(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	m := NewScalingMetrics(cfg)

	// Add arrivals with varying intervals (high variance)
	base := time.Now()
	intervals := []int{5, 50, 10, 100, 15, 80, 20} // Highly variable intervals
	offset := 0
	for _, interval := range intervals {
		offset += interval
		m.RecordArrival(base.Add(time.Duration(offset) * time.Millisecond))
	}

	// Add service times with varying durations
	serviceTimes := []time.Duration{
		10 * time.Millisecond,
		50 * time.Millisecond,
		15 * time.Millisecond,
		80 * time.Millisecond,
		20 * time.Millisecond,
		60 * time.Millisecond,
	}
	for _, st := range serviceTimes {
		m.RecordServiceTime(st)
	}

	ca, cs := m.GetVariabilityCoefficients()
	// With high variance, CVs should be > 0.5
	if ca < 0.5 {
		t.Errorf("expected high arrival CV for variable intervals, got %f", ca)
	}
	if cs < 0.5 {
		t.Errorf("expected high service CV for variable times, got %f", cs)
	}
}

func TestScalingMetricsMultiWindow(t *testing.T) {
	cfg := ScalingMetricsConfig{
		ShortWindowDuration:  50 * time.Millisecond,
		MediumWindowDuration: 100 * time.Millisecond,
		LongWindowDuration:   200 * time.Millisecond,
		MaxSamplesPerWindow:  100,
		MaxVarianceSamples:   100,
	}
	m := NewScalingMetrics(cfg)

	stats := Stats{
		EnqueueRate: 100.0,
		QueueDepth:  10,
	}
	m.Record(stats)

	// All windows should have the data
	short := m.GetShortWindowMetrics()
	medium := m.GetMediumWindowMetrics()
	long := m.GetLongWindowMetrics()

	if short.SampleCount != 1 || medium.SampleCount != 1 || long.SampleCount != 1 {
		t.Errorf("expected 1 sample in all windows, got short=%d, medium=%d, long=%d",
			short.SampleCount, medium.SampleCount, long.SampleCount)
	}

	// Wait for short window to expire
	time.Sleep(60 * time.Millisecond)

	// Record again to trigger prune
	stats.EnqueueRate = 50.0
	m.Record(stats)

	short = m.GetShortWindowMetrics()
	medium = m.GetMediumWindowMetrics()
	long = m.GetLongWindowMetrics()

	// Short window should only have newer sample
	if short.AvgEnqueueRate != 50.0 {
		t.Errorf("expected short window avg 50.0, got %f", short.AvgEnqueueRate)
	}

	// Medium and long windows should have both samples (avg = 75)
	if medium.AvgEnqueueRate != 75.0 {
		t.Errorf("expected medium window avg 75.0, got %f", medium.AvgEnqueueRate)
	}
}

func TestScalingMetricsHasSufficientData(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	m := NewScalingMetrics(cfg)

	// Initially should not have sufficient data
	if m.HasSufficientData() {
		t.Error("expected insufficient data initially")
	}

	// Add some samples
	stats := Stats{EnqueueRate: 100.0, QueueDepth: 10}
	m.Record(stats)
	m.Record(stats)

	// Still not enough
	if m.HasSufficientData() {
		t.Error("expected insufficient data with 2 samples")
	}

	// Add third sample
	m.Record(stats)

	// Now should have sufficient data
	if !m.HasSufficientData() {
		t.Error("expected sufficient data with 3 samples")
	}
}

func TestScalingMetricsGetAverageServiceTime(t *testing.T) {
	cfg := DefaultScalingMetricsConfig()
	m := NewScalingMetrics(cfg)

	// Initially should return 0 (insufficient data)
	avg := m.GetAverageServiceTime()
	if avg != 0 {
		t.Errorf("expected 0 with no data, got %f", avg)
	}

	// Add 2 samples - still not enough
	m.RecordServiceTime(10 * time.Millisecond)
	m.RecordServiceTime(20 * time.Millisecond)
	avg = m.GetAverageServiceTime()
	if avg != 0 {
		t.Errorf("expected 0 with 2 samples, got %f", avg)
	}

	// Add 3rd sample - now should work
	m.RecordServiceTime(30 * time.Millisecond)
	avg = m.GetAverageServiceTime()
	// Average of 10, 20, 30 ms = 20ms = 0.02s
	expected := 0.02
	if math.Abs(avg-expected) > 0.001 {
		t.Errorf("expected avg %f, got %f", expected, avg)
	}

	// Verify sample count
	count := m.GetServiceTimeSampleCount()
	if count != 3 {
		t.Errorf("expected 3 samples, got %d", count)
	}
}

func TestCoefficientOfVariation(t *testing.T) {
	tests := []struct {
		name     string
		values   []float64
		expected float64
	}{
		{
			name:     "constant values",
			values:   []float64{10, 10, 10, 10},
			expected: 0.0,
		},
		{
			name:     "standard normal-ish",
			values:   []float64{1, 2, 3, 4, 5},
			expected: math.Sqrt(2.0) / 3.0, // stddev/mean = sqrt(2)/3
		},
		{
			name:     "insufficient data",
			values:   []float64{1},
			expected: 0.0,
		},
		{
			name:     "empty",
			values:   []float64{},
			expected: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cv := coefficientOfVariation(tc.values)
			if math.Abs(cv-tc.expected) > 0.001 {
				t.Errorf("expected CV %f, got %f", tc.expected, cv)
			}
		})
	}
}

func TestFindCForSLO(t *testing.T) {
	tests := []struct {
		name    string
		lambda  float64
		tau     float64
		wTarget float64
		ca, cs  float64
		wantErr bool
		minC    int
		maxC    int
	}{
		{
			name:    "basic M/M/c",
			lambda:  100.0, // 100 jobs/sec
			tau:     0.02,  // 20ms service time
			wTarget: 0.1,   // 100ms target latency
			ca:      0,     // Use M/M/c (exponential)
			cs:      0,
			wantErr: false,
			minC:    3,  // Should need at least 3 workers
			maxC:    10, // Should not need more than 10
		},
		{
			name:    "high variability (Allen-Cunneen)",
			lambda:  100.0,
			tau:     0.02,
			wTarget: 0.1,
			ca:      2.0, // High arrival variability
			cs:      2.0, // High service variability
			wantErr: false,
			minC:    3,
			maxC:    20, // Higher variability needs more workers
		},
		{
			name:    "low load",
			lambda:  10.0, // 10 jobs/sec
			tau:     0.01, // 10ms service time
			wTarget: 0.05, // 50ms target
			wantErr: false,
			minC:    1,
			maxC:    5,
		},
		{
			name:    "invalid tau",
			lambda:  100.0,
			tau:     0,
			wTarget: 0.1,
			wantErr: true,
		},
		{
			name:    "invalid wTarget",
			lambda:  100.0,
			tau:     0.02,
			wTarget: 0,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, w, err := FindCForSLO(tc.lambda, tc.tau, tc.wTarget, tc.ca, tc.cs, 1000)

			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got c=%d, w=%f", c, w)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if c < tc.minC {
				t.Errorf("c=%d is below minimum expected %d", c, tc.minC)
			}
			if c > tc.maxC {
				t.Errorf("c=%d is above maximum expected %d", c, tc.maxC)
			}

			// Verify W meets SLO
			if w > tc.wTarget {
				t.Errorf("W=%f exceeds target %f", w, tc.wTarget)
			}
		})
	}
}

func TestMmcWait(t *testing.T) {
	tests := []struct {
		name    string
		lambda  float64
		mu      float64
		c       int
		wantErr bool
	}{
		{
			name:    "stable system",
			lambda:  10.0,
			mu:      5.0,
			c:       3,
			wantErr: false,
		},
		{
			name:    "unstable system (overloaded)",
			lambda:  10.0,
			mu:      5.0,
			c:       2, // rho = 10/(2*5) = 1.0
			wantErr: true,
		},
		{
			name:    "invalid c",
			lambda:  10.0,
			mu:      5.0,
			c:       0,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wq, w, err := MmcWait(tc.lambda, tc.mu, tc.c, 0, 0)

			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got wq=%f, w=%f", wq, w)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Basic sanity checks
			if wq < 0 {
				t.Errorf("wq should be non-negative, got %f", wq)
			}
			if w < 1/tc.mu {
				t.Errorf("w should be at least service time (1/mu=%f), got %f", 1/tc.mu, w)
			}
		})
	}
}

func TestAllenCunneenInflation(t *testing.T) {
	lambda := 100.0
	mu := 50.0
	c := 3

	// Base M/M/c (exponential)
	_, wBase, err := MmcWait(lambda, mu, c, 0, 0)
	if err != nil {
		t.Fatalf("base M/M/c failed: %v", err)
	}

	// With high variability (Ca=2, Cs=2)
	// Allen-Cunneen inflation = (Ca² + Cs²)/2 = (4+4)/2 = 4
	_, wHigh, err := MmcWait(lambda, mu, c, 2.0, 2.0)
	if err != nil {
		t.Fatalf("Allen-Cunneen M/M/c failed: %v", err)
	}

	// High variability should increase wait time
	if wHigh <= wBase {
		t.Errorf("expected wHigh > wBase, got wHigh=%f, wBase=%f", wHigh, wBase)
	}

	// With low variability (Ca=0.5, Cs=0.5)
	// Allen-Cunneen inflation = (0.25+0.25)/2 = 0.25, but clamped to 1.0
	_, wLow, err := MmcWait(lambda, mu, c, 0.5, 0.5)
	if err != nil {
		t.Fatalf("low variability M/M/c failed: %v", err)
	}

	// Low variability should not be better than base (conservative clamping)
	if wLow < wBase {
		t.Errorf("expected wLow >= wBase (clamped), got wLow=%f, wBase=%f", wLow, wBase)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		value, min, max, want int
	}{
		{5, 1, 10, 5},
		{0, 1, 10, 1},
		{15, 1, 10, 10},
		{-5, 0, 100, 0},
	}

	for _, tc := range tests {
		got := clamp(tc.value, tc.min, tc.max)
		if got != tc.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tc.value, tc.min, tc.max, got, tc.want)
		}
	}
}
