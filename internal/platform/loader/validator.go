package loader

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"cpra/internal/platform/loader/schema"
)

// Validation errors
var (
	ErrTimeoutExceedsInterval = errors.New("pulse timeout should not exceed interval")
	ErrInvalidCodeColor       = errors.New("invalid code color")
	ErrInvalidInterval        = errors.New("pulse interval must be positive")
	ErrInvalidTimeout         = errors.New("pulse timeout must be positive")
)

// ValidCodeColors defines the valid color names for code alerts.
var ValidCodeColors = map[string]bool{
	"red": true, "yellow": true, "green": true, "cyan": true,
	"gray": true, "orange": true, "purple": true, "white": true,
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
			&StructTagRule{},
			&IntervalTimeoutRule{},
			&CodeColorRule{},
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

// StructTagRule runs struct-tag based validation using schema helpers.
type StructTagRule struct{}

func (r *StructTagRule) Validate(monitor *schema.Monitor) error {
	return schema.ValidateMonitor(monitor)
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
	// CRITICAL: Enforce timeout <= interval to prevent overlapping jobs
	if monitor.Pulse.Timeout > monitor.Pulse.Interval {
		return fmt.Errorf("%w: timeout=%v, interval=%v",
			ErrTimeoutExceedsInterval, monitor.Pulse.Timeout, monitor.Pulse.Interval)
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
