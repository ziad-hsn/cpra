//go:build externaljobs

package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// WorkerExecutionTimeout limits a controller window by the authenticated,
// immutable JobType contract. Queue time is part of this window; callers must
// anchor its deadline to the original scheduled time.
func (c *Catalog) WorkerExecutionTimeout(ctx context.Context, ref persistence.JobTypeReference, maximum time.Duration) (time.Duration, error) {
	if maximum <= 0 || maximum > 24*time.Hour || ref.Validate() != nil {
		return 0, persistence.ErrWorkerExecutionInvalid
	}
	if err := c.readyContext(ctx); err != nil {
		return 0, err
	}
	contract, err := c.workerExecutionContract(ctx, ref)
	if err != nil {
		return 0, err
	}
	if contract.timeout > 0 {
		maximum = min(maximum, contract.timeout)
	}
	return maximum, nil
}
