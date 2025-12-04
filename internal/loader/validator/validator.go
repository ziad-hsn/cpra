package validator

import (
	parser2 "cpra/internal/loader/schema"
)

type Validator interface {
	ValidateManifest(m *parser2.Manifest) error
}

type YamlValidator struct {
}

func NewYamlValidator() *YamlValidator {
	return &YamlValidator{}
}

func (y *YamlValidator) ValidateManifest(m *parser2.Manifest) error {
	// TODO: Implement validation logic
	// This was previously commented out and needs to be properly implemented
	// to prevent security vulnerabilities and logic errors.
	return nil
}
