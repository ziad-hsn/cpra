package queue

import (
    "fmt"
    "math"
    "time"
)

// M/M/c Erlang C computations with numerically stable series expansions.
// Returns P0 (empty probability), Pw (probability of waiting), and utilization rho.
func erlangC(lambda, mu float64, c int) (p0, pw, rho float64, err error) {
    if c <= 0 || mu <= 0 || lambda < 0 {
        return 0, 0, 0, fmt.Errorf("invalid params")
    }
    rho = lambda / (float64(c) * mu)
    if rho >= 1.0 {
        return 0, 0, rho, fmt.Errorf("unstable: rho >= 1")
    }
    a := lambda / mu
    // Compute sum_{n=0}^{c-1} a^n / n!
    sum := 1.0
    term := 1.0
    for n := 1; n <= c-1; n++ {
        term *= a / float64(n)
        sum += term
    }
    // termC = a^c / c!
    termC := term * (a / float64(c))
    p0 = 1.0 / (sum + termC*(1.0/(1.0-rho)))
    pw = termC * (1.0/(1.0-rho)) * p0
    return
}

// MmcWait returns Wq and W for M/M/c; if Ca,Cs > 0, applies Allen–Cunneen variability inflation.
func MmcWait(lambda, mu float64, c int, ca, cs float64) (wq, w float64, err error) {
    p0, pw, _, e := erlangC(lambda, mu, c)
    if e != nil && !stringsContains(e.Error(), "unstable") { // allow unstable to bubble with values
        return 0, 0, e
    }
    _ = p0 // not used directly beyond Pw
    denom := float64(c)*mu - lambda
    if denom <= 0 {
        return 0, 0, fmt.Errorf("unstable: capacity <= arrival")
    }
    baseWq := pw / denom
    // Variability inflation if provided (Allen–Cunneen)
    infl := 1.0
    if ca > 0 && cs > 0 {
        infl = (ca*ca + cs*cs) / 2.0
        if infl < 1.0 {
            infl = 1.0 // never deflate; conservative
        }
    }
    wq = baseWq * infl
    w = wq + 1.0/mu
    return
}

// FindCForSLO finds minimal c such that W <= wTarget (seconds). If ca,cs provided (>0), uses Allen–Cunneen.
func FindCForSLO(lambda, tau, wTarget, ca, cs float64, cMax int) (int, float64, error) {
    if tau <= 0 || wTarget <= 0 {
        return 0, 0, fmt.Errorf("invalid tau or wTarget")
    }
    mu := 1.0 / tau
    // lower bound: ceil(lambda/mu)+1
    c := int(math.Ceil(lambda/mu)) + 1
    if c < 1 {
        c = 1
    }
    if cMax <= 0 {
        cMax = 1_000_000
    }
    for ; c <= cMax; c++ {
        wq, w, err := MmcWait(lambda, mu, c, ca, cs)
        if err == nil && w <= wTarget {
            return c, w, nil
        }
        _ = wq
    }
    return 0, 0, fmt.Errorf("no c found up to %d to meet SLO", cMax)
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

// helper: avoid importing strings for one check
func stringsContains(s, substr string) bool {
    // simple contains
    return len(substr) == 0 || (len(s) >= len(substr) && (func() bool {
        for i := 0; i+len(substr) <= len(s); i++ {
            if s[i:i+len(substr)] == substr {
                return true
            }
        }
        return false
    })())
}

