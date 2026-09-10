package queue

import (
	"math"
	"sync/atomic"
	"time"

	"cpra/internal/runtimeconfig"
	"cpra/internal/slo"
)

// feedback is owned by the pool's autoscaling goroutine. Only the public
// condition is read concurrently. It augments the existing model recommendation.
type feedback struct {
	recorder     *slo.Recorder
	config       runtimeconfig.SLO
	healthySince time.Time
	condition    atomic.Pointer[string]
}

func (p *DynamicWorkerPool) SetSLOFeedback(recorder *slo.Recorder, config runtimeconfig.SLO) {
	p.feedback = &feedback{recorder: recorder, config: config}
}

func (f *feedback) adjust(current, model, maximum, queued int, at time.Time) int {
	view := f.recorder.View(at, f.config.ControlWindow, f.config.MinimumSamples)
	condition := "insufficient_observations"
	var samples uint64
	queueBreach, resultBreach, downstream := false, false, false
	for _, r := range view.Reports {
		samples += r.Samples
		if r.QueueAttainment != nil && *r.QueueAttainment < .99 {
			queueBreach = true
		}
		if r.ResultAttainment != nil && *r.ResultAttainment < .99 {
			resultBreach = true
		}
		if r.Condition == "downstream_limited" {
			downstream = true
		}
	}
	desired := max(current, model)
	if samples >= f.config.MinimumSamples {
		switch {
		case downstream:
			condition = "downstream_limited"
			f.healthySince = time.Time{}
		case queueBreach && queued > 0:
			condition = "queue_delay"
			f.healthySince = time.Time{}
			desired = max(model, int(math.Ceil(float64(current)*1.25)))
		case queueBreach || resultBreach:
			condition = "controller_limited"
			f.healthySince = time.Time{}
		case !view.CoverageComplete:
			condition = "coverage_gap"
			f.healthySince = time.Time{}
		default:
			condition = "healthy"
			if f.healthySince.IsZero() {
				f.healthySince = at
			}
			if at.Sub(f.healthySince) >= f.config.HealthyHold {
				desired = max(model, int(math.Ceil(float64(current)*.9)))
			}
		}
	} else {
		f.healthySince = time.Time{}
	}
	f.condition.Store(&condition)
	return min(maximum, min(current*2, desired))
}

func (p *DynamicWorkerPool) sloCondition() string {
	if p.feedback == nil {
		return "disabled"
	}
	if c := p.feedback.condition.Load(); c != nil {
		return *c
	}
	return "insufficient_observations"
}
