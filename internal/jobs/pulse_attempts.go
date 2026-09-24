package jobs

import (
	"context"
	"errors"
	"time"
)

// pulseAttempts gives each attempt a cancellable context within the original
// check deadline. The first attempt retains the full remaining timeout; retries
// use only what remains, including their delays.
type pulseAttempts struct {
	parent                 context.Context
	context                context.Context
	cancel                 context.CancelFunc
	started                time.Time
	limit, count, timeouts int
	duration, delay        time.Duration
}

func newPulseAttempts(ctx context.Context, retries int) *pulseAttempts {
	limit := max(0, retries)
	if limit < int(^uint(0)>>1) {
		limit++
	}
	return &pulseAttempts{parent: ctx, limit: limit}
}

func (a *pulseAttempts) finishAttempt() {
	if a.cancel == nil {
		return
	}
	a.duration += time.Since(a.started)
	if errors.Is(a.context.Err(), context.DeadlineExceeded) {
		a.timeouts++
	}
	a.cancel()
	a.cancel = nil
}

func (a *pulseAttempts) next() (context.Context, bool) {
	a.finishAttempt()
	if a.count >= a.limit || a.parent.Err() != nil {
		return nil, false
	}
	left := a.limit - a.count
	if a.count > 0 {
		delay := min(50*time.Millisecond, remaining(a.parent)/time.Duration(left)/2)
		if delay > 0 {
			started := time.Now()
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-a.parent.Done():
			}
			timer.Stop()
			a.delay += time.Since(started)
		}
	}
	if a.parent.Err() != nil {
		return nil, false
	}
	budget := remaining(a.parent)
	if budget <= 0 {
		return nil, false
	}
	a.context, a.cancel = context.WithTimeout(a.parent, budget)
	a.started = time.Now()
	a.count++
	return a.context, true
}

func (a *pulseAttempts) complete(r *Result) {
	a.finishAttempt()
	r.Attempts, r.AttemptTimeouts = a.count, a.timeouts
	r.AttemptDuration, r.RetryDelay = a.duration, a.delay
	if err := a.parent.Err(); err != nil && (r.Err != nil || a.count == 0) {
		r.Err = errors.Join(r.Err, err)
	}
}
