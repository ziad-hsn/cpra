package httpauth

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// ParsePolicy decodes one bounded named-principal policy document. Callers own
// protected-file reading and clearing the input. It accepts verifiers only;
// transport and legacy credentials are configured separately.
func ParsePolicy(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return Config{}, errors.New("management policy exceeds its document limit")
	}
	var input struct {
		Principals []Principal `yaml:"principals"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&input); err != nil {
		return Config{}, errors.New("invalid management policy document")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(input.Principals) == 0 {
		return Config{}, errors.New("management policy must contain one document with named principals")
	}
	config := Config{Principals: input.Principals}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}
