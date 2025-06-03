package schema

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"time"
)

type DurationSeconds int

func (d *DurationSeconds) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw string
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("invalid duration %q", raw)
	}
	p, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = DurationSeconds(int(p.Seconds()))
	return nil
}

type StringList []string

func (s *StringList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try unmarshaling as a single string
	var single string
	if err := unmarshal(&single); err == nil {
		*s = []string{single}
		return nil
	}

	// Try unmarshaling as a slice of strings
	var multi []string
	if err := unmarshal(&multi); err == nil {
		*s = multi
		return nil
	}

	return fmt.Errorf("value must be a string or list of strings")
}

type Pulse struct {
	Type        string          `yaml:"type"`
	Interval    DurationSeconds `yaml:"interval"`
	Timeout     time.Duration   `yaml:"timeout"`
	MaxFailures int             `yaml:"max_failures"`
	Groups      StringList      `yaml:"groups"`

	Config PulseConfig
}

type rawPulse struct {
	Type        string          `yaml:"type"`
	Interval    DurationSeconds `yaml:"interval"`
	Timeout     time.Duration   `yaml:"timeout"`
	Retries     int             `yaml:"retries"`
	MaxFailures int             `yaml:"max_failures"`
	Groups      StringList      `yaml:"groups"`
}

func (p *Pulse) UnmarshalYAML(value *yaml.Node) error {
	var temp struct {
		Config   yaml.Node `yaml:"config"`
		rawPulse `yaml:",inline"`
	}

	if err := value.Decode(&temp); err != nil {
		return err
	}

	// Copy decoded fields to real Pulse
	*p = Pulse{
		Type:        temp.Type,
		Interval:    temp.Interval,
		Timeout:     temp.Timeout,
		MaxFailures: temp.MaxFailures,
		Groups:      temp.Groups,
	}

	// Decode config polymorphically
	switch temp.Type {
	case "http":
		var c PulseHTTPConfig
		if err := temp.Config.Decode(&c); err != nil {
			return err
		}
		p.Config = &c
	case "tcp":
		var c PulseTCPConfig
		if err := temp.Config.Decode(&c); err != nil {
			return err
		}
		p.Config = &c
	case "icmp":
		var c PulseICMPConfig
		if err := temp.Config.Decode(&c); err != nil {
			return err
		}
		p.Config = &c
	default:
		return fmt.Errorf("unknown pulse type: %q", temp.Type)
	}

	return nil
}

type PulseConfig interface {
	isPulseConfigs()
}

type PulseHTTPConfig struct {
	Url     string                 `yaml:"url"`
	Method  string                 `json:"method"`
	Headers StringList             `json:"headers"`
	Auth    map[string]interface{} `json:"auth"`
	Retries int                    `yaml:"retries"`
}

func (H PulseHTTPConfig) isPulseConfigs() {}

type PulseTCPConfig struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	Retries int    `yaml:"retries"`
}

func (H PulseTCPConfig) isPulseConfigs() {}

type PulseICMPConfig struct {
	Host      string `yaml:"host"`
	Privilege bool   `yaml:"ignore_privilege"`
	Count     int    `yaml:"count"`
}

func (H PulseICMPConfig) isPulseConfigs() {}

type Intervention struct {
	Action  string    `yaml:"action"`
	Retries int       `yaml:"retries"`
	Target  yaml.Node `yaml:"target"`
}

type InterventionTarget interface {
	GetTargetType() string
}

type InterventionTargetDocker struct {
	Type      string `yaml:"type"`
	Container string `yaml:"container"`
}

func (I InterventionTargetDocker) isInterventionTarget() {
}

type CodeColor yaml.Node

type Codes map[string]CodeColor

type Monitor struct {
	Name         string       `yaml:"name"`
	Enabled      bool         `yaml:"enabled"`
	Pulse        Pulse        `yaml:"pulse_check"`
	Intervention Intervention `yaml:"intervention,omitempty"`
	Codes        Codes        `yaml:"codes"`
}

type Manifest struct {
	Monitors []Monitor `yaml:"monitors"`
}
