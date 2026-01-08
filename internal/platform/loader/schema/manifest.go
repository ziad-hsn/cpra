package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
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
	Url     string     `yaml:"url" json:"url" validate:"required,url"`
	Method  string     `yaml:"method" json:"method" validate:"omitempty,oneof=GET POST PUT HEAD DELETE"`
	Headers StringList `yaml:"headers" json:"headers"`
	Retries int        `yaml:"retries" json:"retries" validate:"gte=0,lte=10"`
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
	Host    string `yaml:"host" validate:"required"`
	Port    int    `yaml:"port" validate:"required,gte=1,lte=65535"`
	Retries int    `yaml:"retries" validate:"gte=0,lte=10"`
}

func (c *PulseTCPConfig) Copy() PulseConfig {
	newConfig := new(PulseTCPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseTCPConfig) isPulseConfigs() {}

type PulseICMPConfig struct {
	Host      string `yaml:"host" validate:"required"`
	Privilege bool   `yaml:"ignore_privilege"`
	Count     int    `yaml:"count" validate:"gte=0"`
	Retries   int    `yaml:"retries" validate:"gte=0,lte=10"`
}

func (c *PulseICMPConfig) Copy() PulseConfig {
	newConfig := new(PulseICMPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseICMPConfig) isPulseConfigs() {}

type Pulse struct {
	Config             PulseConfig   `json:"config" validate:"required"`
	Type               string        `yaml:"type" json:"type" validate:"required,oneof=http tcp icmp"`
	Groups             StringList    `yaml:"groups" json:"groups"`
	Interval           time.Duration `yaml:"interval" json:"interval" validate:"gt=0"`
	Timeout            time.Duration `yaml:"timeout" json:"timeout" validate:"gt=0"`
	MaxFailures        int           `yaml:"max_failures" json:"max_failures"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold" json:"unhealthy_threshold" validate:"gte=0"`
	HealthyThreshold   int           `yaml:"healthy_threshold" json:"healthy_threshold" validate:"gte=0"`
}

type rawPulse struct {
	Type               string        `yaml:"type"`
	Groups             StringList    `yaml:"groups"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	Retries            int           `yaml:"retries"`
	MaxFailures        int           `yaml:"max_failures"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
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
		Type:               temp.Type,
		Interval:           temp.Interval,
		Timeout:            temp.Timeout,
		MaxFailures:        temp.MaxFailures,
		UnhealthyThreshold: temp.UnhealthyThreshold,
		HealthyThreshold:   temp.HealthyThreshold,
		Groups:             temp.Groups,
	}
	// Backward compatibility: if UnhealthyThreshold not set, use MaxFailures
	if p.UnhealthyThreshold == 0 && p.MaxFailures > 0 {
		p.UnhealthyThreshold = p.MaxFailures
	}
	config, err := PulseRegistry.New(temp.Type)
	if err != nil {
		return err
	}
	if err := temp.Config.Decode(config); err != nil {
		return err
	}
	p.Config = config
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Pulse (needed for JSON parser)
func (p *Pulse) UnmarshalJSON(data []byte) error {
	var temp struct {
		Type               string          `json:"type"`
		Interval           string          `json:"interval"`
		Timeout            string          `json:"timeout"`
		Config             json.RawMessage `json:"config"`
		MaxFailures        int             `json:"max_failures"`
		UnhealthyThreshold int             `json:"unhealthy_threshold"`
		HealthyThreshold   int             `json:"healthy_threshold"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Parse duration strings
	interval, err := time.ParseDuration(temp.Interval)
	if err != nil {
		return fmt.Errorf("invalid interval duration %q: %w", temp.Interval, err)
	}

	timeout, err := time.ParseDuration(temp.Timeout)
	if err != nil {
		return fmt.Errorf("invalid timeout duration %q: %w", temp.Timeout, err)
	}

	*p = Pulse{
		Type:               temp.Type,
		Interval:           interval,
		Timeout:            timeout,
		MaxFailures:        temp.MaxFailures,
		UnhealthyThreshold: temp.UnhealthyThreshold,
		HealthyThreshold:   temp.HealthyThreshold,
	}
	if p.UnhealthyThreshold == 0 && p.MaxFailures > 0 {
		p.UnhealthyThreshold = p.MaxFailures
	}

	config, err := PulseRegistry.New(temp.Type)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(temp.Config, config); err != nil {
		return err
	}
	p.Config = config
	return nil
}

//// INTERVENTION TYPES

type Intervention struct {
	Target      InterventionTarget `yaml:"target"`
	Action      string             `yaml:"action" validate:"omitempty,oneof=docker"`
	Retries     int                `yaml:"retries" validate:"gte=0,lte=10"`
	MaxFailures int                `yaml:"max_failures" validate:"gte=0"`
}
type rawIntervention struct {
	Action  string `yaml:"action"`
	Retries int    `yaml:"retries"`
}

func (i *Intervention) UnmarshalYAML(value *yaml.Node) error {
	var temp struct {
		rawIntervention `yaml:",inline"`
		Target          yaml.Node `yaml:"target"`
	}
	if err := value.Decode(&temp); err != nil {
		return err
	}
	*i = Intervention{
		Action:  temp.Action,
		Retries: temp.Retries,
	}
	target, err := InterventionRegistry.New(temp.Action)
	if err != nil {
		return err
	}
	if err := temp.Target.Decode(target); err != nil {
		return err
	}
	i.Target = target
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Intervention (needed for JSON parser)
func (i *Intervention) UnmarshalJSON(data []byte) error {
	var temp struct {
		Action  string          `json:"action"`
		Target  json.RawMessage `json:"target"`
		Retries int             `json:"retries"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	*i = Intervention{
		Action:  temp.Action,
		Retries: temp.Retries,
	}

	target, err := InterventionRegistry.New(temp.Action)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(temp.Target, target); err != nil {
		return err
	}
	i.Target = target
	return nil
}

type InterventionTarget interface {
	GetTargetType() string
	Copy() InterventionTarget
}

type InterventionTargetDocker struct {
	Type       string        `yaml:"type" json:"type" validate:"required"`           // restart, stop, start, kill, pause, unpause, scale
	Container  string        `yaml:"container,omitempty" json:"container,omitempty"` // For container ops
	Service    string        `yaml:"service,omitempty" json:"service,omitempty"`     // For swarm scale
	Replicas   uint64        `yaml:"replicas,omitempty" json:"replicas,omitempty"`   // For swarm scale
	Signal     string        `yaml:"signal,omitempty" json:"signal,omitempty"`       // For kill (default: SIGKILL)
	DockerHost string        `yaml:"docker_host,omitempty" json:"docker_host,omitempty"`
	Timeout    time.Duration `yaml:"timeout" json:"timeout" validate:"gte=0"`
}

func (i *InterventionTargetDocker) Copy() InterventionTarget {
	return &InterventionTargetDocker{
		Type:       strings.Clone(i.Type),
		Container:  strings.Clone(i.Container),
		Service:    strings.Clone(i.Service),
		Replicas:   i.Replicas,
		Signal:     strings.Clone(i.Signal),
		DockerHost: strings.Clone(i.DockerHost),
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
	File string `yaml:"file" json:"file"`
}

func (c *CodeNotificationLog) Copy() CodeNotification {
	return &CodeNotificationLog{
		File: strings.Clone(c.File),
	}
}

func (c *CodeNotificationLog) IsCodeNotification() {
}

type CodeNotificationPagerDuty struct {
	IntegrationKey string `yaml:"integration_key,omitempty" json:"integration_key,omitempty"`
	URL            string `yaml:"url,omitempty" json:"url,omitempty"`
	Severity       string `yaml:"severity,omitempty" json:"severity,omitempty"`
}

func (c *CodeNotificationPagerDuty) Copy() CodeNotification {
	return &CodeNotificationPagerDuty{
		IntegrationKey: strings.Clone(c.IntegrationKey),
		URL:            strings.Clone(c.URL),
		Severity:       strings.Clone(c.Severity),
	}
}

func (c *CodeNotificationPagerDuty) IsCodeNotification() {
}

type CodeNotificationSlack struct {
	WebHookURL string `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	WebHook    string `yaml:"hook,omitempty" json:"hook,omitempty"`
	Channel    string `yaml:"channel,omitempty" json:"channel,omitempty"`
}

func (c *CodeNotificationSlack) Copy() CodeNotification {
	return &CodeNotificationSlack{
		WebHookURL: strings.Clone(c.WebHookURL),
		WebHook:    strings.Clone(c.WebHook),
		Channel:    strings.Clone(c.Channel),
	}
}

func (c *CodeNotificationSlack) IsCodeNotification() {
}

type CodeConfig struct {
	Config   CodeNotification `yaml:"config" json:"config" validate:"required"`
	Notify   string           `yaml:"notify" json:"notify" validate:"required,oneof=log slack pagerduty"`
	Dispatch bool             `yaml:"dispatch" json:"dispatch"`
}

type Codes map[string]CodeConfig

type rawCodes struct {
	Dispatch *bool  `yaml:"dispatch" json:"dispatch"` // Pointer to detect omitted field
	Notify   string `yaml:"notify" json:"notify"`
}

func (c *Codes) UnmarshalYAML(value *yaml.Node) error {
	var codes map[string]yaml.Node
	if err := value.Decode(&codes); err != nil {
		return err
	}
	colors := make(map[string]CodeConfig)
	for color, config := range codes {
		var temp struct {
			rawCodes `yaml:",inline"`
			Config   yaml.Node `yaml:"config"`
		}
		if err := config.Decode(&temp); err != nil {
			return err
		}
		// Default dispatch to true if omitted
		dispatch := true
		if temp.Dispatch != nil {
			dispatch = *temp.Dispatch
		}

		cfg, err := NotifyRegistry.New(temp.Notify)
		if err != nil {
			return err
		}
		if err := temp.Config.Decode(cfg); err != nil {
			return err
		}
		colors[color] = CodeConfig{
			Dispatch: dispatch,
			Notify:   temp.Notify,
			Config:   cfg,
		}
	}
	*c = colors
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Codes (needed for JSON parser)
func (c *Codes) UnmarshalJSON(data []byte) error {
	var codes map[string]struct {
		Dispatch *bool           `json:"dispatch"` // Pointer to detect omitted field
		Notify   string          `json:"notify"`
		Config   json.RawMessage `json:"config"`
	}

	if err := json.Unmarshal(data, &codes); err != nil {
		return err
	}

	colors := make(map[string]CodeConfig)
	for color, config := range codes {
		// Default dispatch to true if omitted
		dispatch := true
		if config.Dispatch != nil {
			dispatch = *config.Dispatch
		}

		cfg, err := NotifyRegistry.New(config.Notify)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(config.Config, cfg); err != nil {
			return err
		}
		colors[color] = CodeConfig{
			Dispatch: dispatch,
			Notify:   config.Notify,
			Config:   cfg,
		}
	}
	*c = colors
	return nil
}

type Monitor struct {
	Codes        Codes        `yaml:"codes" json:"codes" validate:"omitempty,dive,keys,required,endkeys,required"`
	Name         string       `yaml:"name" json:"name" validate:"required,min=1,max=255"`
	Intervention Intervention `yaml:"intervention,omitempty" json:"intervention,omitempty" validate:"-"`
	Pulse        Pulse        `yaml:"pulse_check" json:"pulse_check" validate:"required"`
	Enabled      bool         `yaml:"enabled" json:"enabled"`
	ID           string       `yaml:"id,omitempty" json:"id,omitempty" validate:"-"`
	Slug         string       `yaml:"slug,omitempty" json:"slug,omitempty" validate:"-"`
	Metadata     any          `yaml:"metadata,omitempty" json:"metadata,omitempty" validate:"-"`
}

// UnmarshalYAML sets default values for the Monitor struct, specifically for the Enabled field.
func (m *Monitor) UnmarshalYAML(value *yaml.Node) error {
	// Create a temporary struct with a pointer to a bool for 'Enabled'
	// Must include all fields from Monitor to avoid unknown field errors with KnownFields(true)
	type TmpMonitor struct {
		Enabled      *bool        `yaml:"enabled"`
		Codes        Codes        `yaml:"codes"`
		Name         string       `yaml:"name"`
		Intervention Intervention `yaml:"intervention,omitempty"`
		Pulse        Pulse        `yaml:"pulse_check"`
		ID           string       `yaml:"id,omitempty"`
		Slug         string       `yaml:"slug,omitempty"`
		Metadata     any          `yaml:"metadata,omitempty"`
	}

	var tmp TmpMonitor
	if err := value.Decode(&tmp); err != nil {
		return err
	}

	// Assign fields to the actual monitor struct
	m.Name = tmp.Name
	m.Pulse = tmp.Pulse
	m.Intervention = tmp.Intervention
	m.Codes = tmp.Codes
	m.ID = tmp.ID
	m.Slug = tmp.Slug
	m.Metadata = tmp.Metadata

	// Set 'Enabled' to true if it's not specified in the YAML
	if tmp.Enabled == nil {
		m.Enabled = true
	} else {
		m.Enabled = *tmp.Enabled
	}

	return nil
}

type Manifest struct {
	Monitors []Monitor `yaml:"monitors" json:"monitors"`
}
