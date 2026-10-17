package jobs

import (
	"errors"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

// InterventionBudget resolves the same finite budget used by built-in jobs.
// It reads configuration only, before the action receives a durable deadline.
func InterventionBudget(target manifest.InterventionTarget) (time.Duration, error) {
	var budget time.Duration
	switch c := target.(type) {
	case *manifest.InterventionTargetDocker:
		return dockerOperationBudget(c.Timeout, c.OperationTimeout)
	case *manifest.InterventionTargetWebhook:
		budget = c.Timeout
	case *manifest.InterventionTargetKubernetes:
		budget = c.Timeout
	case *manifest.InterventionTargetSystemd:
		budget = c.Timeout
	case *manifest.InterventionTargetAWS:
		budget = c.Timeout
	case *manifest.InterventionTargetA2A:
		budget = c.TaskTimeout
		if budget == 0 {
			budget = time.Minute
		}
	default:
		return 0, errors.New("intervention budget requires a supported target")
	}
	if budget == 0 {
		budget = 10 * time.Second
	}
	if budget < 0 || budget > 24*time.Hour {
		return 0, errors.New("intervention budget must be positive and at most 24h")
	}
	return budget, nil
}
