package queue

import (
	"math"
	"testing"
)

func TestGetQueueMetrics(t *testing.T) {
	tests := []struct {
		name      string
		lambda    float64
		mu        float64
		c         int
		wantErr   bool
		checkFunc func(t *testing.T, m QueueMetrics)
	}{
		{
			name:    "stable system with low load",
			lambda:  6.0, // 6 jobs/sec
			mu:      2.0, // each worker handles 2 jobs/sec (τ = 0.5s)
			c:       7,   // 7 workers
			wantErr: false,
			checkFunc: func(t *testing.T, m QueueMetrics) {
				if !m.Stable {
					t.Error("expected stable system")
				}
				// ρ = 6 / (7 * 2) = 0.4286
				if m.Utilization < 0.42 || m.Utilization > 0.44 {
					t.Errorf("expected utilization ~0.43, got %.4f", m.Utilization)
				}
				// Queue probability should be relatively low
				if m.QueueProb > 0.1 {
					t.Errorf("expected low queue probability, got %.4f", m.QueueProb)
				}
			},
		},
		{
			name:    "idle system",
			lambda:  0,
			mu:      2.0,
			c:       5,
			wantErr: false,
			checkFunc: func(t *testing.T, m QueueMetrics) {
				if !m.Stable {
					t.Error("expected stable system")
				}
				if m.Utilization != 0 {
					t.Errorf("expected zero utilization, got %.4f", m.Utilization)
				}
				if m.IdleProb != 1.0 {
					t.Errorf("expected idle prob = 1, got %.4f", m.IdleProb)
				}
			},
		},
		{
			name:    "high load system",
			lambda:  9.0,
			mu:      2.0,
			c:       5, // ρ = 9 / (5 * 2) = 0.9
			wantErr: false,
			checkFunc: func(t *testing.T, m QueueMetrics) {
				if !m.Stable {
					t.Error("expected stable system (ρ=0.9 < 1)")
				}
				if m.Utilization < 0.89 || m.Utilization > 0.91 {
					t.Errorf("expected utilization ~0.9, got %.4f", m.Utilization)
				}
				// Queue probability should be high
				if m.QueueProb < 0.3 {
					t.Errorf("expected high queue probability, got %.4f", m.QueueProb)
				}
				// Average wait if queued should be meaningful
				if m.AvgWaitGiven <= 0 {
					t.Error("expected positive conditional wait time")
				}
			},
		},
		{
			name:    "unstable system",
			lambda:  12.0,
			mu:      2.0,
			c:       5, // ρ = 12 / (5 * 2) = 1.2 >= 1
			wantErr: true,
			checkFunc: func(t *testing.T, m QueueMetrics) {
				if m.Stable {
					t.Error("expected unstable system")
				}
				if m.Utilization < 1.0 {
					t.Errorf("expected utilization >= 1, got %.4f", m.Utilization)
				}
			},
		},
		{
			name:    "invalid workers",
			lambda:  5.0,
			mu:      2.0,
			c:       0,
			wantErr: true,
		},
		{
			name:    "invalid mu",
			lambda:  5.0,
			mu:      0,
			c:       5,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := GetQueueMetrics(tt.lambda, tt.mu, tt.c)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetQueueMetrics() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.checkFunc != nil {
				tt.checkFunc(t, m)
			}
		})
	}
}

