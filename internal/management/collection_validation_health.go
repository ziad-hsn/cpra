package management

import (
	"context"
	"errors"
	"time"
)

// collectionValidationHealth preserves a temporary leadership cause without
// treating a failed catalog or permanent storage fault as retryable.
func (c *Catalog) collectionValidationHealth(ctx context.Context, at time.Time) error {
	if !c.verified.Load() || c.failed.Load() {
		return ErrUnavailable
	}
	if c.store.Status().Ready {
		return nil
	}
	_, err := c.store.CollectionValidationWork(ctx, at)
	if !c.verified.Load() || c.failed.Load() {
		return ErrUnavailable
	}
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}
