//go:build externaljobs

package management

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type catalogOptionsExtensions struct {
	ExternalJobsEnabled bool
}

func (o CatalogOptions) validateDriverTypeExtension(category, driver string) (bool, error) {
	if driver != "external" {
		return false, nil
	}
	if !o.ExternalJobsEnabled {
		return true, errors.Join(ErrValidation, errors.New("external jobs are disabled in runtime configuration"))
	}
	if category != "check" && category != "recovery" && category != "notification" {
		return true, ErrValidation
	}
	return true, nil
}

func (o CatalogOptions) requiresCatalogValidation() bool { return o.ExternalJobsEnabled }

func (o CatalogOptions) validateDriverExtension(category string, driver api.DriverConfig) (bool, error) {
	if driver.Type != "external" {
		return false, nil
	}
	if _, err := o.validateDriverTypeExtension(category, driver.Type); err != nil {
		return true, err
	}
	if err := validateProtectedDriver(category, driver); err != nil {
		return true, errors.Join(ErrValidation, err)
	}
	_, err := externalDriverConfig(driver)
	return true, err
}

func (c *Catalog) validateResolvedDriverExtension(ctx context.Context, category string, driver api.DriverConfig) (bool, error) {
	handled, err := c.options.validateDriverExtension(category, driver)
	if !handled || err != nil {
		return handled, err
	}
	_, err = c.externalJobTypeReference(ctx, category, driver, true, c.store.LookupJobTypeVersion)
	return true, err
}

func (s *Stage) validateResolvedDriverExtension(ctx context.Context, category string, driver api.DriverConfig) (bool, error) {
	handled, err := s.options.validateDriverExtension(category, driver)
	if !handled || err != nil || s.catalog == nil {
		return handled, err
	}
	_, err = s.catalog.externalJobTypeReference(ctx, category, driver, true, s.lookupJobTypeVersion)
	return true, err
}

func (s *Stage) lookupJobTypeVersion(ctx context.Context, id, version string) (persistence.JobTypeVersionView, bool, error) {
	return s.catalog.store.LookupBootstrapJobTypeVersion(ctx, s.info.StageID, id, version)
}

func (o CatalogOptions) extendValidationProfile(profile *collectionValidationProfileData) bool {
	if !o.ExternalJobsEnabled {
		return false
	}
	for _, category := range []string{"check", "recovery", "notification"} {
		profile.Drivers = append(profile.Drivers, collectionProfileDriver{Kind: category, Driver: "external", Available: true, ConfigModel: "ExternalConfig"})
	}
	slices.SortFunc(profile.Drivers, func(a, b collectionProfileDriver) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.Driver, b.Driver)
	})
	return true
}

func (CatalogOptions) contractReferenceCount(resource api.Resource) uint64 {
	var count uint64
	_ = visitDrivers(&resource, func(_ string, driver *api.DriverConfig) error {
		if driver.Type == "external" {
			count++
		}
		return nil
	})
	return count
}

func (s *Stage) prepareDriverReferences(ctx context.Context, resource api.Resource, record *persistence.CatalogRecord) error {
	if s.catalog == nil {
		return ctx.Err() // Offline source validation cannot resolve stored contracts.
	}
	refs, err := s.catalog.lookupJobTypeReferences(ctx, resource, true, s.lookupJobTypeVersion)
	if err != nil {
		return err
	}
	record.JobTypeReferences = refs
	return nil
}

func (s *Stage) authenticateDriverReferences(ctx context.Context, resource api.Resource, record persistence.CatalogRecord) error {
	if s.catalog == nil {
		if len(record.JobTypeReferences) != 0 {
			return ErrStageUnavailable
		}
		return ctx.Err()
	}
	refs, err := s.catalog.lookupJobTypeReferences(ctx, resource, false, s.lookupJobTypeVersion)
	if err != nil {
		return err
	}
	if !slices.Equal(refs, record.JobTypeReferences) {
		return ErrStageUnavailable
	}
	return nil
}