func TestIsStable(t *testing.T) {
	tests := []struct {
		name   string
		lambda float64
		tau    float64
		c      int
		want   bool
	}{
		{"stable with headroom", 5.0, 0.5, 7, true}, // λτ = 2.5 < 7
		{"barely stable", 5.0, 0.5, 3, true},        // λτ = 2.5 < 3
		{"unstable equal", 6.0, 0.5, 3, false},      // λτ = 3 >= 3
		{"unstable overload", 10.0, 0.5, 3, false},  // λτ = 5 > 3
		{"zero arrivals", 0, 0.5, 3, true},          // no arrivals is stable
		{"invalid workers", 5.0, 0.5, 0, false},     // no workers
		{"invalid tau", 5.0, 0, 3, false},           // zero service time
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStable(tt.lambda, tt.tau, tt.c); got != tt.want {
				t.Errorf("IsStable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMinWorkersForStability(t *testing.T) {
	tests := []struct {
		name   string
		lambda float64
		tau    float64
		want   int
	}{
		{"normal load", 5.0, 0.5, 4},   // ceil(2.5) + 1 = 4
		{"exact integer", 4.0, 0.5, 3}, // ceil(2) + 1 = 3
		{"high load", 100.0, 0.1, 11},  // ceil(10) + 1 = 11
		{"low load", 1.0, 0.1, 2},      // ceil(0.1) + 1 = 2
		{"zero arrivals", 0, 0.5, 1},   // minimum 1
		{"zero tau", 5.0, 0, 1},        // minimum 1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MinWorkersForStability(tt.lambda, tt.tau); got != tt.want {
				t.Errorf("MinWorkersForStability() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkersForUtilization(t *testing.T) {
	tests := []struct {
		name       string
		lambda     float64
		tau        float64
		targetUtil float64
		want       int
	}{
		{"80% util", 8.0, 0.5, 0.8, 5},         // ceil(4 / 0.8) = 5
		{"60% util", 6.0, 0.5, 0.6, 5},         // ceil(3 / 0.6) = 5
		{"50% util", 10.0, 0.5, 0.5, 10},       // ceil(5 / 0.5) = 10
		{"invalid util > 1", 8.0, 0.5, 1.5, 5}, // defaults to 0.8
		{"invalid util <= 0", 8.0, 0.5, 0, 5},  // defaults to 0.8
		{"zero arrivals", 0, 0.5, 0.8, 1},      // minimum 1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WorkersForUtilization(tt.lambda, tt.tau, tt.targetUtil); got != tt.want {
				t.Errorf("WorkersForUtilization() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUtilization(t *testing.T) {
	tests := []struct {
		name   string
		lambda float64
		tau    float64
		c      int
		want   float64
	}{
		{"normal", 6.0, 0.5, 7, 6.0 * 0.5 / 7.0}, // ≈ 0.4286
		{"high load", 9.0, 0.5, 5, 0.9},          // 9 * 0.5 / 5 = 0.9
		{"overloaded", 12.0, 0.5, 5, 1.2},        // >= 1 is unstable
		{"zero arrivals", 0, 0.5, 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Utilization(tt.lambda, tt.tau, tt.c)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("Utilization() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApproxWorkersForQueueProb(t *testing.T) {
	tests := []struct {
		name       string
		lambda     float64
		mu         float64
		targetProb float64
		minExpect  int // minimum expected workers
		maxExpect  int // maximum expected workers
	}{
		// Square root staffing is an approximation; the result should be
		// close to but not necessarily exactly matching binary search
		{"low queue prob 5%", 100.0, 2.0, 0.05, 51, 70}, // a=50, need c > 50
		{"medium queue prob 10%", 100.0, 2.0, 0.10, 51, 65},
		{"high queue prob 20%", 100.0, 2.0, 0.20, 51, 60},
		{"small system", 6.0, 2.0, 0.05, 4, 10}, // a=3
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApproxWorkersForQueueProb(tt.lambda, tt.mu, tt.targetProb)
			if got < tt.minExpect || got > tt.maxExpect {
				t.Errorf("ApproxWorkersForQueueProb() = %v, want between %v and %v",
					got, tt.minExpect, tt.maxExpect)
			}
		})
	}

	// Test edge cases
	t.Run("invalid params", func(t *testing.T) {
		// Should return minimum stable
		got := ApproxWorkersForQueueProb(10.0, 2.0, 0)
		minStable := int(math.Ceil(10.0/2.0)) + 1
		if got < minStable {
			t.Errorf("invalid params should return at least min stable workers, got %d, want >= %d",
				got, minStable)
		}
	})
}

func TestInverseNormalCDF(t *testing.T) {
	// Test known values
	tests := []struct {
		p    float64
		want float64
		tol  float64
	}{
		{0.5, 0, 0.001},      // median
		{0.05, -1.645, 0.01}, // 5th percentile
		{0.95, 1.645, 0.01},  // 95th percentile
		{0.01, -2.33, 0.05},  // 1st percentile
		{0.99, 2.33, 0.05},   // 99th percentile
	}

	for _, tt := range tests {
		got := inverseNormalCDF(tt.p)
		if math.Abs(got-tt.want) > tt.tol {
			t.Errorf("inverseNormalCDF(%v) = %v, want %v (±%v)", tt.p, got, tt.want, tt.tol)
		}
	}
}

func TestFindCForSLO_Minimality(t *testing.T) {
	// Test that FindCForSLO returns minimal c
	lambda := 100.0 // 100 jobs/sec
	tau := 0.02     // 20ms service time
	wTarget := 0.05 // 50ms total latency target

	c, w, err := FindCForSLO(lambda, tau, wTarget, 0, 0, 0)
	if err != nil {
		t.Fatalf("FindCForSLO() error = %v", err)
	}

	// Verify the result meets the SLO
	if w > wTarget {
		t.Errorf("FindCForSLO() returned W=%v > target=%v", w, wTarget)
	}

	// Verify c-1 would NOT meet the SLO
	if c > 3 {
		_, wLower, errLower := MmcWait(lambda, 1.0/tau, c-1, 0, 0)
		if errLower == nil && wLower <= wTarget {
			t.Errorf("c=%d meets SLO but c-1=%d also meets it (W=%v), not minimal", c, c-1, wLower)
		}
	}

	t.Logf("FindCForSLO: λ=%.0f/s τ=%.0fms W_target=%.0fms => c=%d W=%.3fms",
		lambda, tau*1000, wTarget*1000, c, w*1000)
}

func BenchmarkGetQueueMetrics(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = GetQueueMetrics(100.0, 2.0, 55)
	}
}

func BenchmarkApproxWorkersForQueueProb(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ApproxWorkersForQueueProb(100.0, 2.0, 0.05)
	}
}

func BenchmarkFindCForSLO(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, _ = FindCForSLO(100.0, 0.02, 0.05, 0, 0, 0)
	}
}
