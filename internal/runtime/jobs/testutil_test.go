package jobs

import (
	"testing"
	"time"

	"cpra/internal/platform/loader/schema"
)

// =============================================================================
// Test Helper Functions
// =============================================================================

// newTestHTTPConfig creates a valid HTTP pulse config for tests
func newTestHTTPConfig(url string) schema.Pulse {
	return schema.Pulse{
		Type:               "http",
		Timeout:            5 * time.Second,
		Interval:           30 * time.Second,
		MaxFailures:        3,
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		Config: &schema.PulseHTTPConfig{
			Url:     url,
			Method:  "GET",
			Retries: 2,
		},
	}
}

// newTestTCPConfig creates a valid TCP pulse config for tests
func newTestTCPConfig(host string, port int) schema.Pulse {
	return schema.Pulse{
		Type:               "tcp",
		Timeout:            5 * time.Second,
		Interval:           30 * time.Second,
		MaxFailures:        3,
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		Config: &schema.PulseTCPConfig{
			Host:    host,
			Port:    port,
			Retries: 2,
		},
	}
}

// newTestICMPConfig creates a valid ICMP pulse config for tests
func newTestICMPConfig(host string) schema.Pulse {
	return schema.Pulse{
		Type:               "icmp",
		Timeout:            5 * time.Second,
		Interval:           60 * time.Second,
		MaxFailures:        3,
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		Config: &schema.PulseICMPConfig{
			Host:      host,
			Count:     3,
			Privilege: false,
			Retries:   1,
		},
	}
}

// newTestDockerIntervention creates a valid Docker restart intervention for tests
func newTestDockerIntervention(container string) schema.Intervention {
	return schema.Intervention{
		Action:  "docker",
		Retries: 3,
		Target: &schema.InterventionTargetDocker{
			Type:       "restart",
			Container:  container,
			DockerHost: "unix:///var/run/docker.sock",
			Timeout:    30 * time.Second,
		},
	}
}

// newTestCodeConfig creates a valid code notification config for tests
func newTestCodeConfig(notify string) schema.CodeConfig {
	switch notify {
	case "log":
		return schema.CodeConfig{
			Notify:   "log",
			Dispatch: true,
			Config:   &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}
	case "slack":
		return schema.CodeConfig{
			Notify:   "slack",
			Dispatch: true,
			Config:   &schema.CodeNotificationSlack{WebHook: "https://hooks.slack.com/test"},
		}
	case "pagerduty":
		return schema.CodeConfig{
			Notify:   "pagerduty",
			Dispatch: true,
			Config:   &schema.CodeNotificationPagerDuty{URL: "https://events.pagerduty.com/test"},
		}
	default:
		return schema.CodeConfig{
			Notify:   notify,
			Dispatch: true,
		}
	}
}

// =============================================================================
// Assertion Helpers
// =============================================================================

// assertNoError fails the test if err is not nil
func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// assertError fails the test if err is nil
func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// assertEqual fails the test if got != want
func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// assertNotNil fails the test if v is nil
func assertNotNil(t *testing.T, name string, v any) {
	t.Helper()
	if v == nil {
		t.Fatalf("%s is nil", name)
	}
}
