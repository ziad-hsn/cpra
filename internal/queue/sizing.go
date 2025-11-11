package queue

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var ErrUnstable = errors.New("unstable")

// M/M/c Erlang C computations with numerically stable series expansions.
// Returns P0 (empty probability), Pw (probability of waiting), and utilization rho.
func erlangC(lambda, mu float64, c int) (p0, pw, rho float64, err error) {
	if c <= 0 || mu <= 0 || lambda < 0 {
		return 0, 0, 0, fmt.Errorf("invalid params")
	}
	rho = lambda / (float64(c) * mu)
	if rho >= 1.0 {
		return 0, 0, rho, fmt.Errorf("%w: rho >= 1", ErrUnstable)
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
	pw = termC * (1.0 / (1.0 - rho)) * p0
	return
}

// MmcWait returns Wq and W for M/M/c; if Ca,Cs > 0, applies Allen–Cunneen variability inflation.
func MmcWait(lambda, mu float64, c int, ca, cs float64) (wq, w float64, err error) {
	p0, pw, _, e := erlangC(lambda, mu, c)
	if e != nil && !errors.Is(e, ErrUnstable) { // allow unstable to bubble with values
		return 0, 0, e
	}
	_ = p0 // not used directly beyond Pw
	denom := float64(c)*mu - lambda
	if denom <= 0 {
		return 0, 0, fmt.Errorf("%w: capacity <= arrival", ErrUnstable)
	}
	baseWq := pw / denom
	// Variability inflation if provided (Allen–Cunneen)
	infl := 1.0
	if ca > 0 || cs > 0 {
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
	base := int(math.Ceil(lambda/mu)) + 1
	if base < 1 {
		base = 1
	}
	if cMax <= 0 {
		// Provide a sane upper bound to avoid runaway iteration; allow modest headroom.
		cMax = maxInt(base*4, base+64)
	}

	// Check base first
	if _, w, err := MmcWait(lambda, mu, base, ca, cs); err == nil && w <= wTarget {
		return base, w, nil
	}

	// Exponential search to find an upper bracket where w <= target.
	lo, hi := base, base
	var wHi float64
	for {
		hi *= 2
		if hi > cMax {
			hi = cMax
		}
		_, w, err := MmcWait(lambda, mu, hi, ca, cs)
		if err == nil {
			wHi = w
		} else {
			wHi = math.Inf(1)
		}
		if wHi <= wTarget || hi == cMax {
			break
		}
		lo = hi
	}

	if wHi > wTarget {
		return 0, 0, fmt.Errorf("no c found up to %d to meet SLO", cMax)
	}

	// Binary search between lo (fails) and hi (meets) for minimal c.
	for lo+1 < hi {
		mid := (lo + hi) / 2
		_, w, err := MmcWait(lambda, mu, mid, ca, cs)
		if err != nil || w > wTarget {
			lo = mid
			continue
		}
		hi = mid
	}

	_, wFinal, err := MmcWait(lambda, mu, hi, ca, cs)
	if err != nil {
		return 0, 0, err
	}
	return hi, wFinal, nil
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
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// QueueMetrics contains key M/M/c queueing metrics for observability and capacity planning.
// All times are in seconds.
type QueueMetrics struct {
	Lambda       float64 // Arrival rate (λ) - jobs per second
	Mu           float64 // Service rate per worker (μ) - jobs per second per worker
	Servers      int     // Number of workers (c)
	Utilization  float64 // ρ = λ/(c*μ) - fraction of time workers are busy
	QueueProb    float64 // Pw - probability a job must wait in queue
	IdleProb     float64 // P0 - probability all workers are idle
	AvgWaitQueue float64 // Wq - average time spent waiting in queue (seconds)
	AvgWaitTotal float64 // W = Wq + 1/μ - total time in system (seconds)
	AvgWaitGiven float64 // Wq/Pw - average wait time IF queued (tail latency, seconds)
	Stable       bool    // True if ρ < 1 (queue won't grow unbounded)
}

// GetQueueMetrics computes comprehensive M/M/c queueing metrics for the given parameters.
//
// Parameters:
//   - lambda: arrival rate (jobs per second)
//   - mu: service rate per worker (jobs per second per worker), i.e., 1/τ where τ is mean service time
//   - c: number of parallel workers/servers
//
// Returns metrics including utilization, queue probability, wait times, and stability status.
// If the system is unstable (ρ >= 1), partial metrics are returned with Stable=false.
func GetQueueMetrics(lambda, mu float64, c int) (QueueMetrics, error) {
	m := QueueMetrics{
		Lambda:  lambda,
		Mu:      mu,
		Servers: c,
	}

	if c <= 0 || mu <= 0 {
		return m, fmt.Errorf("invalid parameters: c=%d, mu=%.4f", c, mu)
	}
	if lambda < 0 {
		return m, fmt.Errorf("invalid arrival rate: lambda=%.4f", lambda)
	}

	// Handle zero arrival rate (idle system)
	if lambda == 0 {
		m.Utilization = 0
		m.QueueProb = 0
		m.IdleProb = 1
		m.AvgWaitQueue = 0
		m.AvgWaitTotal = 1.0 / mu
		m.AvgWaitGiven = 0
		m.Stable = true
		return m, nil
	}

	// Compute Erlang C metrics
	p0, pw, rho, err := erlangC(lambda, mu, c)

	m.Utilization = rho
	m.Stable = rho < 1.0

	if err != nil {
		// Unstable system - return partial metrics
		return m, err
	}

	m.QueueProb = pw
	m.IdleProb = p0

	// Compute wait times
	denom := float64(c)*mu - lambda
	if denom <= 0 {
		return m, fmt.Errorf("%w: capacity <= arrival", ErrUnstable)
	}

	m.AvgWaitQueue = pw / denom
	m.AvgWaitTotal = m.AvgWaitQueue + 1.0/mu

	// Conditional wait time (if queued)
	// Wq_given = Wq / Pw = 1 / (c*μ - λ)
	if pw > 0 {
		m.AvgWaitGiven = m.AvgWaitQueue / pw
	} else {
		m.AvgWaitGiven = 0
	}

	return m, nil
}

// ApproxWorkersForQueueProb returns an approximate number of workers needed to achieve
// a target queue probability using the square root staffing formula (Rossetti, 2021).
//
// The approximation is: c ≈ a + z*sqrt(a) where a = λ/μ (offered load)
// and z is derived from the target probability using the inverse normal CDF.
//
// This is faster than binary search in FindCForSLO and useful for quick estimates.
// For precise results, use FindCForSLO with the output as an initial guess.
//
// Parameters:
//   - lambda: arrival rate (jobs per second)
//   - mu: service rate per worker (jobs per second per worker)
//   - targetProb: target probability of waiting in queue (e.g., 0.05 for 5%)
//
// Returns the ceiling of the approximation, ensuring at least ceil(a)+1 workers.
func ApproxWorkersForQueueProb(lambda, mu, targetProb float64) int {
	if lambda <= 0 || mu <= 0 || targetProb <= 0 || targetProb >= 1 {
		// Invalid params - return minimum stable workers
		if lambda > 0 && mu > 0 {
			return int(math.Ceil(lambda/mu)) + 1
		}
		return 1
	}

	a := lambda / mu // offered load

	// z-score from target probability using Hastings approximation for inverse normal
	// For small probabilities, z is negative (we want fewer people waiting)
	// We use the relationship: Pw ≈ 1 - Φ(z*sqrt(a - c + 0.5))
	// Rearranged: z ≈ Φ^(-1)(1 - targetProb) / sqrt(a)
	z := inverseNormalCDF(1 - targetProb)

	// Square root staffing formula: c ≈ a + z*sqrt(a)
	cApprox := a + z*math.Sqrt(a)

	// Ensure at least minimum stable workers
	minStable := int(math.Ceil(a)) + 1
	result := int(math.Ceil(cApprox))

	if result < minStable {
		return minStable
	}
	return result
}

// inverseNormalCDF computes the inverse of the standard normal CDF using
// the Hastings approximation (accurate to ~4.5e-4).
func inverseNormalCDF(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}

	// Use symmetry for p > 0.5
	if p > 0.5 {
		return -inverseNormalCDF(1 - p)
	}

	// Hastings approximation for 0 < p <= 0.5
	// This approximation gives the negative quantile (left tail)
	t := math.Sqrt(-2 * math.Log(p))

	// Coefficients for Hastings approximation
	c0 := 2.515517
	c1 := 0.802853
	c2 := 0.010328
	d1 := 1.432788
	d2 := 0.189269
	d3 := 0.001308

	// For p < 0.5, the result should be negative
	return -(t - (c0+c1*t+c2*t*t)/(1+d1*t+d2*t*t+d3*t*t*t))
}

// IsStable returns true if the queue will not grow unbounded.
// A queue is stable when the arrival rate is less than the total service capacity.
//
// Stability requires: λ < c*μ  (equivalently: λ*τ < c where τ = 1/μ)
//
// Parameters:
//   - lambda: arrival rate (jobs per second)
//   - tau: mean service time per job (seconds), i.e., 1/μ
//   - c: number of parallel workers
func IsStable(lambda, tau float64, c int) bool {
	if c <= 0 || tau <= 0 {
		return false
	}
	if lambda <= 0 {
		return true // no arrivals is trivially stable
	}
	// Stable when λ*τ < c
	return lambda*tau < float64(c)
}

// MinWorkersForStability returns the minimum number of workers required
// for a stable queue (one that won't grow unbounded).
//
// This is: ceil(λ*τ) + 1 = ceil(λ/μ) + 1
//
// This is the absolute minimum - in practice you want more headroom.
// Use WorkersForUtilization for capacity planning.
//
// Parameters:
//   - lambda: arrival rate (jobs per second)
//   - tau: mean service time per job (seconds)
func MinWorkersForStability(lambda, tau float64) int {
	if lambda <= 0 || tau <= 0 {
		return 1
	}
	// Minimum is ceil(λ*τ) + 1 to ensure ρ < 1
	return int(math.Ceil(lambda*tau)) + 1
}

// WorkersForUtilization returns the number of workers needed to achieve
// a target utilization level.
//
// The formula is: c = ceil(λ*τ / targetUtil)
//
// Common targets:
//   - 0.80 (80%): Standard call center rule of thumb, queue times start to grow
//   - 0.60 (60%): Recommended for police/emergency services (proactive capacity)
//   - 0.50 (50%): Conservative, lots of headroom for spikes
//
// Parameters:
//   - lambda: arrival rate (jobs per second)
//   - tau: mean service time per job (seconds)
//   - targetUtil: target utilization (0 < targetUtil < 1), e.g., 0.80 for 80%
func WorkersForUtilization(lambda, tau, targetUtil float64) int {
	if lambda <= 0 || tau <= 0 {
		return 1
	}
	if targetUtil <= 0 || targetUtil >= 1 {
		// Invalid target - use 80% as default
		targetUtil = 0.8
	}

	// c = λ*τ / targetUtil
	c := (lambda * tau) / targetUtil
	result := int(math.Ceil(c))

	// Ensure at least minimum stable workers
	minStable := MinWorkersForStability(lambda, tau)
	if result < minStable {
		return minStable
	}
	return result
}

// Utilization returns the utilization (ρ) for given parameters.
// ρ = λ / (c * μ) = λ * τ / c
//
// A utilization >= 1.0 means the queue is unstable and will grow unbounded.
func Utilization(lambda, tau float64, c int) float64 {
	if c <= 0 || tau <= 0 {
		return math.Inf(1)
	}
	if lambda <= 0 {
		return 0
	}
	return (lambda * tau) / float64(c)
}
