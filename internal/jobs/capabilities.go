package jobs

import (
	"fmt"
	"github.com/ziad-hsn/cpra/internal/loader/schema"
	"runtime"
	"sync"
)

// Capability describes compiled support, not provider connectivity or certification.
type Capability struct {
	Kind      string `json:"kind"`
	Driver    string `json:"driver"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Written only by package init functions selected by build constraints.
var enabledDriverTags = map[string]bool{}
var capabilityOnce sync.Once
var capabilityInventory []Capability

// Capabilities returns an independent, deterministically ordered inventory.
func Capabilities() []Capability {
	return append([]Capability(nil), capabilities()...)
}

func capabilities() []Capability {
	capabilityOnce.Do(func() { capabilityInventory = buildCapabilities() })
	return capabilityInventory
}

func buildCapabilities() []Capability {
	groups := []struct {
		kind    string
		drivers []string
	}{
		{"check", []string{"http", "tcp", "udp", "dns", "icmp", "grpc", "docker", "tls", "redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka"}},
		{"recovery", []string{"docker", "webhook", "kubernetes", "aws", "systemd"}},
		{"notification", []string{"log", "email", "webhook", "pagerduty", "slack", "telegram", "discord", "opsgenie", "teams", "mattermost", "pushover", "twilio", "datadog", "victorops"}},
	}
	optional := map[string]bool{"redis": true, "postgres": true, "mysql": true, "mongo": true, "rabbitmq": true, "kafka": true, "kubernetes": true, "aws": true, "systemd": true, "teams": true, "twilio": true}
	out := make([]Capability, 0, 33)
	for _, group := range groups {
		for _, driver := range group.drivers {
			c := Capability{Kind: group.kind, Driver: driver, Available: true}
			if optional[driver] && !enabledDriverTags[driver] {
				c.Available = false
				c.Reason = "requires build tag " + driver
			}
			if driver == "systemd" && runtime.GOOS != "linux" {
				c.Available = false
				c.Reason = "only supported on Linux"
			}
			out = append(out, c)
		}
	}
	return out
}

// ValidateDriver checks compilation/platform support without opening a client.
func ValidateDriver(kind, driver string) error {
	for _, capability := range capabilities() {
		if capability.Kind == kind && capability.Driver == driver {
			if !capability.Available {
				return fmt.Errorf("%s driver %s unavailable: %s", kind, driver, capability.Reason)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown %s driver %q", kind, driver)
}

func ValidateCapabilities(m *schema.Monitor) error {
	if err := ValidateDriver("check", m.Pulse.Type); err != nil {
		return err
	}
	if m.Intervention.Action != "" {
		if err := ValidateDriver("recovery", m.Intervention.Action); err != nil {
			return err
		}
	}
	for _, code := range m.Codes {
		if code.Notify != "" {
			if err := ValidateDriver("notification", code.Notify); err != nil {
				return err
			}
		}
	}
	return nil
}
