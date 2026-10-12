package systems

import "time"

// SetWakeup connects background projection preparation before initialization.
// Only immutable handoff messages cross goroutines; the owner still reads and
// writes all ECS/scheduler state itself. The owner never closes this channel.
func (s *DurableSystem) SetWakeup(wakeup chan<- struct{}) { s.wakeup = wakeup }

// NextUpdate is called only by the ECS owner after Update. Work already ready
// retains the configured tick interval. Idle waits stop at the earliest check,
// action, snooze or SLO deadline, with a one-second progress/health fallback.
// It neither queries disk nor scans the fleet, and hints cannot authorize work.
func (s *DurableSystem) NextUpdate(now time.Time, interval time.Duration) time.Duration {
	if s.lastError != nil {
		return max(interval, time.Second)
	}
	if s.pendingWrite != nil || s.recovering.Load() || s.controlsPending || s.controlView != nil ||
		s.resultsBuffered() || s.scheduler.ReadyLen() != 0 || s.actions.ReadyLen() != 0 || s.workerNeedsUpdate() ||
		s.catalogRuntime != nil && len(s.catalogRuntime.updates) != 0 {
		return interval
	}
	wait := time.Second
	include := func(at time.Time) {
		wait = min(wait, at.Sub(now))
	}
	if at, ok := s.scheduler.NextWake(); ok {
		include(at)
	}
	if at, ok := s.actions.NextWake(); ok {
		include(at)
	}
	if s.snoozes.Len() != 0 {
		include(s.snoozes.entries[0].at)
	}
	include(s.lastSLO.Add(s.config.SLO.EvaluationInterval))
	return max(interval, wait)
}
