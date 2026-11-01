package runtimeconfig

import (
	"fmt"
	"regexp"
	"time"
)

type NotificationPacing struct {
	MinimumInterval time.Duration `yaml:"minimum_interval,omitempty"`
	SharedScope     string        `yaml:"shared_scope,omitempty"`
}

var notificationScopeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (n Notifications) Validate() error {
	if len(n.Pacing) > 15 {
		return fmt.Errorf("notifications.pacing exceeds supported drivers")
	}
	for driver, pacing := range n.Pacing {
		if err := (Notifications{Concurrency: map[string]int{driver: 1}}).Validate(); err != nil {
			return err
		}
		if pacing.MinimumInterval < 0 || pacing.MinimumInterval > 24*time.Hour || pacing.SharedScope != "" && !notificationScopeName.MatchString(pacing.SharedScope) {
			return fmt.Errorf("notification pacing requires a nonnegative interval up to 24h and a scope name of at most 64 letters, digits, dots, underscores or hyphens")
		}
	}
	if len(n.Concurrency) > 15 {
		return fmt.Errorf("notifications.concurrency exceeds supported drivers")
	}
	for driver, limit := range n.Concurrency {
		switch driver {
		case "log", "email", "webhook", "slack", "pagerduty", "telegram", "discord", "opsgenie", "teams", "mattermost", "pushover", "twilio", "datadog", "victorops", "a2a":
		default:
			return fmt.Errorf("notifications.concurrency contains an unsupported driver")
		}
		if limit < 1 || limit > 16 {
			return fmt.Errorf("notifications.concurrency values must be between 1 and 16")
		}
	}
	return nil
}
