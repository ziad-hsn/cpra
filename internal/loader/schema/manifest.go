package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	Url                string            `yaml:"url" json:"url"`
	Method             string            `yaml:"method" json:"method"`
	Headers            map[string]string `yaml:"headers" json:"headers"`
	Body               string            `yaml:"body" json:"body"`
	ExpectedStatus     []int             `yaml:"expected_status" json:"expected_status"`
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify" json:"insecure_skip_verify"`
	Retries            int               `yaml:"retries" json:"retries"`
}

func (c *PulseHTTPConfig) Copy() PulseConfig {
	newConfig := new(PulseHTTPConfig)
	*newConfig = *c
	if c.Headers != nil {
		newConfig.Headers = make(map[string]string, len(c.Headers))
		for k, v := range c.Headers {
			newConfig.Headers[k] = v
		}
	}
	if c.ExpectedStatus != nil {
		newConfig.ExpectedStatus = append([]int(nil), c.ExpectedStatus...)
	}
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
	Retries   int    `yaml:"retries"`
}

func (c *PulseICMPConfig) Copy() PulseConfig {
	newConfig := new(PulseICMPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseICMPConfig) isPulseConfigs() {}

// PulseDNSConfig checks that a hostname resolves via DNS.
type PulseDNSConfig struct {
	Host    string `yaml:"host" json:"host"`
	Server  string `yaml:"server" json:"server"`
	Retries int    `yaml:"retries" json:"retries"`
}

func (c *PulseDNSConfig) Copy() PulseConfig {
	newConfig := new(PulseDNSConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseDNSConfig) isPulseConfigs() {}

// PulseUDPConfig checks a UDP endpoint by sending a datagram and awaiting a reply.
type PulseUDPConfig struct {
	Host    string `yaml:"host" json:"host"`
	Port    int    `yaml:"port" json:"port"`
	Payload string `yaml:"payload" json:"payload"`
	Retries int    `yaml:"retries" json:"retries"`
}

func (c *PulseUDPConfig) Copy() PulseConfig {
	newConfig := new(PulseUDPConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseUDPConfig) isPulseConfigs() {}

// PulseGRPCConfig checks a gRPC endpoint via the standard health service.
type PulseGRPCConfig struct {
	Host    string `yaml:"host" json:"host"`
	Port    int    `yaml:"port" json:"port"`
	Service string `yaml:"service" json:"service"`
	Retries int    `yaml:"retries" json:"retries"`
}

func (c *PulseGRPCConfig) Copy() PulseConfig {
	newConfig := new(PulseGRPCConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseGRPCConfig) isPulseConfigs() {}

// PulseDockerConfig checks that a Docker container is running.
type PulseDockerConfig struct {
	Container string `yaml:"container" json:"container"`
	Retries   int    `yaml:"retries" json:"retries"`
}

func (c *PulseDockerConfig) Copy() PulseConfig {
	newConfig := new(PulseDockerConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseDockerConfig) isPulseConfigs() {}

type Pulse struct {
	Config             PulseConfig   `json:"config"`
	Type               string        `yaml:"type" json:"type"`
	Groups             StringList    `yaml:"groups" json:"groups"`
	Retries            int           `yaml:"retries" json:"retries"`
	Interval           time.Duration `yaml:"interval" json:"interval"`
	Timeout            time.Duration `yaml:"timeout" json:"timeout"`
	MaxFailures        int           `yaml:"max_failures" json:"max_failures"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold" json:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold" json:"healthy_threshold"`
}

// applyDefaultRetries sets the per-type config's Retries to the top-level
// default when the per-type config did not specify one (Retries == 0).
func applyDefaultRetries(cfg PulseConfig, retries int) {
	if retries <= 0 {
		return
	}
	switch c := cfg.(type) {
	case *PulseHTTPConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseTCPConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseICMPConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseDNSConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseUDPConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseGRPCConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseDockerConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseRedisConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulsePostgresConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseMySQLConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseMongoConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseRabbitMQConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseKafkaConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	case *PulseTLSConfig:
		if c.Retries == 0 {
			c.Retries = retries
		}
	}
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
		rawPulse `yaml:",inline"`
		Config   yaml.Node `yaml:"config"`
	}
	if err := value.Decode(&temp); err != nil {
		return err
	}
	*p = Pulse{
		Type:               temp.Type,
		Groups:             temp.Groups,
		Retries:            temp.Retries,
		Interval:           temp.Interval,
		Timeout:            temp.Timeout,
		MaxFailures:        temp.MaxFailures,
		UnhealthyThreshold: temp.UnhealthyThreshold,
		HealthyThreshold:   temp.HealthyThreshold,
	}
	// Backward compatibility: if UnhealthyThreshold not set, use MaxFailures
	if p.UnhealthyThreshold == 0 && p.MaxFailures > 0 {
		p.UnhealthyThreshold = p.MaxFailures
	}
	switch temp.Type {
	case "http":
		var c = &PulseHTTPConfig{} // FIX: Allocate on the heap
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "tcp":
		var c = &PulseTCPConfig{} // FIX: Allocate on the heap
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "icmp":
		var c = &PulseICMPConfig{} // FIX: Allocate on the heap
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "dns":
		var c = &PulseDNSConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "udp":
		var c = &PulseUDPConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "grpc":
		var c = &PulseGRPCConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "docker":
		var c = &PulseDockerConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "redis":
		var c = &PulseRedisConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "postgres":
		var c = &PulsePostgresConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "mysql":
		var c = &PulseMySQLConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "mongo":
		var c = &PulseMongoConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "rabbitmq":
		var c = &PulseRabbitMQConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "kafka":
		var c = &PulseKafkaConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	case "tls":
		var c = &PulseTLSConfig{}
		if err := temp.Config.Decode(c); err != nil {
			return err
		}
		p.Config = c
	default:
		return fmt.Errorf("unknown pulse type: %q", temp.Type)
	}
	applyDefaultRetries(p.Config, temp.Retries)
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Pulse (needed for JSON parser)
func (p *Pulse) UnmarshalJSON(data []byte) error {
	var temp struct {
		Type               string          `json:"type"`
		Groups             StringList      `json:"groups"`
		Retries            int             `json:"retries"`
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
		Groups:             temp.Groups,
		Retries:            temp.Retries,
		Interval:           interval,
		Timeout:            timeout,
		MaxFailures:        temp.MaxFailures,
		UnhealthyThreshold: temp.UnhealthyThreshold,
		HealthyThreshold:   temp.HealthyThreshold,
	}
	if p.UnhealthyThreshold == 0 && p.MaxFailures > 0 {
		p.UnhealthyThreshold = p.MaxFailures
	}

	switch temp.Type {
	case "http":
		var c = &PulseHTTPConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "tcp":
		var c = &PulseTCPConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "icmp":
		var c = &PulseICMPConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "dns":
		var c = &PulseDNSConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "udp":
		var c = &PulseUDPConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "grpc":
		var c = &PulseGRPCConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "docker":
		var c = &PulseDockerConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "redis":
		var c = &PulseRedisConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "postgres":
		var c = &PulsePostgresConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "mysql":
		var c = &PulseMySQLConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "mongo":
		var c = &PulseMongoConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "rabbitmq":
		var c = &PulseRabbitMQConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "kafka":
		var c = &PulseKafkaConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	case "tls":
		var c = &PulseTLSConfig{}
		if err := json.Unmarshal(temp.Config, c); err != nil {
			return err
		}
		p.Config = c
	default:
		return fmt.Errorf("unknown pulse type: %q", temp.Type)
	}
	applyDefaultRetries(p.Config, temp.Retries)
	return nil
}

//// INTERVENTION TYPES

type Intervention struct {
	Target      InterventionTarget `yaml:"target" json:"target"`
	Action      string             `yaml:"action" json:"action"`
	Retries     int                `yaml:"retries" json:"retries"`
	MaxFailures int                `yaml:"max_failures" json:"max_failures"`
}
type rawIntervention struct {
	MaxFailures int    `yaml:"max_failures"`
	Action      string `yaml:"action"`
	Retries     int    `yaml:"retries"`
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
		Action:      temp.Action,
		Retries:     temp.Retries,
		MaxFailures: temp.MaxFailures,
	}
	switch temp.Action {
	case "docker":
		var t = &InterventionTargetDocker{} // FIX: Allocate on the heap
		if err := temp.Target.Decode(t); err != nil {
			return err
		}
		i.Target = t
	case "kubernetes":
		var t = &InterventionTargetKubernetes{}
		if err := temp.Target.Decode(t); err != nil {
			return err
		}
		i.Target = t
	case "webhook":
		var t = &InterventionTargetWebhook{}
		if err := temp.Target.Decode(t); err != nil {
			return err
		}
		i.Target = t
	case "systemd":
		var t = &InterventionTargetSystemd{}
		if err := temp.Target.Decode(t); err != nil {
			return err
		}
		i.Target = t
	case "aws":
		var t = &InterventionTargetAWS{}
		if err := temp.Target.Decode(t); err != nil {
			return err
		}
		i.Target = t
	default:
		return fmt.Errorf("unknown intervention type: %q", temp.Action)
	}
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Intervention (needed for JSON parser)
func (i *Intervention) UnmarshalJSON(data []byte) error {
	var temp struct {
		Action      string          `json:"action"`
		MaxFailures int             `json:"max_failures"`
		Target      json.RawMessage `json:"target"`
		Retries     int             `json:"retries"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	*i = Intervention{
		Action:      temp.Action,
		Retries:     temp.Retries,
		MaxFailures: temp.MaxFailures,
	}

	switch temp.Action {
	case "docker":
		var t = &InterventionTargetDocker{}
		if err := decodeInterventionTargetJSON(temp.Target, t); err != nil {
			return err
		}
		i.Target = t
	case "kubernetes":
		var t = &InterventionTargetKubernetes{}
		if err := decodeInterventionTargetJSON(temp.Target, t); err != nil {
			return err
		}
		i.Target = t
	case "webhook":
		var t = &InterventionTargetWebhook{}
		if err := decodeInterventionTargetJSON(temp.Target, t); err != nil {
			return err
		}
		i.Target = t
	case "systemd":
		var t = &InterventionTargetSystemd{}
		if err := decodeInterventionTargetJSON(temp.Target, t); err != nil {
			return err
		}
		i.Target = t
	case "aws":
		var t = &InterventionTargetAWS{}
		if err := decodeInterventionTargetJSON(temp.Target, t); err != nil {
			return err
		}
		i.Target = t
	default:
		return fmt.Errorf("unknown intervention type: %q", temp.Action)
	}
	return nil
}

// JSON recovery durations accept the same strings as YAML, while retaining
// numeric nanoseconds for callers of the existing JSON representation.
func decodeInterventionTargetJSON(data []byte, target any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw := fields["timeout"]; len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		duration, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("invalid recovery timeout: %w", err)
		}
		fields["timeout"], err = json.Marshal(int64(duration))
		if err != nil {
			return err
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

type InterventionTarget interface {
	GetTargetType() string
	Copy() InterventionTarget
}

type InterventionTargetDocker struct {
	Type      string        `yaml:"type" json:"type"`
	Container string        `yaml:"container" json:"container"`
	Timeout   time.Duration `yaml:"timeout" json:"timeout"`
}

func (i *InterventionTargetDocker) Copy() InterventionTarget {
	return &InterventionTargetDocker{
		Type:      strings.Clone(i.Type),
		Container: strings.Clone(i.Container),
		Timeout:   i.Timeout,
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
	RoutingKey string `yaml:"routing_key" json:"routing_key"`
	URL        string `yaml:"url" json:"url"`
}

func (c *CodeNotificationPagerDuty) Copy() CodeNotification {
	return &CodeNotificationPagerDuty{
		URL:        strings.Clone(c.URL),
		RoutingKey: strings.Clone(c.RoutingKey),
	}
}

func (c *CodeNotificationPagerDuty) IsCodeNotification() {
}

type CodeNotificationSlack struct {
	WebHook string `yaml:"hook" json:"hook"`
}

func (c *CodeNotificationSlack) Copy() CodeNotification {
	return &CodeNotificationSlack{
		WebHook: strings.Clone(c.WebHook),
	}
}

func (c *CodeNotificationSlack) IsCodeNotification() {
}

// CodeNotificationTelegram delivers alerts to a Telegram chat via the Bot API.
type CodeNotificationTelegram struct {
	BotToken string `yaml:"bot_token" json:"bot_token"`
	ChatID   string `yaml:"chat_id" json:"chat_id"`
}

func (c *CodeNotificationTelegram) Copy() CodeNotification {
	return &CodeNotificationTelegram{
		BotToken: strings.Clone(c.BotToken),
		ChatID:   strings.Clone(c.ChatID),
	}
}

func (c *CodeNotificationTelegram) IsCodeNotification() {
}

// CodeNotificationDiscord delivers alerts to a Discord channel via a webhook.
type CodeNotificationDiscord struct {
	WebhookURL string `yaml:"webhook_url" json:"webhook_url"`
}

func (c *CodeNotificationDiscord) Copy() CodeNotification {
	return &CodeNotificationDiscord{
		WebhookURL: strings.Clone(c.WebhookURL),
	}
}

func (c *CodeNotificationDiscord) IsCodeNotification() {
}

// CodeNotificationOpsgenie delivers alerts to Opsgenie via the v2 alerts API.
type CodeNotificationOpsgenie struct {
	APIKey string `yaml:"api_key" json:"api_key"`
	URL    string `yaml:"url" json:"url"`
}

func (c *CodeNotificationOpsgenie) Copy() CodeNotification {
	return &CodeNotificationOpsgenie{
		APIKey: strings.Clone(c.APIKey),
		URL:    strings.Clone(c.URL),
	}
}

func (c *CodeNotificationOpsgenie) IsCodeNotification() {
}

type CodeConfig struct {
	Config      CodeNotification `yaml:"config" json:"config"`
	Notify      string           `yaml:"notify" json:"notify"`
	NotifyGroup string           `yaml:"notify_group" json:"notify_group"`
	Dispatch    bool             `yaml:"dispatch" json:"dispatch"`
}

type Codes map[string]CodeConfig

type rawCodes struct {
	Dispatch    *bool  `yaml:"dispatch" json:"dispatch"` // Pointer to detect omitted field
	Notify      string `yaml:"notify" json:"notify"`
	NotifyGroup string `yaml:"notify_group" json:"notify_group"`
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

		cc := CodeConfig{
			Dispatch:    dispatch,
			Notify:      temp.Notify,
			NotifyGroup: temp.NotifyGroup,
		}
		if temp.NotifyGroup == "" {
			cfg, err := decodeCodeNotificationYAML(temp.Notify, temp.Config)
			if err != nil {
				return err
			}
			cc.Config = cfg
		}
		colors[color] = cc
	}
	*c = colors
	return nil
}

// UnmarshalJSON handles JSON unmarshaling for Codes (needed for JSON parser)
func (c *Codes) UnmarshalJSON(data []byte) error {
	var codes map[string]struct {
		Dispatch    *bool           `json:"dispatch"` // Pointer to detect omitted field
		Notify      string          `json:"notify"`
		NotifyGroup string          `json:"notify_group"`
		Config      json.RawMessage `json:"config"`
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

		cc := CodeConfig{
			Dispatch:    dispatch,
			Notify:      config.Notify,
			NotifyGroup: config.NotifyGroup,
		}
		if config.NotifyGroup == "" {
			cfg, err := decodeCodeNotificationJSON(config.Notify, config.Config)
			if err != nil {
				return err
			}
			cc.Config = cfg
		}
		colors[color] = cc
	}
	*c = colors
	return nil
}

type Monitor struct {
	ID           string              `yaml:"id,omitempty" json:"id,omitempty"`
	Pulse        Pulse               `yaml:"pulse_check" json:"pulse_check"`
	Codes        Codes               `yaml:"codes" json:"codes"`
	Intervention Intervention        `yaml:"intervention,omitempty" json:"intervention,omitempty"`
	Name         string              `yaml:"name" json:"name"`
	Enabled      bool                `yaml:"enabled" json:"enabled"`
	Tags         []string            `yaml:"tags" json:"tags"`
	Maintenance  []MaintenanceWindow `yaml:"maintenance" json:"maintenance"`
}

// UnmarshalYAML sets default values for the Monitor struct, specifically for the Enabled field.
func (m *Monitor) UnmarshalYAML(value *yaml.Node) error {
	// Create a temporary struct with a pointer to a bool for 'Enabled'
	type TmpMonitor struct {
		ID           string              `yaml:"id" json:"id"`
		Pulse        Pulse               `yaml:"pulse_check"`
		Enabled      *bool               `yaml:"enabled"`
		Codes        Codes               `yaml:"codes"`
		Intervention Intervention        `yaml:"intervention,omitempty"`
		Name         string              `yaml:"name"`
		Tags         []string            `yaml:"tags"`
		Maintenance  []MaintenanceWindow `yaml:"maintenance"`
	}

	var tmp TmpMonitor
	if err := value.Decode(&tmp); err != nil {
		return err
	}

	// Assign fields to the actual monitor struct
	m.ID = tmp.ID
	m.Name = tmp.Name
	m.Pulse = tmp.Pulse
	m.Intervention = tmp.Intervention
	m.Codes = tmp.Codes
	m.Tags = tmp.Tags
	m.Maintenance = tmp.Maintenance

	// Set 'Enabled' to true if it's not specified in the YAML
	if tmp.Enabled == nil {
		m.Enabled = true
	} else {
		m.Enabled = *tmp.Enabled
	}

	return nil
}

// UnmarshalJSON mirrors UnmarshalYAML's defaulting so JSON-loaded monitors
// also default Enabled to true when the field is omitted (previously a JSON
// monitor with no "enabled" key was silently disabled).
func (m *Monitor) UnmarshalJSON(data []byte) error {
	type TmpMonitor struct {
		ID           string              `yaml:"id" json:"id"`
		Pulse        Pulse               `json:"pulse_check"`
		Enabled      *bool               `json:"enabled"`
		Codes        Codes               `json:"codes"`
		Intervention Intervention        `json:"intervention,omitempty"`
		Name         string              `json:"name"`
		Tags         []string            `json:"tags"`
		Maintenance  []MaintenanceWindow `json:"maintenance"`
	}

	var tmp TmpMonitor
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	m.ID = tmp.ID
	m.Name = tmp.Name
	m.Pulse = tmp.Pulse
	m.Intervention = tmp.Intervention
	m.Codes = tmp.Codes
	m.Tags = tmp.Tags
	m.Maintenance = tmp.Maintenance

	if tmp.Enabled == nil {
		m.Enabled = true
	} else {
		m.Enabled = *tmp.Enabled
	}

	return nil
}

type Manifest struct {
	Version            int                 `yaml:"version" json:"version"`
	Monitors           []Monitor           `yaml:"monitors" json:"monitors"`
	Endpoints          map[string]Endpoint `yaml:"endpoints" json:"endpoints"`
	NotificationGroups NotificationGroups  `yaml:"notification_groups" json:"notification_groups"`
}
