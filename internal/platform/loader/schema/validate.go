package schema

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

// Use WithRequiredStructEnabled - recommended for new projects (v11 default).
var validate = validator.New(validator.WithRequiredStructEnabled())

// ValidateMonitor validates a single monitor using struct tags.
func ValidateMonitor(m *Monitor) error {
	return validate.Struct(m)
}

// ValidateManifest validates all monitors and wraps errors with monitor name.
func ValidateManifest(manifest *Manifest) error {
	for i := range manifest.Monitors {
		if err := validate.Struct(&manifest.Monitors[i]); err != nil {
			return fmt.Errorf("monitor %q: %w", manifest.Monitors[i].Name, err)
		}
	}
	return nil
}
