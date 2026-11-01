//go:build externaljobs

package persistence

import "time"

// ExecutionDeadline is fixed by the first committed start. Historical intents
// retain their absolute deadline, including when an offer or process is replaced.
func (r WorkerExecutionRecord) ExecutionDeadline() time.Time {
	if r.Intent.ExecutionBudget == 0 {
		return r.Intent.Deadline
	}
	if r.Lifecycle == nil || r.Lifecycle.GrantID == "" {
		return time.Time{}
	}
	return r.Lifecycle.StartedAt.Add(r.Intent.ExecutionBudget)
}

func (r WorkerExecutionRecord) executionExpired(at time.Time) bool {
	deadline := r.ExecutionDeadline()
	return !deadline.IsZero() && !at.Before(deadline)
}

// AssignmentDeadline is an upper bound, not a start grant. The worker receives
// the exact execution deadline from Start and must honor the smaller deadline.
func (r WorkerExecutionRecord) AssignmentDeadline() time.Time {
	return workerAssignmentDeadline(r.Intent, r.Lifecycle)
}

func workerAssignmentDeadline(in WorkerExecutionIntent, lease *WorkerExecutionLifecycle) time.Time {
	if in.ExecutionBudget > 0 && lease != nil {
		return lease.OfferDeadline.Add(in.ExecutionBudget)
	}
	return in.Deadline
}

// ValidTiming is shared by plaintext preparation and sealed-intent validation.
// New notifications and recoveries may wait without an execution deadline;
// the committed envelope format and action binding validate their permission.
func (in WorkerExecutionIntent) ValidTiming() bool {
	if in.Scheduled.IsZero() || in.Scheduled.Year() < 1 || in.Scheduled.Year() > 9998 {
		return false
	}
	if in.ExecutionBudget != 0 {
		return (in.Category == "notification" || in.Category == "recovery") && in.ExecutionBudget > 0 && in.ExecutionBudget <= 24*time.Hour && in.Deadline.IsZero()
	}
	return in.Deadline.After(in.Scheduled) && in.Deadline.Sub(in.Scheduled) <= 24*time.Hour && in.Deadline.Year() <= 9999
}

func (in WorkerExecutionIntent) ExecutionBudgetDuration() time.Duration {
	if in.ExecutionBudget > 0 {
		return in.ExecutionBudget
	}
	return in.Deadline.Sub(in.Scheduled)
}
