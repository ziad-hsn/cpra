package slo

import "time"

// pauseExposure aggregates owner-observed membership, not a per-monitor time
// series. Active membership is rebuilt after restart; only elapsed exposure is
// retained in the normal bounded aggregate window.
type pauseExposure struct {
	count uint64
	at    time.Time
}

// ChangePaused records an owner-installed pause/resume. delta must be +1 or -1
// and one monitor may contribute at most once, even if disabled and snoozed.
// It never changes scheduled obligations, misses, samples or attainment.
func (r *Recorder) ChangePaused(driver string, delta int, at time.Time) {
	if driver == "" || (delta != 1 && delta != -1) || at.IsZero() || at.UnixNano() < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pauses == nil {
		r.pauses = make(map[string]pauseExposure)
	}
	p := r.pauses[driver]
	if delta < 0 && p.count == 0 {
		return
	}
	if p.at.IsZero() {
		p.at = at
	}
	r.accruePause(driver, &p, at)
	if delta > 0 {
		p.count++
	} else {
		p.count--
	}
	r.pauses[driver] = p
	if r.state.ByDriver[driver] == nil {
		r.state.ByDriver[driver] = new([Slots]Bucket)
	}
}

func (r *Recorder) flushPauses(at time.Time) {
	for driver, p := range r.pauses {
		r.accruePause(driver, &p, at)
		r.pauses[driver] = p
	}
}

// At most 60 slots are visited after a long pause. Backward wall-clock movement
// does not subtract exposure or count an interval twice.
func (r *Recorder) accruePause(driver string, p *pauseExposure, at time.Time) {
	if !at.After(p.at) {
		return
	}
	if p.count == 0 {
		p.at = at
		return
	}
	start := p.at
	oldest := time.Unix(0, (at.UnixNano()/int64(SlotDuration)-Slots+1)*int64(SlotDuration))
	if start.Before(oldest) {
		start = oldest
	}
	window := r.state.ByDriver[driver]
	if window == nil {
		window = new([Slots]Bucket)
		r.state.ByDriver[driver] = window
	}
	for start.Before(at) {
		epoch := start.UnixNano() / int64(SlotDuration)
		end := time.Unix(0, (epoch+1)*int64(SlotDuration))
		if end.After(at) {
			end = at
		}
		bucket := &window[epoch%Slots]
		if bucket.Epoch <= epoch {
			if bucket.Epoch != epoch {
				*bucket = Bucket{Epoch: epoch}
			}
			bucket.PausedSeconds += end.Sub(start).Seconds() * float64(p.count)
		}
		start = end
	}
	p.at = at
}
