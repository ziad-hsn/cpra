package management

import (
	"context"
	"errors"
	"time"
)

// collectionValidationHealth preserves a temporary leadership cause without
// treating a failed catalog or permanent storage fault as retryable.
func (c *Catalog) collectionValidationHealth(ctx context.Context, at time.Time) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.verified.Load() || c.failed.Load() {
		return ErrUnavailable
	}
	status, err := c.store.StatusContext(ctx)
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	if status.Ready {
		return nil
	}
	_, err = c.store.CollectionValidationWork(ctx, at)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.verified.Load() || c.failed.Load() {
		return ErrUnavailable
	}
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}
