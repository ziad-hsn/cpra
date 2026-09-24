package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// CollectionOperationObservation pairs one protected response with its original
// metadata fence. Response admission can recheck it without reading history.
type CollectionOperationObservation struct {
	Operation api.Operation
	read      persistence.CollectionOperationObservation
	catalog   *Catalog
}

func (c *Catalog) CollectionOperationObservation(ctx context.Context, id, actor string, at time.Time) (CollectionOperationObservation, error) {
	if ctx == nil || !c.verified.Load() || c.failed.Load() {
		return CollectionOperationObservation{}, ErrUnavailable
	}
	read, err := c.store.CollectionOperationObservation(ctx, id, actor, at)
	if err != nil {
		return CollectionOperationObservation{}, err
	}
	return CollectionOperationObservation{Operation: collectionOperationView(read.Receipt()), read: read, catalog: c}, nil
}

func (o CollectionOperationObservation) Recheck(ctx context.Context, at time.Time) error {
	if o.catalog == nil || !o.catalog.verified.Load() || o.catalog.failed.Load() {
		return ErrUnavailable
	}
	return o.read.Recheck(ctx, at)
}
