package queue

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var ErrNoFeasibleCapacity = errors.New("no feasible worker capacity")

func finitePositive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// M/M/c Erlang C computations using the numerically stable Erlang-B recurrence.
// Returns P0 (empty probability), Pw (probability of waiting), and utilization rho.
//
// The direct series a^n/n! can overflow for large offered load a. We instead
// compute the Erlang-B blocking probability B via the
// recurrence B(0)=1, B(n) = a*B(n-1)/(n + a*B(n-1)), then derive Erlang-C from
// the identity Pw = B / (1 - rho*(1-B)). P0 is recovered in log space.
func erlangC(lambda, mu float64, c int) (p0, pw, rho float64, err error) {
	if c <= 0 || !finitePositive(mu) || lambda < 0 || math.IsNaN(lambda) || math.IsInf(lambda, 0) {
		return 0, 0, 0, fmt.Errorf("invalid params")
	}
	a := lambda / mu
	rho = a / float64(c)
	if rho >= 1.0 {
		return 0, 0, rho, fmt.Errorf("unstable: rho >= 1")
	}
	if a == 0 {
		// No arrivals: the system is always empty.
		return 1.0, 0.0, 0.0, nil
	}
	// Erlang-B blocking probability via the stable recurrence.
	b := 1.0
	for n := 1; n <= c; n++ {
		b = a * b / (float64(n) + a*b)
	}
	// Erlang-C waiting probability from Erlang-B: Pw = B / (1 - rho*(1-B)).
	pw = b / (1.0 - rho*(1.0-b))
	// Empty probability P0 = Pw * (1-rho) / (a^c/c!), with a^c/c! computed in
	// log space (log(a^c/c!) = c*log(a) - log(c!)) to avoid overflow.
	lg, _ := math.Lgamma(float64(c) + 1) // log(c!)
	logT := float64(c)*math.Log(a) - lg
	if pw == 0 {
		// When the waiting probability underflows, capacity is far into the
		// Poisson tail; the empty probability can still be represented.
		p0 = math.Exp(-a)
	} else {
		p0 = math.Exp(math.Log(pw) + math.Log1p(-rho) - logT)
	}
	return
}

// MmcWait returns mean queue wait and total latency. It applies the
// Allen-Cunneen variability factor, floored at the M/M/c baseline.
func MmcWait(lambda, mu float64, c int, ca, cs float64) (wq, w float64, err error) {
	p0, pw, _, e := erlangC(lambda, mu, c)
	if e != nil {
		return math.Inf(1), math.Inf(1), e
	}
	_ = p0 // not used directly beyond Pw
	denom := float64(c)*mu - lambda
	if denom <= 0 {
		return 0, 0, fmt.Errorf("unstable: capacity <= arrival")
	}
	baseWq := pw / denom
	// Variability inflation if provided (Allen–Cunneen)
	if ca < 0 || cs < 0 || math.IsNaN(ca) || math.IsNaN(cs) || math.IsInf(ca, 0) || math.IsInf(cs, 0) {
		return 0, 0, fmt.Errorf("invalid variability")
	}
	infl := math.Max(1, (ca*ca+cs*cs)/2)
	if math.IsInf(infl, 0) {
		return 0, 0, fmt.Errorf("variability exceeds numerical range")
	}
	wq = baseWq * infl
	w = wq + 1.0/mu
	return
}

// FindCForSLO finds the smallest stable capacity whose estimated mean total
// latency is at most wTarget seconds, subject to cMax.
func FindCForSLO(lambda, tau, wTarget, ca, cs float64, cMax int) (int, float64, error) {
	if lambda < 0 || math.IsNaN(lambda) || math.IsInf(lambda, 0) || !finitePositive(tau) || !finitePositive(wTarget) || cMax < 0 || ca < 0 || cs < 0 || math.IsNaN(ca) || math.IsNaN(cs) || math.IsInf(ca, 0) || math.IsInf(cs, 0) {
		return 0, 0, fmt.Errorf("invalid sizing inputs")
	}
	if cMax == 0 {
		cMax = 1_000_000
	}
	// Stability requires c > lambda*tau. Check bounds before converting to int.
	load := lambda * tau
	if load >= float64(cMax) || wTarget < tau || (lambda > 0 && wTarget == tau) {
		return 0, 0, fmt.Errorf("%w up to %d workers", ErrNoFeasibleCapacity, cMax)
	}
	lower := int(math.Floor(load)) + 1
	_, upperWait, err := MmcWait(lambda, 1/tau, cMax, ca, cs)
	if err != nil {
		return 0, 0, err
	}
	if upperWait > wTarget {
		return 0, 0, fmt.Errorf("%w up to %d workers", ErrNoFeasibleCapacity, cMax)
	}
	// Mean wait decreases with capacity. Binary search bounds the number of
	// Erlang-C evaluations, including when the latency target is unattainable.
	upper := cMax
	for lower < upper {
		mid := lower + (upper-lower)/2
		_, wait, err := MmcWait(lambda, 1/tau, mid, ca, cs)
		if err != nil {
			return 0, 0, err
		}
		if wait <= wTarget {
			upper = mid
		} else {
			lower = mid + 1
		}
	}
	_, wait, err := MmcWait(lambda, 1/tau, lower, ca, cs)
	return lower, wait, err
}

// RecommendCFromObserved computes a recommended worker count from observed queue stats and worker pool stats.
// It estimates lambda from enqueue rate, tau from per-worker throughput, and targets a total latency of wqTarget+tau.
func RecommendCFromObserved(qs Stats, wp WorkerPoolStats, wqTarget time.Duration, ca, cs float64) (int, float64, error) {
	lambda := qs.EnqueueRate // jobs/sec
	if lambda <= 0 {
		return wp.RunningWorkers, 0, fmt.Errorf("no arrivals observed")
	}
	// Estimate per-worker mu from dequeue rate and running workers
	running := wp.RunningWorkers
	if running <= 0 || qs.DequeueRate <= 0 {
		return wp.RunningWorkers, 0, fmt.Errorf("insufficient throughput data")
	}
	muPerWorker := qs.DequeueRate / float64(running)
	if muPerWorker <= 0 {
		return wp.RunningWorkers, 0, fmt.Errorf("invalid per-worker throughput")
	}
	tau := 1.0 / muPerWorker
	wTarget := wqTarget.Seconds() + tau // total latency target ≈ queue target + service time
	c, w, err := FindCForSLO(lambda, tau, wTarget, ca, cs, 0)
	return c, w, err
}
