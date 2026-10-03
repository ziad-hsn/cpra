//go:build externaljobs

package systems

import (
	"errors"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

const (
	workerAdmissionRetryMin = 250 * time.Millisecond
	workerAdmissionRetryMax = 5 * time.Second
	workerAdmissionTypes    = 4096
)

type workerAdmissionKey struct{ id, uid string }

type workerAdmissionRetry struct {
	until time.Time
	delay time.Duration
}

// The ECS owner owns these temporary delays. They grant no execution authority,
// alter no original deadline and never retry an uncertain external operation.
type workerAdmissionBackoff struct {
	types         map[workerAdmissionKey]workerAdmissionRetry
	overflowUntil time.Time
}

func (b *workerAdmissionBackoff) ready(key workerAdmissionKey, now time.Time) bool {
	return !now.Before(b.overflowUntil) && !now.Before(b.types[key].until)
}

func (b *workerAdmissionBackoff) observe(key workerAdmissionKey, err error, now time.Time) {
	if err == nil {
		delete(b.types, key)
		return
	}
	if !errors.Is(err, persistence.ErrWorkerExecutionQuota) {
		return
	}
	prior, exists := b.types[key]
	if exists && now.Before(prior.until) {
		return // One rejected batch must not multiply the delay for each result.
	}
	if !exists && len(b.types) >= workerAdmissionTypes {
		for k, retry := range b.types {
			if !now.Before(retry.until) {
				delete(b.types, k)
			}
		}
		if len(b.types) >= workerAdmissionTypes {
			// Bound state during rapid type/incarnation churn. This brief shared
			// pause admits no extra record and expires without a storage change.
			b.overflowUntil = now.Add(workerAdmissionRetryMin)
			return
		}
	}
	if b.types == nil {
		b.types = make(map[workerAdmissionKey]workerAdmissionRetry)
	}
	delay := min(workerAdmissionRetryMax, max(workerAdmissionRetryMin, 2*prior.delay))
	if now.Sub(prior.until) > time.Minute {
		delay = workerAdmissionRetryMin
	}
	b.types[key] = workerAdmissionRetry{until: now.Add(delay), delay: delay}
}
