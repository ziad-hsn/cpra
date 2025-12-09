package loader

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"cpra/internal/loader/schema"
)

// Validation errors
var (
	ErrEmptyMonitorName       = errors.New("monitor name cannot be empty")
	ErrEmptyPulseType         = errors.New("pulse type cannot be empty")
	ErrInvalidPulseType       = errors.New("invalid pulse type: must be http, tcp, or icmp")
	ErrInvalidURL             = errors.New("invalid URL")
	ErrMissingURL             = errors.New("HTTP pulse requires URL")
	ErrMissingHost            = errors.New("TCP/ICMP pulse requires host")
	ErrInvalidPort            = errors.New("TCP pulse port must be between 1 and 65535")
	ErrInvalidInterval        = errors.New("pulse interval must be positive")
	ErrInvalidTimeout         = errors.New("pulse timeout must be positive")
	ErrTimeoutExceedsInterval = errors.New("pulse timeout should not exceed interval")
	ErrInvalidThreshold       = errors.New("threshold must be positive")
	ErrInvalidCodeColor       = errors.New("invalid code color")
	ErrInvalidNotifyType      = errors.New("invalid notify type")
)

// ValidCodeColors defines the valid color names for code alerts.
var ValidCodeColors = map[string]bool{
	"red": true, "yellow": true, "green": true, "cyan": true,
	"gray": true, "orange": true, "purple": true, "white": true,
}

// ValidPulseTypes defines the valid pulse check types.
var ValidPulseTypes = map[string]bool{
	"http": true, "tcp": true, "icmp": true,
}

// ValidNotifyTypes defines the valid notification types.
var ValidNotifyTypes = map[string]bool{
	"log": true, "slack": true, "pagerduty": true, "email": true, "webhook": true,
}

// ValidationRule is the interface for all validation rules.
type ValidationRule interface {
	Validate(monitor *schema.Monitor) error
}

// MonitorValidator holds a collection of validation rules.
type MonitorValidator struct {
	rules []ValidationRule
}

// NewValidator creates a new monitor validator with default rules.
func NewValidator() *MonitorValidator {
	return &MonitorValidator{
		rules: []ValidationRule{
			&RequiredFieldsRule{},
			&PulseTypeRule{},
			&PulseConfigRule{},
			&IntervalTimeoutRule{},
			&ThresholdRule{},
			&CodeColorRule{},
			&NotifyTypeRule{},
		},
	}
}

// Validate runs all validation rules on a monitor.
func (v *MonitorValidator) Validate(monitor *schema.Monitor) error {
	for _, rule := range v.rules {
		if err := rule.Validate(monitor); err != nil {
			return err
		}
	}
	return nil
}

// RequiredFieldsRule validates required fields are present.
type RequiredFieldsRule struct{}

func (r *RequiredFieldsRule) Validate(monitor *schema.Monitor) error {
	if monitor.Name == "" {
		return ErrEmptyMonitorName
	}
	if monitor.Pulse.Type == "" {
		return ErrEmptyPulseType
	}
	return nil
}

// PulseTypeRule validates the pulse type is valid.
type PulseTypeRule struct{}

func (r *PulseTypeRule) Validate(monitor *schema.Monitor) error {
	if !ValidPulseTypes[strings.ToLower(monitor.Pulse.Type)] {
		return fmt.Errorf("%w: got %q", ErrInvalidPulseType, monitor.Pulse.Type)
	}
	return nil
}

// PulseConfigRule validates pulse configuration based on type.
type PulseConfigRule struct{}

func (r *PulseConfigRule) Validate(monitor *schema.Monitor) error {
	switch strings.ToLower(monitor.Pulse.Type) {
	case "http":
		cfg, ok := monitor.Pulse.Config.(*schema.PulseHTTPConfig)
		if !ok || cfg == nil {
			return ErrMissingURL
		}
		if cfg.Url == "" {
			return ErrMissingURL
		}
		if _, err := url.Parse(cfg.Url); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidURL, err)
		}

	case "tcp":
		cfg, ok := monitor.Pulse.Config.(*schema.PulseTCPConfig)
		if !ok || cfg == nil {
			return ErrMissingHost
		}
		if cfg.Host == "" {
			return ErrMissingHost
		}
		if cfg.Port < 1 || cfg.Port > 65535 {
			return fmt.Errorf("%w: got %d", ErrInvalidPort, cfg.Port)
		}

	case "icmp":
		cfg, ok := monitor.Pulse.Config.(*schema.PulseICMPConfig)
		if !ok || cfg == nil {
			return ErrMissingHost
		}
		if cfg.Host == "" {
			return ErrMissingHost
		}
	}
	return nil
}

// IntervalTimeoutRule validates interval and timeout values.
type IntervalTimeoutRule struct{}

func (r *IntervalTimeoutRule) Validate(monitor *schema.Monitor) error {
	if monitor.Pulse.Interval <= 0 {
		return ErrInvalidInterval
	}
	if monitor.Pulse.Timeout <= 0 {
		return ErrInvalidTimeout
	}
	return nil
}

// ThresholdRule validates threshold values.
type ThresholdRule struct{}

func (r *ThresholdRule) Validate(monitor *schema.Monitor) error {
	if monitor.Pulse.UnhealthyThreshold < 0 {
		return fmt.Errorf("%w: unhealthy_threshold must be >= 0", ErrInvalidThreshold)
	}
	if monitor.Pulse.HealthyThreshold < 0 {
		return fmt.Errorf("%w: healthy_threshold must be >= 0", ErrInvalidThreshold)
	}
	return nil
}

// CodeColorRule validates code color names.
type CodeColorRule struct{}

func (r *CodeColorRule) Validate(monitor *schema.Monitor) error {
	for color := range monitor.Codes {
		if !ValidCodeColors[strings.ToLower(color)] {
			return fmt.Errorf("%w: got %q", ErrInvalidCodeColor, color)
		}
	}
	return nil
}

// NotifyTypeRule validates notification types.
type NotifyTypeRule struct{}

func (r *NotifyTypeRule) Validate(monitor *schema.Monitor) error {
	for _, cfg := range monitor.Codes {
		if cfg.Notify != "" && !ValidNotifyTypes[strings.ToLower(cfg.Notify)] {
			return fmt.Errorf("%w: got %q", ErrInvalidNotifyType, cfg.Notify)
		}
	}
	return nil
}

// MinIntervalRule enforces a minimum pulse interval.
type MinIntervalRule struct {
	MinInterval time.Duration
}

func (r *MinIntervalRule) Validate(monitor *schema.Monitor) error {
	if monitor.Pulse.Interval < r.MinInterval {
		return fmt.Errorf("pulse interval %v is below minimum %v", monitor.Pulse.Interval, r.MinInterval)
	}
	return nil
}
