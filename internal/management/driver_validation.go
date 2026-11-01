package management

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ValidateCompiledResource checks the pure driver/platform capability contract.
// Callers can use it for offline input without storage, credentials or clients.
func (o CatalogOptions) ValidateCompiledResource(resource api.Resource) error {
	return visitDrivers(&resource, func(category string, driver *api.DriverConfig) error {
		if err := o.validateDriverType(category, driver.Type); err != nil {
			return api.InvalidField("type", "unsupportedDriver")
		}
		return nil
	})
}

func (o CatalogOptions) validateDriver(category string, driver api.DriverConfig) error {
	if handled, err := o.validateDriverExtension(category, driver); handled {
		return err
	}
	if err := o.validateDriverType(category, driver.Type); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, api.InvalidField("type", "unsupportedDriver"))
	}
	if err := validateProtectedDriver(category, driver); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return o.validateNotificationLog(category, driver)
}

func (o CatalogOptions) validateNotificationLog(category string, driver api.DriverConfig) error {
	if o.NotificationLogEnabled || category != "notification" || driver.Type != "log" {
		return nil
	}
	var config struct {
		File string `json:"file"`
	}
	if json.Unmarshal(driver.Config, &config) != nil || config.File != "" || driver.CredentialRefs != nil && (*driver.CredentialRefs)["file"] != "" {
		return fmt.Errorf("%w: %w", ErrValidation, api.InvalidField("config.file", "logDirectoryRequired"))
	}
	return nil
}

func (o CatalogOptions) validateDriverType(category, driver string) error {
	if handled, err := o.validateDriverTypeExtension(category, driver); handled {
		return err
	}
	return jobs.ValidateDriver(category, driver)
}

func (c *Catalog) validateResolvedDriver(ctx context.Context, category string, driver api.DriverConfig) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handled, err := c.validateResolvedDriverExtension(ctx, category, driver); handled {
		return err
	}
	if err := c.options.validateNotificationLog(category, driver); err != nil {
		return err
	}
	return ValidateResolvedDriver(category, driver)
}
