package parser

import (
	"cpra/internal/loader/schema"
	"gopkg.in/yaml.v3"
)

func isValidKey(key string, section string) error {
	f := ManifestFields[section]
	if _, ok := f[key]; !ok {
		return ErrUnknownField
	}
	return nil
}

func checkMissingRequiredKey(section string, node map[string]yaml.Node) (string, error) {
	f := ManifestFields[section]
	for key, field := range f {
		if field.Required {
			if _, ok := node[key]; !ok {
				return key, ErrRequiredField
			}
		}
	}
	return "", nil
}

func decodePulseConfig(config yaml.Node, pulseType string) (schema.PulseConfig, error) {
	switch pulseType {
	case "http":
		var pulseConfig schema.PulseHTTPConfig
		err := config.Decode(&pulseConfig)
		if err != nil {
			return nil, err
		}
		return &pulseConfig, nil
	case "tcp":
		var pulseConfig schema.PulseTCPConfig
		err := config.Decode(&pulseConfig)
		if err != nil {
			return nil, err
		}
		return &pulseConfig, nil
	case "icmp":
		var pulseConfig schema.PulseICMPConfig
		err := config.Decode(&pulseConfig)
		if err != nil {
			return nil, err
		}
		return &pulseConfig, nil
	default:
		return nil, ErrInvalidPulseType
	}
}
