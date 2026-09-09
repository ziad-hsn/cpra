package queue

import (
	"errors"
	"math"
	"testing"
)

// TestErlangCKnownValues verifies erlangC against hand-computed M/M/c results.
func TestErlangCKnownValues(t *testing.T) {
	cases := []struct {
		name   string
		lambda float64
		mu     float64
		c      int
		wantPw float64
		wantP0 float64
	}{
		// M/M/1 with rho=0.5: Pw = rho = 0.5, P0 = 1-rho = 0.5.
		{"mm1-rho0.5", 0.5, 1.0, 1, 0.5, 0.5},
		// M/M/2 with a=1 (rho=0.5): Pw = 1/3, P0 = 1/3.
		{"mm2-a1", 1.0, 1.0, 2, 1.0 / 3.0, 1.0 / 3.0},
		// M/M/2 with a=1.5 (rho=0.75): Pw = 4.5/7.
		{"mm2-a1.5", 1.5, 1.0, 2, 4.5 / 7.0, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p0, pw, rho, err := erlangC(tc.lambda, tc.mu, tc.c)
			if err != nil {
				t.Fatalf("erlangC: %v", err)
			}
			if math.Abs(pw-tc.wantPw) > 1e-9 {
				t.Errorf("pw = %v, want %v", pw, tc.wantPw)
			}
			if tc.wantP0 != 0 && math.Abs(p0-tc.wantP0) > 1e-9 {
				t.Errorf("p0 = %v, want %v", p0, tc.wantP0)
			}
			if rho >= 1.0 {
				t.Errorf("rho = %v, want < 1", rho)
			}
		})
	}
}

// TestErlangCStability verifies the recurrence does not overflow to NaN/Inf
// for large offered loads (the original a^n/n! series overflowed).
func TestErlangCStability(t *testing.T) {
	// lambda=16000/s, mu=1/s => a=16000; c=20000 => rho=0.8.
	p0, pw, rho, err := erlangC(16000.0, 1.0, 20000)
	if err != nil {
		t.Fatalf("erlangC: %v", err)
	}
	if math.IsNaN(pw) || math.IsInf(pw, 0) {
		t.Errorf("pw = %v, want finite", pw)
	}
	if pw < 0 || pw > 1 {
		t.Errorf("pw = %v, want in [0,1]", pw)
	}
	if math.IsNaN(p0) || math.IsInf(p0, 0) {
		t.Errorf("p0 = %v, want finite", p0)
	}
	if rho != 0.8 {
		t.Errorf("rho = %v, want 0.8", rho)
	}
}

// TestErlangCNoArrivals verifies the degenerate lambda=0 case.
func TestErlangCNoArrivals(t *testing.T) {
	p0, pw, rho, err := erlangC(0.0, 1.0, 3)
	if err != nil {
		t.Fatalf("erlangC: %v", err)
	}
	if p0 != 1.0 || pw != 0.0 || rho != 0.0 {
		t.Errorf("got p0=%v pw=%v rho=%v, want 1,0,0", p0, pw, rho)
	}
}

// TestFindCForSLO verifies the minimal-server search returns a stable system.
func TestFindCForSLO(t *testing.T) {
	// lambda=10/s, tau=0.1s (mu=10/s), target W <= 0.2s.
	c, w, err := FindCForSLO(10.0, 0.1, 0.2, 0, 0, 100)
	if err != nil {
		t.Fatalf("FindCForSLO: %v", err)
	}
	if c < 2 {
		t.Errorf("c = %d, want >= 2 (need > lambda/mu = 1)", c)
	}
	if w <= 0 || math.IsNaN(w) {
		t.Errorf("w = %v, want positive finite", w)
	}
}

func TestFindCForSLOMinimumAndUnattainableBounds(t *testing.T) {
	c, w, err := FindCForSLO(.5, 1, 2, 1, 1, 64)
	if err != nil || c != 1 || math.Abs(w-2) > 1e-12 {
		t.Fatal(c, w, err)
	}
	for _, tc := range []struct {
		lambda, tau, target float64
		limit               int
	}{{10, .1, .05, 64}, {10, .1, .1, 64}, {100, .1, 1, 10}, {10, .1, .101, 2}} {
		_, _, err := FindCForSLO(tc.lambda, tc.tau, tc.target, 1, 1, tc.limit)
		if !errors.Is(err, ErrNoFeasibleCapacity) {
			t.Fatal(tc, err)
		}
	}
	if _, _, err := FindCForSLO(math.NaN(), .1, 1, 1, 1, 64); err == nil {
		t.Fatal("accepted NaN rate")
	}
}
func TestErlangCEmptyProbabilityAtLowLoadWithLargeCapacity(t *testing.T) {
	p0, _, _, err := erlangC(1, 1, 10000)
	if err != nil || math.Abs(p0-math.Exp(-1)) > 1e-10 || math.IsNaN(p0) {
		t.Fatal(p0, err)
	}
}

func TestFindCRejectsOverflowingVariability(t *testing.T) {
	if _, _, err := FindCForSLO(1, 1, 2, math.MaxFloat64, 1, 8192); err == nil {
		t.Fatal("accepted overflowing CV")
	}
}
