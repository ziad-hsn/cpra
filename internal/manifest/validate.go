package manifest

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

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
	settings := api.MonitorSpec{Check: api.CheckSpec{Interval: m.Pulse.Interval.String(), Timeout: m.Pulse.Timeout.String(), Retries: api.Pointer(int64(m.Pulse.Retries)), MaxFailures: api.Pointer(int64(m.Pulse.MaxFailures)), HealthyThreshold: api.Pointer(int64(m.Pulse.HealthyThreshold)), UnhealthyThreshold: api.Pointer(int64(m.Pulse.UnhealthyThreshold))}, AutoIgnore: m.AutoIgnore}
	if m.Intervention.Action != "" {
		settings.Recovery = &api.RecoverySpec{Retries: api.Pointer(int64(m.Intervention.Retries)), MaxFailures: api.Pointer(int64(m.Intervention.MaxFailures)), MaxAttempts: api.Pointer(int64(m.Intervention.MaxAttempts))}
	}
	colors := make(map[string]api.AlertRule, len(m.Codes))
	for color := range m.Codes {
		colors[color] = api.AlertRule{}
	}
	settings.Notifications = &colors
	if err := api.ValidateMonitorSettings(settings); err != nil {
		return manifestSettingsError(err)
	}
	if err := api.ValidateAutoIgnore(m.AutoIgnore); err != nil {
		return fmt.Errorf("auto_ignore: %w", err)
	}
	if _, err := CompileMaintenance(m.Maintenance); err != nil {
		return err
	}
	if m.Name == "" {
		return api.InvalidField("name", "required")
	}
	if m.Pulse.Type == "" {
		return api.InvalidField("pulse_check.type", "required")
	}
	if m.Pulse.Interval <= 0 {
		return api.InvalidField("pulse_check.interval", "required")
	}
	if m.Pulse.Timeout <= 0 {
		return api.InvalidField("pulse_check.timeout", "required")
	}
	if m.Pulse.Config == nil {
		return api.InvalidField("pulse_check.config", "required")
	}
	if err := validateDriverSettings("check", m.Pulse.Type, m.Pulse.Config); err != nil {
		return api.PrefixValidationError("pulse_check", err)
	}
	if m.Intervention.Action != "" {
		if m.Intervention.Target == nil {
			return api.InvalidField("intervention.target", "required")
		}
		if err := validateDriverSettings("recovery", m.Intervention.Action, m.Intervention.Target); err != nil {
			return api.PrefixValidationError("intervention", err)
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
	for _, color := range slices.Sorted(maps.Keys(m.Codes)) {
		notification := m.Codes[color]
		if notification.Notify != "" && notification.NotifyGroup == "" {
			if err := validateDriverSettings("notification", notification.Notify, notification.Config); err != nil {
				return api.PrefixValidationError("codes."+color, err)
			}
		}
		switch notification.Trigger {
		case "", "state_change":
		case "recovery_failed", "recovery_exhausted":
			if color != "red" || m.Intervention.Action == "" {
				return api.InvalidField("codes."+color+".trigger", "notificationTrigger")
			}
		default:
			return api.InvalidField("codes."+color+".trigger", "notificationTrigger")
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
			return api.InvalidField("pulse_check.config.url", "required")
		}
	case *PulseTCPConfig:
		if cfg.Host == "" {
			return api.InvalidField("pulse_check.config.host", "required")
		}
		if cfg.Port == 0 {
			return api.InvalidField("pulse_check.config.port", "required")
		}
	case *PulseICMPConfig:
		if cfg.Host == "" {
			return api.InvalidField("pulse_check.config.host", "required")
		}
	case *PulseDNSConfig:
		if cfg.Host == "" {
			return api.InvalidField("pulse_check.config.host", "required")
		}
	case *PulseUDPConfig:
		if cfg.Host == "" || cfg.Port == 0 {
			return api.InvalidField("pulse_check.config", "required")
		}
	case *PulseGRPCConfig:
		if cfg.Host == "" || cfg.Port == 0 {
			return api.InvalidField("pulse_check.config", "required")
		}
	case *PulseDockerConfig:
		if cfg.Container == "" {
			return api.InvalidField("pulse_check.config.container", "required")
		}
	}
	return nil
}

// validateGroups checks that every notify_group reference resolves to a
// defined group whose endpoints all exist.
func validateGroups(m *Manifest) error {
	for index, name := range slices.Sorted(maps.Keys(m.Endpoints)) {
		endpoint := m.Endpoints[name]
		if err := validateDriverSettings("notification", endpoint.Type, endpoint.Config); err != nil {
			return api.PrefixValidationError(fmt.Sprintf("endpoints[%d]", index), err)
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
	for index, group := range slices.Sorted(maps.Keys(m.NotificationGroups)) {
		endpoints := m.NotificationGroups[group]
		if len(endpoints) == 0 {
			return api.InvalidField(fmt.Sprintf("notification_groups[%d]", index), "nonemptyReferences")
		}
		seen := make(map[string]bool, len(endpoints))
		for ordinal, name := range endpoints {
			if seen[name] {
				return api.InvalidField(fmt.Sprintf("notification_groups[%d][%d]", index, ordinal), "duplicateIdentity")
			}
			seen[name] = true
			if _, ok := m.Endpoints[name]; !ok {
				return api.InvalidField(fmt.Sprintf("notification_groups[%d][%d]", index, ordinal), "referenceNotFound")
			}
		}
	}
	for i := range m.Monitors {
		if err := ValidateMonitorReferences(&m.Monitors[i], m.Endpoints, m.NotificationGroups); err != nil {
			return err
		}
	}
	return nil
}

// ValidateMonitorReferences checks one entry against previously validated
// manifest metadata. Streaming callers must apply this to every emitted entry.
func ValidateMonitorReferences(mon *Monitor, endpoints map[string]Endpoint, groups NotificationGroups) error {
	for _, color := range slices.Sorted(maps.Keys(mon.Codes)) {
		cfg := mon.Codes[color]
		if cfg.NotifyGroup == "" {
			continue
		}
		names, ok := groups[cfg.NotifyGroup]
		if !ok || len(names) == 0 {
			return api.InvalidField("codes."+color+".notify_group", "referenceNotFound")
		}
		for _, name := range names {
			if _, ok := endpoints[name]; !ok {
				return api.InvalidField("codes."+color+".notify_group", "referenceNotFound")
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
		return err
	}
	return api.ValidateDriverSettings(category, api.DriverConfig{Type: kind, Config: normalized})
}

// Translate only diagnostic schema names; values and validation rules are shared
// with management resources. File users see the spelling they can edit.
func manifestSettingsError(err error) error {
	detail, ok := api.ValidationFieldError(err)
	if !ok {
		return err
	}
	field := detail.Field
	for _, pair := range [][2]string{{"check", "pulse_check"}, {"recovery", "intervention"}, {"notifications", "codes"}, {"autoIgnore", "auto_ignore"}} {
		if field == pair[0] || strings.HasPrefix(field, pair[0]+".") {
			field = pair[1] + strings.TrimPrefix(field, pair[0])
			break
		}
	}
	for _, pair := range [][2]string{{"maxFailures", "max_failures"}, {"maxAttempts", "max_attempts"}, {"unhealthyThreshold", "unhealthy_threshold"}, {"healthyThreshold", "healthy_threshold"}} {
		field = strings.ReplaceAll(field, pair[0], pair[1])
	}
	return api.InvalidField(field, detail.Reason)
}
