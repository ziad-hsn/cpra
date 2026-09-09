package schema

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewPulseTypesDecodeYAML(t *testing.T) {
	data := []byte(`
monitors:
  - name: redis-check
    pulse_check:
      type: redis
      interval: 1m
      timeout: 5s
      config: {addr: localhost:6379, password: secret, db: 1}
  - name: postgres-check
    pulse_check:
      type: postgres
      interval: 1m
      timeout: 5s
      config: {host: db, port: 5432, user: u, password: p, database: app}
  - name: mysql-check
    pulse_check:
      type: mysql
      interval: 1m
      timeout: 5s
      config: {host: db, port: 3306, user: u, database: app}
  - name: mongo-check
    pulse_check:
      type: mongo
      interval: 1m
      timeout: 5s
      config: {uri: mongodb://localhost:27017}
  - name: rabbitmq-check
    pulse_check:
      type: rabbitmq
      interval: 1m
      timeout: 5s
      config: {url: amqp://guest:guest@localhost:5672/}
  - name: kafka-check
    pulse_check:
      type: kafka
      interval: 1m
      timeout: 5s
      config: {brokers: [localhost:9092]}
  - name: tls-check
    pulse_check:
      type: tls
      interval: 1m
      timeout: 5s
      config: {host: example.com, port: 443, warn_days: 30, critical_days: 7}
`)

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Monitors) != 7 {
		t.Fatalf("expected 7 monitors, got %d", len(m.Monitors))
	}
	for i, want := range []string{"redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka", "tls"} {
		if m.Monitors[i].Pulse.Type != want {
			t.Fatalf("monitor %d type = %q, want %q", i, m.Monitors[i].Pulse.Type, want)
		}
	}
	if _, ok := m.Monitors[0].Pulse.Config.(*PulseRedisConfig); !ok {
		t.Fatalf("redis config type = %T", m.Monitors[0].Pulse.Config)
	}
	if tls, ok := m.Monitors[6].Pulse.Config.(*PulseTLSConfig); !ok {
		t.Fatalf("tls config type = %T", m.Monitors[6].Pulse.Config)
	} else if tls.WarnDays != 30 || tls.CriticalDays != 7 {
		t.Fatalf("tls thresholds = %d/%d, want 30/7", tls.WarnDays, tls.CriticalDays)
	}
}

func TestNewInterventionTypesDecodeYAML(t *testing.T) {
	cases := []struct{ action, yaml string }{
		{"kubernetes", `action: kubernetes
retries: 2
target: {namespace: default, kind: deployment, name: foo, replicas: 3}
`},
		{"webhook", `action: webhook
target: {url: https://hooks.example/x, method: POST}
`},
		{"systemd", `action: systemd
target: {unit: myapp.service, mode: replace}
`},
		{"aws", `action: aws
target: {region: us-east-1, operation: reboot-instance, instance_id: i-123}
`},
	}
	for _, tc := range cases {
		var i Intervention
		if err := yaml.Unmarshal([]byte(tc.yaml), &i); err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		if i.Action != tc.action || i.Target == nil {
			t.Fatalf("%s: action=%q target=%v", tc.action, i.Action, i.Target)
		}
	}
	var k Intervention
	_ = yaml.Unmarshal([]byte("action: kubernetes\ntarget: {namespace: n, name: x, replicas: 5}\n"), &k)
	if kt, ok := k.Target.(*InterventionTargetKubernetes); !ok || kt.Replicas == nil || *kt.Replicas != 5 {
		t.Fatalf("kubernetes replicas not parsed: %+v", k.Target)
	}
}

func TestNewNotificationTypesDecodeYAML(t *testing.T) {
	data := []byte(`
red:
  notify: teams
  config: {webhook_url: https://teams.example/x}
yellow:
  notify: mattermost
  config: {webhook_url: https://mm.example/hooks/x}
cyan:
  notify: pushover
  config: {app_token: tok, user_key: uk, priority: 1}
gray:
  notify: twilio
  config: {account_sid: sid, auth_token: at, from: "+1", to: "+2"}
green:
  notify: datadog
  config: {api_key: ak, app_key: appk, tags: [a, b]}
blue:
  notify: victorops
  config: {rest_endpoint_key: rk, routing_key: rt, message_type: CRITICAL}
`)

	var codes Codes
	if err := yaml.Unmarshal(data, &codes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(codes) != 6 {
		t.Fatalf("expected 6 codes, got %d", len(codes))
	}
	if _, ok := codes["red"].Config.(*CodeNotificationTeams); !ok {
		t.Fatalf("teams config type = %T", codes["red"].Config)
	}
	if dd, ok := codes["green"].Config.(*CodeNotificationDatadog); !ok {
		t.Fatalf("datadog config type = %T", codes["green"].Config)
	} else if len(dd.Tags) != 2 {
		t.Fatalf("datadog tags = %v, want 2", dd.Tags)
	}
}

func TestNewTypesJSONDecode(t *testing.T) {
	pulseJSON := `{"type":"redis","interval":"1m","timeout":"5s","config":{"addr":"localhost:6379"}}`
	var p Pulse
	if err := json.Unmarshal([]byte(pulseJSON), &p); err != nil {
		t.Fatalf("pulse json: %v", err)
	}
	if _, ok := p.Config.(*PulseRedisConfig); !ok {
		t.Fatalf("pulse config type = %T", p.Config)
	}

	codeJSON := `{"red":{"notify":"mattermost","config":{"webhook_url":"https://x"}}}`
	var c Codes
	if err := json.Unmarshal([]byte(codeJSON), &c); err != nil {
		t.Fatalf("codes json: %v", err)
	}
	if _, ok := c["red"].Config.(*CodeNotificationMattermost); !ok {
		t.Fatalf("codes config type = %T", c["red"].Config)
	}
}
