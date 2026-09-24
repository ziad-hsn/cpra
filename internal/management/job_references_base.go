//go:build !externaljobs

package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func clearJobTypeReferences(*persistence.CatalogRecord) {}

func validateDriverCredentialScope(api.DriverConfig) error { return nil }

func (*Catalog) prepareJobTypeReferences(ctx context.Context, _ api.Resource, _ *persistence.CatalogRecord) error {
	return ctx.Err()
}

func (*Catalog) authenticateJobTypeReferences(ctx context.Context, _ api.Resource, _ persistence.CatalogRecord) error {
	return ctx.Err()
}
