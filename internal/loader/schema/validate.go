package schema

import "fmt"

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
