// internal/queue/queue.go  (append at bottom or in a new file within the queue package)
package queue

import (
	"math"
	"time"
)

// WorldSummary carries just what we need from the World.
// Keep it minimal to avoid cross-package coupling.
type WorldSummary struct {
	// Pulse
	NumMonitors int
	// If monitors have heterogeneous intervals, fill IntervalHist
	// with counts by interval; otherwise leave it empty and set AvgInterval.
	IntervalHist map[time.Duration]int
	AvgInterval  time.Duration // used if IntervalHist is nil/empty

	// Service time assumptions (seconds) for the PULSE path.
	// If you don't know yet, start with a conservative guess (e.g., 150ms).
	MeanServicePulse time.Duration

	// Optional extras if you want to include downstream work in λ:
	// Fraction of pulses expected to fail and trigger intervention.
	PulseFailProb           float64 // e.g., 0.01 = 1%
	MeanServiceIntervention time.Duration
	// Fraction of interventions that trigger code alerts.
	InterventionEscalationProb float64 // e.g., 0.10 = 10%
	MeanServiceCode            time.Duration
}

// ComputeInitialSizingFromWorld returns:
//   - desiredWorkers: initial worker target
//   - suggestedQueueCap: queue size to keep Wq under wqTarget
//
// All without creating new packages or touching other layers.
func ComputeInitialSizingFromWorld(
	ws WorldSummary,
	rhoTarget float64, // utilization target, e.g., 0.75
	wqTarget time.Duration, // desired queue wait bound, e.g., 1s
	minWorkers, maxWorkers int,
	minQueue, maxQueue int,
) (desiredWorkers int, suggestedQueueCap int) {

	// 1) Average arrival rate for pulses
	var lambdaPulse float64
	if len(ws.IntervalHist) > 0 {
		for iv, n := range ws.IntervalHist {
			if iv > 0 && n > 0 {
				lambdaPulse += float64(n) / iv.Seconds()
			}
		}
	} else if ws.AvgInterval > 0 && ws.NumMonitors > 0 {
		lambdaPulse = float64(ws.NumMonitors) / ws.AvgInterval.Seconds()
	}

	// 2) Include expected downstream work (optional but useful)
	// λ_intervention ≈ λ_pulse * p_fail
	lambdaIntervention := lambdaPulse * clamp01(ws.PulseFailProb)
	// λ_code ≈ λ_intervention * p_escalate
	lambdaCode := lambdaIntervention * clamp01(ws.InterventionEscalationProb)

	// Total effective arrival rate across all classes we run in THIS pool.
	// If you run interventions/code in separate pools, omit them here.
	totalLambda := lambdaPulse + lambdaIntervention + lambdaCode

	// 3) Mean service time (seconds) — weighted by arrival mix.
	// If you keep everything in a single pool, weight by λ of each class.
	// If you separate pools, use the corresponding class’s service only.
	var es float64
	{
		esPulse := clampDur(ws.MeanServicePulse).Seconds()
		esIntv := clampDur(ws.MeanServiceIntervention).Seconds()
		esCode := clampDur(ws.MeanServiceCode).Seconds()

		den := totalLambda
		if den <= 0 {
			// fallback to pulse mean only
			es = esPulse
		} else {
			es = (lambdaPulse*esPulse + lambdaIntervention*esIntv + lambdaCode*esCode) / den
		}
	}

	// 4) Per-worker service rate μ (jobs/sec/worker), assuming 1-at-a-time per worker
	mu := 0.0
	if es > 0 {
		mu = 1.0 / es
	}

	// 5) Workers from Little’s Law + utilization target:
	// c = ceil( λ / (μ * ρ_target) )
	if totalLambda > 0 && mu > 0 && rhoTarget > 0 {
		desiredWorkers = int(math.Ceil(totalLambda / (mu * rhoTarget)))
	}

	desiredWorkers = clampInt(desiredWorkers, minWorkers, maxWorkers)

	// 6) Queue capacity to keep Wq under target:
	// K ≈ λ * Wq_target  (+ cushion if you want)
	k := int(math.Ceil(totalLambda * wqTarget.Seconds()))
	suggestedQueueCap = clampInt(k, minQueue, maxQueue)

	return desiredWorkers, suggestedQueueCap
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
func clampDur(d time.Duration) time.Duration {
	if d <= 0 {
		return 1 * time.Millisecond
	} // avoid div/0; pick tiny positive
	return d
}
func clampInt(v, lo, hi int) int {
	if hi > 0 && v > hi {
		return hi
	}
	if lo > 0 && v < lo {
		return lo
	}
	return v
}
