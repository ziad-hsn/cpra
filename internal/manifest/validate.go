package manifest

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Validate checks a manifest for semantic errors: required fields on each
// monitor and dangling notification-group references. It is the single
// validation entry point shared by the streaming parsers.
func Validate(m *Manifest) error {
	for i := range m.Monitors {
		if err := ValidateMonitor(&m.Monitors[i]); err != nil {
			return err
		}
	}
	return validateGroups(m)
}

// ValidateMonitor checks a single monitor for semantic errors (required
// fields and per-type pulse config requirements).
func ValidateMonitor(m *Monitor) error {
	if _, err := CompileMaintenance(m.Maintenance); err != nil {
		return fmt.Errorf("monitor %q: %w", m.Name, err)
	}
	if m.Name == "" {
		return fmt.Errorf("monitor name is required")
	}
	if m.Pulse.Type == "" {
		return fmt.Errorf("monitor %q: pulse_check.type is required", m.Name)
	}
	if m.Pulse.Interval <= 0 {
		return fmt.Errorf("monitor %q: pulse_check.interval is required", m.Name)
	}
	if m.Pulse.Timeout <= 0 {
		return fmt.Errorf("monitor %q: pulse_check.timeout is required", m.Name)
	}
	if m.Pulse.Config == nil {
		return fmt.Errorf("monitor %q: pulse_check.config is required", m.Name)
	}
	if err := validateDriverSettings("check", m.Pulse.Type, m.Pulse.Config); err != nil {
		return fmt.Errorf("monitor %q pulse_check: %w", m.Name, err)
	}
	if m.Intervention.Action != "" {
		if m.Intervention.Target == nil {
			return fmt.Errorf("monitor %q intervention.target is required", m.Name)
		}
		if err := validateDriverSettings("recovery", m.Intervention.Action, m.Intervention.Target); err != nil {
			return fmt.Errorf("monitor %q intervention: %w", m.Name, err)
		}
	}
	if m.Intervention.Action == "a2a" {
		cfg, ok := m.Intervention.Target.(*InterventionTargetA2A)
		if !ok {
			return fmt.Errorf("A2A intervention target is required")
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	if m.Intervention.MaxAttempts < 0 || m.Intervention.MaxAttempts > math.MaxInt32 {
		return fmt.Errorf("intervention max_attempts is outside the supported range")
	}
	for color, notification := range m.Codes {
		if notification.Notify != "" && notification.NotifyGroup == "" {
			if err := validateDriverSettings("notification", notification.Notify, notification.Config); err != nil {
				return fmt.Errorf("monitor %q codes.%s: %w", m.Name, color, err)
			}
		}
		switch notification.Trigger {
		case "", "state_change":
		case "recovery_failed", "recovery_exhausted":
			if color != "red" || m.Intervention.Action == "" {
				return fmt.Errorf("recovery notification triggers require Code red and an intervention")
			}
		default:
			return fmt.Errorf("unsupported notification trigger")
		}
		if notification.Notify == "a2a" {
			cfg, ok := notification.Config.(*CodeNotificationA2A)
			if !ok {
				return fmt.Errorf("A2A notification configuration is required")
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
		}
	}

	switch cfg := m.Pulse.Config.(type) {
	case *PulseHTTPConfig:
		if cfg.Url == "" {
			return fmt.Errorf("monitor %q: pulse_check.config.url is required for http", m.Name)
		}
	case *PulseTCPConfig:
		if cfg.Host == "" {
			return fmt.Errorf("monitor %q: pulse_check.config.host is required for tcp", m.Name)
		}
		if cfg.Port == 0 {
			return fmt.Errorf("monitor %q: pulse_check.config.port is required for tcp", m.Name)
		}
	case *PulseICMPConfig:
		if cfg.Host == "" {
			return fmt.Errorf("monitor %q: pulse_check.config.host is required for icmp", m.Name)
		}
	case *PulseDNSConfig:
		if cfg.Host == "" {
			return fmt.Errorf("monitor %q: pulse_check.config.host is required for dns", m.Name)
		}
	case *PulseUDPConfig:
		if cfg.Host == "" || cfg.Port == 0 {
			return fmt.Errorf("monitor %q: pulse_check.config.host/port required for udp", m.Name)
		}
	case *PulseGRPCConfig:
		if cfg.Host == "" || cfg.Port == 0 {
			return fmt.Errorf("monitor %q: pulse_check.config.host/port required for grpc", m.Name)
		}
	case *PulseDockerConfig:
		if cfg.Container == "" {
			return fmt.Errorf("monitor %q: pulse_check.config.container is required for docker", m.Name)
		}
	}
	return nil
}

// validateGroups checks that every notify_group reference resolves to a
// defined group whose endpoints all exist.
func validateGroups(m *Manifest) error {
	for _, endpoint := range m.Endpoints {
		if err := validateDriverSettings("notification", endpoint.Type, endpoint.Config); err != nil {
			return fmt.Errorf("notification endpoint: %w", err)
		}
		if endpoint.Type == "a2a" {
			cfg, ok := endpoint.Config.(*CodeNotificationA2A)
			if !ok {
				return fmt.Errorf("A2A endpoint configuration is required")
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
		}
	}
	for _, endpoints := range m.NotificationGroups {
		for _, name := range endpoints {
			if _, ok := m.Endpoints[name]; !ok {
				return fmt.Errorf("notification group references an undefined endpoint")
			}
		}
	}
	for i := range m.Monitors {
		mon := &m.Monitors[i]
		for color, cfg := range mon.Codes {
			if cfg.NotifyGroup == "" {
				continue
			}
			names, ok := m.NotificationGroups[cfg.NotifyGroup]
			if !ok {
				return fmt.Errorf("monitor %q color %q: notification group %q not found", mon.Name, color, cfg.NotifyGroup)
			}
			for _, name := range names {
				if _, ok := m.Endpoints[name]; !ok {
					return fmt.Errorf("monitor %q color %q: endpoint %q not found for group %q", mon.Name, color, name, cfg.NotifyGroup)
				}
			}
		}
	}
	return nil
}

// Normalize into the same inert schema used by the SDK and management API.
// Provider secrets are never included in validation error messages.
func validateDriverSettings(category, kind string, config any) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("driver configuration cannot be encoded")
	}
	if target, ok := config.(*InterventionTargetDocker); ok && target.OperationTimeout == 0 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		delete(fields, "operation_timeout") // Zero selects the manifest default.
		raw, _ = json.Marshal(fields)
	}
	normalized, err := api.NormalizeManifestDriver(category, kind, raw)
	if err != nil {
		return fmt.Errorf("driver configuration has unsupported fields or types")
	}
	return api.ValidateDriverSettings(category, api.DriverConfig{Type: kind, Config: normalized})
}
