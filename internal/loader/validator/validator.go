package validator

import (
	parser2 "cpra/internal/loader/schema"
	"cpra/loader/parser"
)

type Validator interface {
	ValidateManifest() error
}

type YamlValidator struct {
}

func NewYamlValidator() *YamlValidator {
	return &YamlValidator{}
}

func validateStringSize(s string, min int, max int) error {
	if len(s) < min || len(s) > max {
		return &parser.requiredFieldError{
			Field:  "monitors.name",
			Reason: "must be between 1 and 100 characters",
		}
	}
	return nil
}

func (y *YamlValidator) validatePulseConfig(p parser2.PulseConfig) error {

	switch p.(type) {
	case parser2.PulseHTTPConfig:
		if p.(parser2.PulseHTTPConfig).Url == "" {
			return &parser.requiredFieldError{
				Field:  "monitors.config.url",
				Reason: "cannot be empty for pulse type http",
			}
		}
	}

	return nil
}

func (y *YamlValidator) validatePulse(p *parser2.Pulse) error {
	if p.Type == "" {
		return &parser.requiredFieldError{
			Field:  "monitors.name",
			Reason: "cannot be empty",
		}
	}

	cfg, err := parser2.DecodePulseConfig(p)
	if err != nil {
		return err
	}
	if cfg == nil {
		return &parser.requiredFieldError{
			Field:  "monitors.pulse.config",
			Reason: "cannot be empty",
		}
	}
	err = y.validatePulseConfig(cfg)
	if err != nil {
		return err
	}

	return nil
}

func (y *YamlValidator) validateMonitor(m *parser2.Monitor) error {
	if m.Name == "" {
		return &parser.requiredFieldError{
			Field:  "Monitor.Name",
			Reason: "cannot be empty",
		}

	}
	return nil
}

func (y *YamlValidator) ValidateManifest(m *parser2.Manifest) error {
	for _, monitor := range m.Monitors {
		err := y.validateMonitor(&monitor)
		if err != nil {
			return err
		}
	}
	return nil
}
