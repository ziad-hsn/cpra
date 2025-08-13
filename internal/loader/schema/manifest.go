package schema

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"strings"
	"time"
)

//// UTILITY TYPES

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
	var single string
	if err := unmarshal(&single); err == nil {
		*s = []string{single}
		return nil
	}
	var multi []string
	if err := unmarshal(&multi); err == nil {
		*s = multi
		return nil
	}
	return fmt.Errorf("value must be a string or list of strings")
}

//// PULSE TYPES

type PulseConfig interface {
	isPulseConfigs()
	Copy() PulseConfig
}

type PulseHTTPConfig struct {
	Url     string
	Method  string
	Headers StringList
	Retries int
}

func (c *PulseHTTPConfig) Copy() PulseConfig {
	// This was already correct, but for consistency, we'll return a pointer
	// to a new struct.

	newConfig := new(PulseHTTPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseHTTPConfig) isPulseConfigs() {}

type PulseTCPConfig struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	Retries int    `yaml:"retries"`
}

func (c *PulseTCPConfig) Copy() PulseConfig {
	newConfig := new(PulseTCPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseTCPConfig) isPulseConfigs() {}

type PulseICMPConfig struct {
	Host      string `yaml:"host"`
	Privilege bool   `yaml:"ignore_privilege"`
	Count     int    `yaml:"count"`
}

func (c *PulseICMPConfig) Copy() PulseConfig {
	newConfig := new(PulseICMPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseICMPConfig) isPulseConfigs() {}

type Pulse struct {
	Type        string        `yaml:"type"`
	Interval    time.Duration `yaml:"interval"`
	Timeout     time.Duration `yaml:"timeout"`
	MaxFailures int           `yaml:"max_failures"`
	Groups      StringList    `yaml:"groups"`
	Config      PulseConfig
}

type rawPulse struct {
	Type        string        `yaml:"type"`
	Interval    time.Duration `yaml:"interval"`
	Timeout     time.Duration `yaml:"timeout"`
	Retries     int           `yaml:"retries"`
	MaxFailures int           `yaml:"max_failures"`
	Groups      StringList    `yaml:"groups"`
}

func (p *Pulse) UnmarshalYAML(value *yaml.Node) error {
	var temp struct {
		Config   yaml.Node `yaml:"config"`
		rawPulse `yaml:",inline"`
	}
	if err := value.Decode(&temp); err != nil {
		return err
	}
	*p = Pulse{
		Type:        temp.Type,
		Interval:    temp.Interval,
		Timeout:     temp.Timeout,
		MaxFailures: temp.MaxFailures,
		Groups:      temp.Groups,
	}
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

//// INTERVENTION TYPES

type Intervention struct {
	Action      string             `yaml:"action"`
	Retries     int                `yaml:"retries"`
	Target      InterventionTarget `yaml:"target"`
	MaxFailures int                `yaml:"max_failures"`
}
type rawIntervention struct {
	Action  string `yaml:"action"`
	Retries int    `yaml:"retries"`
}

func (i *Intervention) UnmarshalYAML(value *yaml.Node) error {
	var temp struct {
		Target          yaml.Node `yaml:"target"`
		rawIntervention `yaml:",inline"`
	}
	if err := value.Decode(&temp); err != nil {
		return err
	}
	*i = Intervention{
		Action:  temp.Action,
		Retries: temp.Retries,
	}
	switch temp.Action {
	case "docker":
		var t InterventionTargetDocker
		if err := temp.Target.Decode(&t); err != nil {
			return err
		}
		i.Target = &t
	default:
		return fmt.Errorf("unknown intervention type: %q", temp.Action)
	}
	return nil
}

type InterventionTarget interface {
	GetTargetType() string
	Copy() InterventionTarget
}

type InterventionTargetDocker struct {
	Type      string        `yaml:"type"`
	Container string        `yaml:"container"`
	Timeout   time.Duration `yaml:"timeout"`
}

func (i *InterventionTargetDocker) Copy() InterventionTarget {
	return &InterventionTargetDocker{
		Type:      strings.Clone(i.Type),
		Container: strings.Clone(i.Container),
	}
}

func (i *InterventionTargetDocker) GetTargetType() string {
	return i.Type
}

type CodeNotification interface {
	IsCodeNotification()
	Copy() CodeNotification
}

type CodeNotificationLog struct {
	File string `yaml:"file"`
}

func (c *CodeNotificationLog) Copy() CodeNotification {
	return &CodeNotificationLog{
		File: strings.Clone(c.File),
	}
}

func (c *CodeNotificationLog) IsCodeNotification() {
}

type CodeNotificationPagerDuty struct {
	URL string `yaml:"url"`
}

func (c *CodeNotificationPagerDuty) Copy() CodeNotification {
	return &CodeNotificationPagerDuty{
		URL: strings.Clone(c.URL),
	}
}

func (c *CodeNotificationPagerDuty) IsCodeNotification() {
}

type CodeNotificationSlack struct {
	WebHook string `yaml:"hook"`
}

func (c *CodeNotificationSlack) Copy() CodeNotification {
	return &CodeNotificationSlack{
		WebHook: strings.Clone(c.WebHook),
	}
}

func (c *CodeNotificationSlack) IsCodeNotification() {
}

type CodeConfig struct {
	Dispatch bool             `yaml:"dispatch"`
	Notify   string           `yaml:"notify"`
	Config   CodeNotification `yaml:"config"` // or more specific struct if desired
}

type Codes map[string]CodeConfig

type rawCodes struct {
	Dispatch bool   `yaml:"dispatch"`
	Notify   string `yaml:"notify"`
}

func (c *Codes) UnmarshalYAML(value *yaml.Node) error {

	var codes map[string]yaml.Node
	if err := value.Decode(&codes); err != nil {
		return err
	}
	colors := make(map[string]CodeConfig)
	for color, config := range codes {
		var temp struct {
			Config   yaml.Node `yaml:"config"`
			rawCodes `yaml:",inline"`
		}
		if err := config.Decode(&temp); err != nil {
			return err
		}

		switch temp.Notify {
		case "log":
			var t CodeNotificationLog
			if err := temp.Config.Decode(&t); err != nil {
				return err
			}
			colors[color] = CodeConfig{
				Dispatch: temp.Dispatch,
				Notify:   temp.Notify,
				Config:   &t,
			}
		case "slack":
			var t CodeNotificationSlack
			if err := temp.Config.Decode(&t); err != nil {
				return err
			}
			colors[color] = CodeConfig{
				Dispatch: temp.Dispatch,
				Notify:   temp.Notify,
				Config:   &t,
			}
		case "pagerduty":
			var t CodeNotificationPagerDuty
			if err := temp.Config.Decode(&t); err != nil {
				return err
			}
			colors[color] = CodeConfig{
				Dispatch: temp.Dispatch,
				Notify:   temp.Notify,
				Config:   &t,
			}
		default:
			return fmt.Errorf("unknown notificiation type: %q", temp.Notify)

		}
	}
	*c = colors

	return nil
}

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
