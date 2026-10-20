package api

import (
	"math"
	"slices"
	"time"
)

// MinimumCheckInterval matches the default controller's five-millisecond tick.
const MinimumCheckInterval = 5 * time.Millisecond

// ValidateMonitorSettings validates shared scheduling and incident policy without
// constructing jobs, resolving credentials, or reading the server's state.
func ValidateMonitorSettings(spec MonitorSpec) error {
	interval, err := time.ParseDuration(spec.Check.Interval)
	if err != nil || interval < MinimumCheckInterval {
		return InvalidField("check.interval", "minimumInterval")
	}
	if timeout, err := time.ParseDuration(spec.Check.Timeout); err != nil || timeout <= 0 {
		return InvalidField("check.timeout", "positiveDuration")
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{{"check.retries", spec.Check.Retries}, {"check.maxFailures", spec.Check.MaxFailures}, {"check.unhealthyThreshold", spec.Check.UnhealthyThreshold}, {"check.healthyThreshold", spec.Check.HealthyThreshold}} {
		name, value := field.name, field.value
		if value != nil && (*value < 0 || *value > math.MaxInt32) {
			return InvalidField(name, "counterRange")
		}
	}
	if spec.Recovery != nil {
		for _, field := range []struct {
			name  string
			value *int64
		}{{"recovery.retries", spec.Recovery.Retries}, {"recovery.maxFailures", spec.Recovery.MaxFailures}, {"recovery.maxAttempts", spec.Recovery.MaxAttempts}} {
			name, value := field.name, field.value
			if value != nil && (*value < 0 || *value > math.MaxInt32) {
				return InvalidField(name, "counterRange")
			}
		}
		if spec.Recovery.Cooldown != nil {
			if duration, err := time.ParseDuration(*spec.Recovery.Cooldown); err != nil || duration < 0 {
				return InvalidField("recovery.cooldown", "nonnegativeDuration")
			}
		}
	}
	if spec.Notifications != nil {
		for color := range *spec.Notifications {
			if !slices.Contains([]string{"red", "yellow", "cyan", "green", "gray", "blue"}, color) {
				return InvalidField("notifications", "notificationColor")
			}
		}
	}
	return ValidateAutoIgnore(spec.AutoIgnore)
}
