//go:build !externaljobs

package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type catalogOptionsExtensions struct{}

func (CatalogOptions) validateDriverTypeExtension(string, string) (bool, error) { return false, nil }

func (CatalogOptions) requiresCatalogValidation() bool { return false }

func (CatalogOptions) validateDriverExtension(string, api.DriverConfig) (bool, error) {
	return false, nil
}

func (*Catalog) validateResolvedDriverExtension(context.Context, string, api.DriverConfig) (bool, error) {
	return false, nil
}

func (*Stage) validateResolvedDriverExtension(context.Context, string, api.DriverConfig) (bool, error) {
	return false, nil
}

func (CatalogOptions) extendValidationProfile(*collectionValidationProfileData) bool { return false }

func (CatalogOptions) contractReferenceCount(api.Resource) uint64 { return 0 }

func (*Stage) prepareDriverReferences(ctx context.Context, _ api.Resource, _ *persistence.CatalogRecord) error {
	return ctx.Err()
}

func (*Stage) authenticateDriverReferences(ctx context.Context, _ api.Resource, _ persistence.CatalogRecord) error {
	return ctx.Err()
}
