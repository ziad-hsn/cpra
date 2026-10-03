package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// OperationView freezes bounded live receipts and the retained terminal-history
// boundary. It contains identities and outcomes, never resource payloads.
type OperationView struct {
	view    persistence.OperationView
	catalog *Catalog
}

func (c *Catalog) OperationSnapshot(at time.Time, maxBytes int64) (OperationView, error) {
	return c.OperationSnapshotContext(context.Background(), at, maxBytes)
}

func (c *Catalog) OperationSnapshotContext(ctx context.Context, at time.Time, maxBytes int64) (OperationView, error) {
	if err := c.readyContext(ctx); err != nil {
		return OperationView{}, err
	}
	view, err := c.store.OperationSnapshotContext(ctx, at, maxBytes)
	return OperationView{view: view, catalog: c}, err
}

func (v OperationView) EstimatedBytes() int64 { return v.view.EstimatedBytes() }

func (v OperationView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Operation, string, error) {
	if ctx == nil {
		return nil, "", ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := v.catalog.readyContext(ctx); err != nil {
		return nil, "", err
	}
	if monitorID != "" && !validID(monitorID) {
		return nil, "", ErrValidation
	}
	rows, next, err := v.view.Page(ctx, monitorID, after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]api.Operation, 0, len(rows))
	for _, receipt := range rows {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		items = append(items, operationView(receipt))
	}
	return items, next, nil
}
