package schema

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

const v2YAML = `
version: 2
endpoints:
  ops_email:
    type: email
    config:
      to: ops@example.com
      from: cpra@example.com
  alerts_slack:
    type: slack
    config:
      hook: https://hooks.slack.com/xxx
  status_hook:
    type: webhook
    config:
      url: https://hooks.example.com/cpra
notification_groups:
  oncall: [alerts_slack, status_hook]
  ops: [ops_email]
monitors:
  - name: "API health"
    tags: ["api", "critical"]
    pulse_check:
      type: http
      interval: 1m
      timeout: 10s
      config:
        url: https://api.example.com/health
    codes:
      red:
        dispatch: true
        notify_group: oncall
      yellow:
        dispatch: true
        notify: log
        config:
          file: alerts.log
    maintenance:
      - cron: "0 2 * * *"
        duration: 1h
        timezone: UTC
`

func TestManifestV2WithGroups(t *testing.T) {
	var m Manifest
	if err := yaml.Unmarshal([]byte(v2YAML), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if m.Version != 2 {
		t.Fatalf("expected version 2, got %d", m.Version)
	}
	if len(m.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(m.Endpoints))
	}
	if m.Endpoints["ops_email"].Type != "email" {
		t.Fatalf("expected email endpoint type, got %q", m.Endpoints["ops_email"].Type)
	}
	if _, ok := m.Endpoints["ops_email"].Config.(*CodeNotificationEmail); !ok {
		t.Fatalf("expected email config, got %T", m.Endpoints["ops_email"].Config)
	}
	if _, ok := m.Endpoints["status_hook"].Config.(*CodeNotificationWebhook); !ok {
		t.Fatalf("expected webhook config, got %T", m.Endpoints["status_hook"].Config)
	}

	if len(m.NotificationGroups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(m.NotificationGroups))
	}
	oncall := m.NotificationGroups["oncall"]
	if len(oncall) != 2 || oncall[0] != "alerts_slack" || oncall[1] != "status_hook" {
		t.Fatalf("unexpected oncall group: %v", oncall)
	}

	mon := m.Monitors[0]
	if len(mon.Tags) != 2 || mon.Tags[0] != "api" {
		t.Fatalf("unexpected tags: %v", mon.Tags)
	}
	if len(mon.Maintenance) != 1 || mon.Maintenance[0].Cron != "0 2 * * *" {
		t.Fatalf("unexpected maintenance: %v", mon.Maintenance)
	}

	red := mon.Codes["red"]
	if red.NotifyGroup != "oncall" {
		t.Fatalf("expected red notify_group oncall, got %q", red.NotifyGroup)
	}
	if red.Notify != "" || red.Config != nil {
		t.Fatalf("expected red to have no inline notify/config, got notify=%q config=%v", red.Notify, red.Config)
	}

	yellow := mon.Codes["yellow"]
	if yellow.Notify != "log" || yellow.NotifyGroup != "" {
		t.Fatalf("expected yellow inline log, got notify=%q group=%q", yellow.Notify, yellow.NotifyGroup)
	}
	if _, ok := yellow.Config.(*CodeNotificationLog); !ok {
		t.Fatalf("expected yellow log config, got %T", yellow.Config)
	}
}

func TestManifestV1BackwardCompat(t *testing.T) {
	data := []byte(`
monitors:
  - name: "legacy"
    pulse_check:
      type: http
      interval: 1m
      timeout: 10s
      config:
        url: https://example.com
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          url: pager
`)

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Version != 0 {
		t.Fatalf("expected default version 0, got %d", m.Version)
	}
	red := m.Monitors[0].Codes["red"]
	if red.Notify != "pagerduty" || red.NotifyGroup != "" {
		t.Fatalf("unexpected red config: notify=%q group=%q", red.Notify, red.NotifyGroup)
	}
	if _, ok := red.Config.(*CodeNotificationPagerDuty); !ok {
		t.Fatalf("expected pagerduty config, got %T", red.Config)
	}
}

func TestManifestV2JSON(t *testing.T) {
	data := []byte(`{"version":2,"endpoints":{"ops_email":{"type":"email","config":{"to":"ops@example.com"}}},"notification_groups":{"ops":["ops_email"]},"monitors":[{"name":"m","pulse_check":{"type":"http","interval":"1m","timeout":"10s","config":{"url":"https://x"}},"codes":{"red":{"dispatch":true,"notify_group":"ops"}}}]}`)

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Version != 2 {
		t.Fatalf("expected version 2, got %d", m.Version)
	}
	if m.Monitors[0].Codes["red"].NotifyGroup != "ops" {
		t.Fatalf("expected notify_group ops, got %q", m.Monitors[0].Codes["red"].NotifyGroup)
	}
}
