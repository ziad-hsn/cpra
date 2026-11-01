package queue

import (
	"github.com/ziad-hsn/cpra/internal/jobs"
	"slices"
	"time"
)

const maxNotificationScopes = 4096

type notificationRate struct {
	state           jobs.NotificationRateState
	active          int
	releaseInterval time.Duration
}

// SetNotificationRates installs committed hints before dispatch and after
// reconciliation. Local reservations and longer known pauses are preserved.
func (q *FairQueue) SetNotificationRates(states map[string]jobs.NotificationRateState) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.notifications {
		return
	}
	q.pruneNotificationRates(time.Now())
	if q.rates == nil {
		q.rates = map[string]*notificationRate{}
	}
	for id, state := range states {
		rate := q.rates[id]
		if rate == nil {
			if len(q.rates) >= maxNotificationScopes {
				continue
			}
			rate = &notificationRate{}
			q.rates[id] = rate
		}
		mergeNotificationRate(&rate.state, state)
	}
	q.notify()
}
func (q *FairQueue) pruneNotificationRates(at time.Time) {
	for id, r := range q.rates {
		if r.active == 0 && !at.Before(r.state.NotBefore) && !at.Before(r.state.SpacingUntil) {
			delete(q.rates, id)
		}
	}
}
func (q *FairQueue) notificationRateAvailable(job jobs.Job, at time.Time) bool {
	d, ok := job.(*jobs.Dispatch)
	if !ok || len(d.RateScopes) == 0 {
		return true
	}
	needed := 0
	for _, scope := range d.RateScopes {
		r := q.rates[scope.ID]
		if r == nil {
			needed++
			continue
		}
		if at.Before(r.state.NotBefore) || (scope.Serial || scope.Interval > 0 || r.releaseInterval > 0 || at.Before(r.state.SpacingUntil) && r.state.Spacing > 0) && r.active > 0 {
			return false
		}
	}
	if len(q.rates)+needed > maxNotificationScopes {
		q.pruneNotificationRates(at)
		needed = 0
		for _, scope := range d.RateScopes {
			if q.rates[scope.ID] == nil {
				needed++
			}
		}
	}
	return len(q.rates)+needed <= maxNotificationScopes
}
func (q *FairQueue) reserveNotificationRates(job jobs.Job) []jobs.NotificationRateScope {
	d, ok := job.(*jobs.Dispatch)
	if !ok || len(d.RateScopes) == 0 {
		return nil
	}
	if q.rates == nil {
		q.rates = map[string]*notificationRate{}
	}
	for _, scope := range d.RateScopes {
		r := q.rates[scope.ID]
		if r == nil {
			r = &notificationRate{}
			q.rates[scope.ID] = r
		}
		r.active++
		r.releaseInterval = max(r.releaseInterval, scope.Interval)
	}
	return slices.Clone(d.RateScopes)
}
func (q *FairQueue) releaseNotificationRates(l *fairLease, result jobs.Result) {
	at := result.ExecutionEnd
	if at.IsZero() {
		at = time.Now()
	}
	for _, scope := range l.rateScopes {
		r := q.rates[scope.ID]
		if r == nil {
			continue
		}
		if feedback := result.RateFeedback; feedback != nil {
			for _, update := range feedback.Updates {
				if update.ID == scope.ID {
					mergeNotificationRate(&r.state, update.State)
				}
			}
		}
		interval := max(scope.Interval, r.releaseInterval)
		if at.Before(r.state.SpacingUntil) {
			interval = max(interval, r.state.Spacing)
		}
		if next := at.Add(interval); next.After(r.state.NotBefore) {
			r.state.NotBefore = next
		}
		r.active--
		if r.active == 0 {
			r.releaseInterval = 0
		}
		if r.active == 0 && !at.Before(r.state.NotBefore) && !at.Before(r.state.SpacingUntil) {
			delete(q.rates, scope.ID)
		}
	}
	l.rateScopes = nil
}
func mergeNotificationRate(old *jobs.NotificationRateState, next jobs.NotificationRateState) {
	if next.NotBefore.After(old.NotBefore) {
		old.NotBefore = next.NotBefore
	}
	if next.SpacingUntil.After(old.SpacingUntil) {
		old.SpacingUntil = next.SpacingUntil
	}
	old.Spacing = max(old.Spacing, next.Spacing)
}
func (h *Handle) SetNotificationRates(states map[string]jobs.NotificationRateState) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if q, ok := h.queue.(*FairQueue); ok {
		q.SetNotificationRates(states)
	}
}
