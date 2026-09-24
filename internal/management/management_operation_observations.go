package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ManagementOperationView is one bounded snapshot of shared ordinary receipts
// and collection observations owned by the authenticated actor. Empty actor
// explicitly selects ordinary receipts only; it never means all collections.
type ManagementOperationView struct {
	view    persistence.ManagementOperationView
	catalog *Catalog
}

func (c *Catalog) ManagementOperationSnapshot(ctx context.Context, actor string, at time.Time, maxBytes int64) (ManagementOperationView, error) {
	// The durable constructor owns context-aware health/lock checks. Ready also
	// reads Store.Status and would wait on owner locks without this context.
	if ctx == nil || !c.verified.Load() || c.failed.Load() {
		return ManagementOperationView{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ManagementOperationView{}, err
	}
	view, err := c.store.ManagementOperationSnapshot(ctx, actor, at, maxBytes)
	if err != nil {
		return ManagementOperationView{}, err
	}
	return ManagementOperationView{view: view, catalog: c}, nil
}

func (v ManagementOperationView) EstimatedBytes() int64 { return v.view.EstimatedBytes() }

func (v ManagementOperationView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Operation, string, error) {
	page, err := v.ReadPage(ctx, monitorID, after, limit)
	return page.Items, page.Next, err
}

// ManagementOperationPage holds projected rows and the original private page
// fence for final admission. It is not retained by an operation-list cursor.
type ManagementOperationPage struct {
	Items   []api.Operation
	Next    string
	page    persistence.ManagementOperationPage
	catalog *Catalog
}

func (p ManagementOperationPage) Recheck(ctx context.Context, at time.Time) error {
	if p.catalog == nil || !p.catalog.verified.Load() || p.catalog.failed.Load() {
		return ErrUnavailable
	}
	return p.page.Recheck(ctx, at)
}

func (v ManagementOperationView) ReadPage(ctx context.Context, monitorID, after string, limit int) (ManagementOperationPage, error) {
	empty := ManagementOperationPage{}
	if ctx == nil || v.catalog == nil || !v.catalog.verified.Load() || v.catalog.failed.Load() {
		return empty, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if monitorID != "" && !validID(monitorID) {
		return empty, ErrValidation
	}
	page, err := v.view.ReadPage(ctx, monitorID, after, limit)
	if err != nil {
		return empty, err
	}
	items := make([]api.Operation, 0, len(page.Rows))
	for _, row := range page.Rows {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if (row.Operation == nil) == (row.Collection == nil) {
			return empty, ErrUnavailable
		}
		if row.Operation != nil {
			items = append(items, operationView(*row.Operation))
		} else {
			items = append(items, collectionOperationView(*row.Collection))
		}
	}
	return ManagementOperationPage{Items: items, Next: page.Next, page: page, catalog: v.catalog}, nil
}
