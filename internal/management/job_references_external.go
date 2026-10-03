//go:build externaljobs

package management

import (
	"context"
	"errors"
	"slices"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func clearJobTypeReferences(record *persistence.CatalogRecord) {
	record.JobTypeReferences = nil
}

func validateDriverCredentialScope(driver api.DriverConfig) error {
	if driver.Type == "external" && driver.CredentialRefs != nil && len(*driver.CredentialRefs) != 0 {
		return errors.Join(ErrValidation, errors.New("external drivers require worker-local credentials"))
	}
	return nil
}

func (c *Catalog) prepareJobTypeReferences(ctx context.Context, resource api.Resource, record *persistence.CatalogRecord) error {
	if record == nil || record.Key.Kind != resource.Kind || record.Key.ID != resource.Metadata.ID {
		return ErrValidation
	}
	refs, err := c.resourceJobTypeReferences(ctx, resource, true)
	if err != nil {
		return err
	}
	record.JobTypeReferences = refs
	return nil
}

func (c *Catalog) authenticateJobTypeReferences(ctx context.Context, resource api.Resource, record persistence.CatalogRecord) error {
	refs, err := c.resourceJobTypeReferences(ctx, resource, false)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		// Availability failures cannot establish corruption of encrypted state.
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		c.fail()
		return ErrUnavailable
	}
	if !slices.Equal(refs, record.JobTypeReferences) {
		c.fail()
		return ErrUnavailable
	}
	return nil
}

func (c *Catalog) resourceJobTypeReferences(ctx context.Context, resource api.Resource, preparing bool) ([]persistence.JobTypeReference, error) {
	return c.lookupJobTypeReferences(ctx, resource, preparing, c.store.LookupJobTypeVersion)
}

type jobTypeVersionLookup func(context.Context, string, string) (persistence.JobTypeVersionView, bool, error)

func (c *Catalog) lookupJobTypeReferences(ctx context.Context, resource api.Resource, preparing bool, lookup jobTypeVersionLookup) ([]persistence.JobTypeReference, error) {
	if ctx == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var refs []persistence.JobTypeReference
	err := visitDrivers(&resource, func(category string, driver *api.DriverConfig) error {
		if driver.Type != "external" {
			return nil
		}
		ref, err := c.externalJobTypeReference(ctx, category, *driver, preparing, lookup)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return persistence.CanonicalJobTypeReferences(refs)
}

func (c *Catalog) externalJobTypeReference(ctx context.Context, category string, driver api.DriverConfig, preparing bool, lookup jobTypeVersionLookup) (persistence.JobTypeReference, error) {
	var ref persistence.JobTypeReference
	config, err := externalDriverConfig(driver)
	if err != nil {
		return ref, err
	}
	selected, ok, err := lookup(ctx, config.JobTypeID, config.Version)
	if err != nil {
		if ctx.Err() != nil {
			return ref, ctx.Err()
		}
		return ref, ErrUnavailable
	}
	if !ok || selected.Version.Category != category || preparing && (selected.CurrentRemoved || selected.CurrentUID != selected.Version.Record.UID) {
		return ref, persistence.ErrCatalogDependency
	}
	retained := selected.Version
	contract, err := c.openJobType(ctx, retained)
	if err != nil {
		return ref, err
	}
	compiled, err := compileJobType(ctx, contract)
	if err != nil {
		return ref, err
	}
	if err := compiled.parameters.validate(ctx, config.Parameters); err != nil {
		if ctx.Err() != nil {
			return ref, ctx.Err()
		}
		return ref, errors.Join(ErrValidation, err)
	}
	return persistence.JobTypeReference{JobTypeID: config.JobTypeID, JobTypeUID: retained.Record.UID,
		Version: config.Version, Revision: retained.Record.Revision, Category: category}, nil
}

func externalDriverConfig(driver api.DriverConfig) (api.ExternalConfig, error) {
	var config api.ExternalConfig
	if err := validateDriverCredentialScope(driver); err != nil {
		return config, err
	}
	if len(driver.Config) > jobValueBytes+4096 || api.StrictDecode(driver.Config, &config) != nil ||
		!jobTypeName(config.JobTypeID) || !jobTypeName(config.Version) ||
		config.CredentialProfile != "" && !jobTypeName(config.CredentialProfile) ||
		len(config.Parameters) == 0 || len(config.Parameters) > jobValueBytes {
		return api.ExternalConfig{}, ErrValidation
	}
	return config, nil
}
