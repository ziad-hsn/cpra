package queue

import (
	"errors"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"sync"
)

func cancelledDispatch(job jobs.Job) bool {
	d, ok := job.(*jobs.Dispatch)
	return ok && d.CancelledBeforeStart()
}

// CancelPending visits only this monitor's bounded operational entries. keep
// reads the owner's immutable committed projection and performs no I/O.
func (q *FairQueue) CancelPending(monitorID string, keep func(string) bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, dispatch := range q.pendingActions[monitorID] {
		if !keep(id) {
			dispatch.CancelBeforeStart()
		}
	}
	q.notify()
}
func (h *Handle) CancelPending(monitorID string, keep func(string) bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if q, ok := h.queue.(*FairQueue); ok {
		q.CancelPending(monitorID, keep)
	}
}

// SetNotificationConcurrency configures the destination cap; global reservations
// and the single local delivery sequence remain stricter limits.
func (q *FairQueue) SetNotificationConcurrency(limits map[string]int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.notifications {
		return errors.New("notification limits require a notification queue")
	}
	for driver, limit := range limits {
		if q.drivers[driver] == nil || limit < 1 || limit > 16 {
			return errors.New("invalid or unavailable notification concurrency")
		}
	}
	q.concurrency = map[string]int{"webhook": 4}
	for driver, limit := range limits {
		q.concurrency[driver] = limit
	}
	q.notify()
	return nil
}
func (l *fairLease) Execute() jobs.Result {
	r := l.Job.Execute()
	if l.queue.notifications {
		if dispatch, ok := l.Job.(*jobs.Dispatch); ok && dispatch.FenceResult {
			l.fenced = true
			var once sync.Once
			r.ReleaseDispatch = func() {
				once.Do(func() {
					q := l.queue
					q.mu.Lock()
					f := l.flow
					q.releaseNotificationRates(l, r)
					l.released = true
					if l.completed {
						q.outstanding--
					}
					delete(f.sequences, l.sequence)
					if f.active == 0 && f.first == nil && len(f.sequences) == 0 {
						delete(l.driver.flows, f.key)
					}
					q.mu.Unlock()
					q.notify()
				})
			}
		}
	}
	return r
}
