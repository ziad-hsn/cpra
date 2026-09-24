package management

import (
	"context"
	"errors"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// OperationAs preserves shared ordinary operation receipts and restricts
// collection observations to their original authenticated actor. The HTTP owner
// supplies allowCollections only after enforcing the current collection read
// floor, and must recheck authorization after the read. This method does not
// decrypt input, read validation-result rows or authorize any execution.
func (c *Catalog) OperationAs(ctx context.Context, id, actor string, at time.Time, allowCollections bool) (api.Operation, error) {
	empty := api.Operation{}
	if ctx == nil {
		return empty, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !c.verified.Load() || c.failed.Load() {
		return empty, ErrUnavailable
	}
	if !validID(id) {
		return empty, persistence.ErrOperationNotFound
	}
	receipt, err := c.store.OperationContext(ctx, id, at)
	if err == nil {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		return operationView(receipt), nil
	}
	if !errors.Is(err, persistence.ErrOperationExpired) && !errors.Is(err, persistence.ErrOperationNotFound) {
		return empty, err
	}
	if !allowCollections {
		return empty, persistence.ErrOperationNotFound
	}
	collection, err := c.store.CollectionOperationAs(ctx, id, actor, at)
	if err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return collectionOperationView(collection), nil
}
