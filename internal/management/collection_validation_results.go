package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// CollectionValidationResultView exposes only the original protected result.
// The HTTP boundary authenticates actor and checks read permissions before this
// call; storage checks original ownership before opening any result rows.
func (c *Catalog) CollectionValidationResultView(ctx context.Context, id, actor string, at time.Time) (*persistence.CollectionValidationResultView, persistence.CollectionValidationResultStatus, error) {
	if ctx == nil {
		return nil, persistence.CollectionValidationResultStatus{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, persistence.CollectionValidationResultStatus{}, err
	}
	if !c.Ready() {
		return nil, persistence.CollectionValidationResultStatus{}, ErrUnavailable
	}
	return c.store.CollectionValidationResultView(ctx, id, actor, at)
}
