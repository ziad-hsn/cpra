package schema

import (
	"testing"
	"time"
)

func TestValidateMonitor(t *testing.T) {
	valid := &Monitor{
		Name: "api",
		Pulse: Pulse{
			Type:     "http",
			Interval: time.Second,
			Timeout:  time.Second,
			Config:   &PulseHTTPConfig{Url: "https://example.com"},
		},
	}
	if err := ValidateMonitor(valid); err != nil {
		t.Fatalf("expected valid monitor, got %v", err)
	}

	if err := ValidateMonitor(&Monitor{Pulse: valid.Pulse}); err == nil {
		t.Fatal("expected error for missing name")
	}

	noCfg := &Monitor{Name: "api", Pulse: Pulse{Type: "http", Interval: time.Second, Timeout: time.Second}}
	if err := ValidateMonitor(noCfg); err == nil {
		t.Fatal("expected error for missing pulse config")
	}

	noURL := &Monitor{Name: "api", Pulse: Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &PulseHTTPConfig{}}}
	if err := ValidateMonitor(noURL); err == nil {
		t.Fatal("expected error for http without url")
	}

	noPort := &Monitor{Name: "db", Pulse: Pulse{Type: "tcp", Interval: time.Second, Timeout: time.Second, Config: &PulseTCPConfig{Host: "127.0.0.1"}}}
	if err := ValidateMonitor(noPort); err == nil {
		t.Fatal("expected error for tcp without port")
	}
}

func TestValidateGroups(t *testing.T) {
	valid := &Manifest{
		Endpoints: map[string]Endpoint{
			"ops_email": {Type: "email", Config: &CodeNotificationEmail{To: "ops@example.com"}},
		},
		NotificationGroups: NotificationGroups{
			"oncall": {"ops_email"},
		},
		Monitors: []Monitor{
			{
				Name:  "api",
				Pulse: Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &PulseHTTPConfig{Url: "https://x"}},
				Codes: Codes{"red": {Dispatch: true, NotifyGroup: "oncall"}},
			},
		},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("expected valid manifest, got %v", err)
	}

	// Dangling group reference.
	bad := &Manifest{
		Monitors: []Monitor{
			{
				Name:  "api",
				Pulse: Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &PulseHTTPConfig{Url: "https://x"}},
				Codes: Codes{"red": {Dispatch: true, NotifyGroup: "missing"}},
			},
		},
	}
	if err := Validate(bad); err == nil {
		t.Fatal("expected error for dangling group")
	}

	// Group referencing a missing endpoint.
	bad2 := &Manifest{
		NotificationGroups: NotificationGroups{"oncall": {"nope"}},
		Monitors: []Monitor{
			{
				Name:  "api",
				Pulse: Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &PulseHTTPConfig{Url: "https://x"}},
				Codes: Codes{"red": {Dispatch: true, NotifyGroup: "oncall"}},
			},
		},
	}
	if err := Validate(bad2); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}
