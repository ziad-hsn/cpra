// Package drivers provides notification driver implementations for code alert jobs.
//
// Each driver implements the NotificationDriver interface and handles the specifics
// of sending alerts to different notification channels (Slack, PagerDuty, Email, Webhook).
//
// The driver abstraction eliminates code duplication by allowing a single CodeNotificationJob
// to work with any notification backend.
package drivers

import (
	"context"
)

// Notification contains all the information needed to send an alert.
type Notification struct {
	Monitor   string
	Color     string
	Status    string
	Severity  string
	Summary   string
	Action    string
	NextSteps string
	Message   string // Pre-formatted human-readable message
}

// NotificationDriver is the interface that all notification backends must implement.
// This enables the Strategy pattern for code alert delivery.
type NotificationDriver interface {
	// Name returns the driver identifier (e.g., "slack", "pagerduty", "email", "webhook").
	Name() string

	// Send delivers the notification through the driver's channel.
	// It should return an error if the delivery fails.
	Send(ctx context.Context, notification *Notification) error
}

// DriverRegistry holds registered notification drivers for lookup by name.
type DriverRegistry struct {
	drivers map[string]NotificationDriver
}

// NewDriverRegistry creates a new driver registry with all available drivers.
func NewDriverRegistry() *DriverRegistry {
	return &DriverRegistry{
		drivers: map[string]NotificationDriver{
			"slack":     &SlackDriver{},
			"pagerduty": &PagerDutyDriver{},
			"email":     &EmailDriver{},
			"webhook":   &WebhookDriver{},
		},
	}
}

// Get returns a driver by name, or nil if not found.
func (r *DriverRegistry) Get(name string) NotificationDriver {
	return r.drivers[name]
}

// Register adds a new driver to the registry.
// This allows extending the system with custom notification drivers.
func (r *DriverRegistry) Register(name string, driver NotificationDriver) {
	r.drivers[name] = driver
}

// Names returns all registered driver names.
func (r *DriverRegistry) Names() []string {
	names := make([]string, 0, len(r.drivers))
	for name := range r.drivers {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the global driver registry used by job factories.
// It is initialized with all built-in drivers.
var DefaultRegistry = NewDriverRegistry()
