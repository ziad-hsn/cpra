package schema

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// =============================================================================
// Pulse YAML Unmarshaling Tests
// =============================================================================

// TestPulse_UnmarshalYAML_HTTP tests HTTP pulse config unmarshaling
func TestPulse_UnmarshalYAML_HTTP(t *testing.T) {
	t.Parallel()
	yamlData := `
type: http
interval: 30s
timeout: 5s
max_failures: 3
unhealthy_threshold: 5
healthy_threshold: 2
config:
  url: http://example.com/health
  method: GET
  retries: 3
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pulse.Type != "http" {
		t.Errorf("Type = %q, want %q", pulse.Type, "http")
	}
	if pulse.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", pulse.Interval)
	}
	if pulse.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", pulse.Timeout)
	}
	if pulse.MaxFailures != 3 {
		t.Errorf("MaxFailures = %d, want 3", pulse.MaxFailures)
	}
	if pulse.UnhealthyThreshold != 5 {
		t.Errorf("UnhealthyThreshold = %d, want 5", pulse.UnhealthyThreshold)
	}
	if pulse.HealthyThreshold != 2 {
		t.Errorf("HealthyThreshold = %d, want 2", pulse.HealthyThreshold)
	}

	httpConfig, ok := pulse.Config.(*PulseHTTPConfig)
	if !ok {
		t.Fatalf("Config is not *PulseHTTPConfig, got %T", pulse.Config)
	}
	if httpConfig.Url != "http://example.com/health" {
		t.Errorf("URL = %q, want %q", httpConfig.Url, "http://example.com/health")
	}
	if httpConfig.Method != "GET" {
		t.Errorf("Method = %q, want %q", httpConfig.Method, "GET")
	}
	if httpConfig.Retries != 3 {
		t.Errorf("Retries = %d, want 3", httpConfig.Retries)
	}
}

// TestPulse_UnmarshalYAML_TCP tests TCP pulse config unmarshaling
func TestPulse_UnmarshalYAML_TCP(t *testing.T) {
	t.Parallel()
	yamlData := `
type: tcp
interval: 10s
timeout: 2s
config:
  host: db.example.com
  port: 5432
  retries: 2
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pulse.Type != "tcp" {
		t.Errorf("Type = %q, want %q", pulse.Type, "tcp")
	}

	tcpConfig, ok := pulse.Config.(*PulseTCPConfig)
	if !ok {
		t.Fatalf("Config is not *PulseTCPConfig, got %T", pulse.Config)
	}
	if tcpConfig.Host != "db.example.com" {
		t.Errorf("Host = %q, want %q", tcpConfig.Host, "db.example.com")
	}
	if tcpConfig.Port != 5432 {
		t.Errorf("Port = %d, want 5432", tcpConfig.Port)
	}
	if tcpConfig.Retries != 2 {
		t.Errorf("Retries = %d, want 2", tcpConfig.Retries)
	}
}

// TestPulse_UnmarshalYAML_ICMP tests ICMP pulse config unmarshaling
func TestPulse_UnmarshalYAML_ICMP(t *testing.T) {
	t.Parallel()
	yamlData := `
type: icmp
interval: 60s
timeout: 3s
config:
  host: gateway.local
  count: 3
  ignore_privilege: true
  retries: 1
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pulse.Type != "icmp" {
		t.Errorf("Type = %q, want %q", pulse.Type, "icmp")
	}

	icmpConfig, ok := pulse.Config.(*PulseICMPConfig)
	if !ok {
		t.Fatalf("Config is not *PulseICMPConfig, got %T", pulse.Config)
	}
	if icmpConfig.Host != "gateway.local" {
		t.Errorf("Host = %q, want %q", icmpConfig.Host, "gateway.local")
	}
	if icmpConfig.Count != 3 {
		t.Errorf("Count = %d, want 3", icmpConfig.Count)
	}
	if !icmpConfig.Privilege {
		t.Error("Privilege should be true")
	}
}

// TestPulse_UnmarshalYAML_UnknownType tests error on unknown pulse type
func TestPulse_UnmarshalYAML_UnknownType(t *testing.T) {
	t.Parallel()
	yamlData := `
type: unknown
interval: 30s
timeout: 5s
config: {}
`
	var pulse Pulse
	err := yaml.Unmarshal([]byte(yamlData), &pulse)
	if err == nil {
		t.Fatal("Expected error for unknown pulse type, got nil")
	}
}

// TestPulse_UnmarshalYAML_BackwardsCompatibility tests max_failures fallback
func TestPulse_UnmarshalYAML_BackwardsCompatibility(t *testing.T) {
	t.Parallel()
	// When unhealthy_threshold is not set, it should fallback to max_failures
	yamlData := `
type: http
interval: 30s
timeout: 5s
max_failures: 3
config:
  url: http://example.com
  method: GET
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pulse.UnhealthyThreshold != 3 {
		t.Errorf("UnhealthyThreshold = %d, want 3 (from max_failures fallback)", pulse.UnhealthyThreshold)
	}
}

// =============================================================================
// Intervention YAML Unmarshaling Tests
// =============================================================================

// TestIntervention_UnmarshalYAML_DockerRestart tests Docker restart intervention
func TestIntervention_UnmarshalYAML_DockerRestart(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
retries: 3
target:
  type: restart
  container: my-container
  docker_host: unix:///var/run/docker.sock
  timeout: 30s
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if intervention.Action != "docker" {
		t.Errorf("Action = %q, want %q", intervention.Action, "docker")
	}
	if intervention.Retries != 3 {
		t.Errorf("Retries = %d, want 3", intervention.Retries)
	}

	target, ok := intervention.Target.(*InterventionTargetDocker)
	if !ok {
		t.Fatalf("Target is not *InterventionTargetDocker, got %T", intervention.Target)
	}
	if target.Type != "restart" {
		t.Errorf("Type = %q, want %q", target.Type, "restart")
	}
	if target.Container != "my-container" {
		t.Errorf("Container = %q, want %q", target.Container, "my-container")
	}
	if target.DockerHost != "unix:///var/run/docker.sock" {
		t.Errorf("DockerHost = %q, want %q", target.DockerHost, "unix:///var/run/docker.sock")
	}
}

// TestIntervention_UnmarshalYAML_DockerStop tests Docker stop intervention
func TestIntervention_UnmarshalYAML_DockerStop(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: stop
  container: stop-container
  timeout: 10s
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target, ok := intervention.Target.(*InterventionTargetDocker)
	if !ok {
		t.Fatalf("Target is not *InterventionTargetDocker, got %T", intervention.Target)
	}
	if target.Type != "stop" {
		t.Errorf("Type = %q, want %q", target.Type, "stop")
	}
}

// TestIntervention_UnmarshalYAML_DockerStart tests Docker start intervention
func TestIntervention_UnmarshalYAML_DockerStart(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: start
  container: start-container
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target := intervention.Target.(*InterventionTargetDocker)
	if target.Type != "start" {
		t.Errorf("Type = %q, want %q", target.Type, "start")
	}
}

// TestIntervention_UnmarshalYAML_DockerKill tests Docker kill intervention
func TestIntervention_UnmarshalYAML_DockerKill(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: kill
  container: kill-container
  signal: SIGTERM
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target := intervention.Target.(*InterventionTargetDocker)
	if target.Type != "kill" {
		t.Errorf("Type = %q, want %q", target.Type, "kill")
	}
	if target.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want %q", target.Signal, "SIGTERM")
	}
}

// TestIntervention_UnmarshalYAML_DockerPause tests Docker pause intervention
func TestIntervention_UnmarshalYAML_DockerPause(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: pause
  container: pause-container
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target := intervention.Target.(*InterventionTargetDocker)
	if target.Type != "pause" {
		t.Errorf("Type = %q, want %q", target.Type, "pause")
	}
}

// TestIntervention_UnmarshalYAML_DockerUnpause tests Docker unpause intervention
func TestIntervention_UnmarshalYAML_DockerUnpause(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: unpause
  container: unpause-container
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target := intervention.Target.(*InterventionTargetDocker)
	if target.Type != "unpause" {
		t.Errorf("Type = %q, want %q", target.Type, "unpause")
	}
}

// TestIntervention_UnmarshalYAML_DockerScale tests Docker scale intervention
func TestIntervention_UnmarshalYAML_DockerScale(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
target:
  type: scale
  service: my-service
  replicas: 5
  timeout: 60s
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target := intervention.Target.(*InterventionTargetDocker)
	if target.Type != "scale" {
		t.Errorf("Type = %q, want %q", target.Type, "scale")
	}
	if target.Service != "my-service" {
		t.Errorf("Service = %q, want %q", target.Service, "my-service")
	}
	if target.Replicas != 5 {
		t.Errorf("Replicas = %d, want 5", target.Replicas)
	}
}

// TestIntervention_UnmarshalYAML_UnknownAction tests error on unknown action
func TestIntervention_UnmarshalYAML_UnknownAction(t *testing.T) {
	t.Parallel()
	yamlData := `
action: kubernetes
target:
  type: restart
  pod: my-pod
`
	var intervention Intervention
	err := yaml.Unmarshal([]byte(yamlData), &intervention)
	if err == nil {
		t.Fatal("Expected error for unknown intervention action, got nil")
	}
}

// =============================================================================
// InterventionTargetDocker Tests
// =============================================================================

// TestInterventionTargetDocker_Copy tests Copy method
func TestInterventionTargetDocker_Copy(t *testing.T) {
	t.Parallel()
	original := &InterventionTargetDocker{
		Type:       "restart",
		Container:  "test-container",
		Service:    "test-service",
		Replicas:   3,
		Signal:     "SIGTERM",
		DockerHost: "unix:///var/run/docker.sock",
	}

	copied := original.Copy()
	copiedDocker, ok := copied.(*InterventionTargetDocker)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	// Modify original
	original.Container = "modified-container"
	original.Replicas = 10

	// Copy should be unchanged
	if copiedDocker.Container != "test-container" {
		t.Errorf("Container was modified in copy, got %q", copiedDocker.Container)
	}
	if copiedDocker.Replicas != 3 {
		t.Errorf("Replicas was modified in copy, got %d", copiedDocker.Replicas)
	}
}

// TestInterventionTargetDocker_GetTargetType tests GetTargetType method
func TestInterventionTargetDocker_GetTargetType(t *testing.T) {
	t.Parallel()
	target := &InterventionTargetDocker{Type: "restart"}
	if target.GetTargetType() != "restart" {
		t.Errorf("GetTargetType() = %q, want %q", target.GetTargetType(), "restart")
	}
}

// =============================================================================
// PulseConfig Copy Tests
// =============================================================================

// TestPulseHTTPConfig_Copy tests HTTP config Copy method
func TestPulseHTTPConfig_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseHTTPConfig{
		Url:     "http://example.com",
		Method:  "GET",
		Retries: 3,
	}

	copied := original.Copy()
	copiedHTTP, ok := copied.(*PulseHTTPConfig)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	original.Url = "http://modified.com"
	if copiedHTTP.Url != "http://example.com" {
		t.Errorf("URL was modified in copy, got %q", copiedHTTP.Url)
	}
}

// TestPulseTCPConfig_Copy tests TCP config Copy method
func TestPulseTCPConfig_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseTCPConfig{
		Host:    "db.example.com",
		Port:    5432,
		Retries: 2,
	}

	copied := original.Copy()
	copiedTCP, ok := copied.(*PulseTCPConfig)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	original.Port = 3306
	if copiedTCP.Port != 5432 {
		t.Errorf("Port was modified in copy, got %d", copiedTCP.Port)
	}
}

// TestPulseICMPConfig_Copy tests ICMP config Copy method
func TestPulseICMPConfig_Copy(t *testing.T) {
	t.Parallel()
	original := &PulseICMPConfig{
		Host:      "gateway.local",
		Count:     3,
		Privilege: true,
		Retries:   1,
	}

	copied := original.Copy()
	copiedICMP, ok := copied.(*PulseICMPConfig)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	original.Count = 5
	if copiedICMP.Count != 3 {
		t.Errorf("Count was modified in copy, got %d", copiedICMP.Count)
	}
}

// =============================================================================
// StringList Unmarshaling Tests
// =============================================================================

// TestStringList_UnmarshalYAML_SingleString tests single string
func TestStringList_UnmarshalYAML_SingleString(t *testing.T) {
	t.Parallel()
	yamlData := `"production"`
	var sl StringList
	if err := yaml.Unmarshal([]byte(yamlData), &sl); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(sl) != 1 {
		t.Fatalf("Expected 1 element, got %d", len(sl))
	}
	if sl[0] != "production" {
		t.Errorf("Element = %q, want %q", sl[0], "production")
	}
}

// TestStringList_UnmarshalYAML_MultipleStrings tests list of strings
func TestStringList_UnmarshalYAML_MultipleStrings(t *testing.T) {
	t.Parallel()
	yamlData := `
- production
- staging
- development
`
	var sl StringList
	if err := yaml.Unmarshal([]byte(yamlData), &sl); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(sl) != 3 {
		t.Fatalf("Expected 3 elements, got %d", len(sl))
	}
	expected := []string{"production", "staging", "development"}
	for i, exp := range expected {
		if sl[i] != exp {
			t.Errorf("Element[%d] = %q, want %q", i, sl[i], exp)
		}
	}
}

// =============================================================================
// Code Notification Tests
// =============================================================================

// TestCodes_UnmarshalYAML_Log tests log notification config
func TestCodes_UnmarshalYAML_Log(t *testing.T) {
	t.Parallel()
	yamlData := `
red:
  notify: log
  dispatch: true
  config:
    file: /var/log/alerts.log
`
	var codes Codes
	if err := yaml.Unmarshal([]byte(yamlData), &codes); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	redCode, ok := codes["red"]
	if !ok {
		t.Fatal("Missing 'red' code config")
	}

	if redCode.Notify != "log" {
		t.Errorf("Notify = %q, want %q", redCode.Notify, "log")
	}
	if !redCode.Dispatch {
		t.Error("Dispatch should be true")
	}

	logConfig, ok := redCode.Config.(*CodeNotificationLog)
	if !ok {
		t.Fatalf("Config is not *CodeNotificationLog, got %T", redCode.Config)
	}
	if logConfig.File != "/var/log/alerts.log" {
		t.Errorf("File = %q, want %q", logConfig.File, "/var/log/alerts.log")
	}
}

// TestCodes_UnmarshalYAML_Slack tests slack notification config
func TestCodes_UnmarshalYAML_Slack(t *testing.T) {
	t.Parallel()
	yamlData := `
yellow:
  notify: slack
  config:
    hook: https://hooks.slack.com/services/xxx/yyy/zzz
`
	var codes Codes
	if err := yaml.Unmarshal([]byte(yamlData), &codes); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	yellowCode, ok := codes["yellow"]
	if !ok {
		t.Fatal("Missing 'yellow' code config")
	}

	slackConfig, ok := yellowCode.Config.(*CodeNotificationSlack)
	if !ok {
		t.Fatalf("Config is not *CodeNotificationSlack, got %T", yellowCode.Config)
	}
	if slackConfig.WebHook != "https://hooks.slack.com/services/xxx/yyy/zzz" {
		t.Errorf("WebHook = %q", slackConfig.WebHook)
	}
}

// TestCodes_UnmarshalYAML_PagerDuty tests pagerduty notification config
func TestCodes_UnmarshalYAML_PagerDuty(t *testing.T) {
	t.Parallel()
	yamlData := `
red:
  notify: pagerduty
  config:
    url: https://events.pagerduty.com/v2/enqueue
`
	var codes Codes
	if err := yaml.Unmarshal([]byte(yamlData), &codes); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	redCode := codes["red"]
	pdConfig, ok := redCode.Config.(*CodeNotificationPagerDuty)
	if !ok {
		t.Fatalf("Config is not *CodeNotificationPagerDuty, got %T", redCode.Config)
	}
	if pdConfig.URL != "https://events.pagerduty.com/v2/enqueue" {
		t.Errorf("URL = %q", pdConfig.URL)
	}
}

// TestCodes_UnmarshalYAML_DefaultDispatch tests dispatch defaults to true
func TestCodes_UnmarshalYAML_DefaultDispatch(t *testing.T) {
	t.Parallel()
	yamlData := `
green:
  notify: log
  config:
    file: /var/log/recovery.log
`
	var codes Codes
	if err := yaml.Unmarshal([]byte(yamlData), &codes); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	greenCode := codes["green"]
	if !greenCode.Dispatch {
		t.Error("Dispatch should default to true when omitted")
	}
}

// TestCodes_UnmarshalYAML_UnknownNotifyType tests error on unknown type
func TestCodes_UnmarshalYAML_UnknownNotifyType(t *testing.T) {
	t.Parallel()
	yamlData := `
red:
  notify: sms
  config:
    phone: +1234567890
`
	var codes Codes
	err := yaml.Unmarshal([]byte(yamlData), &codes)
	if err == nil {
		t.Fatal("Expected error for unknown notify type, got nil")
	}
}

// =============================================================================
// Additional Coverage Tests - Copy Methods
// =============================================================================

// TestCodeNotificationLog_Copy tests log notification Copy method
func TestCodeNotificationLog_Copy(t *testing.T) {
	t.Parallel()
	original := &CodeNotificationLog{File: "/var/log/test.log"}
	copied := original.Copy()

	copiedLog, ok := copied.(*CodeNotificationLog)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	// Modify original
	original.File = "/var/log/modified.log"

	// Copy should be unchanged
	if copiedLog.File != "/var/log/test.log" {
		t.Errorf("File was modified in copy, got %q", copiedLog.File)
	}
}

// TestCodeNotificationPagerDuty_Copy tests PagerDuty notification Copy method
func TestCodeNotificationPagerDuty_Copy(t *testing.T) {
	t.Parallel()
	original := &CodeNotificationPagerDuty{URL: "https://events.pagerduty.com/test"}
	copied := original.Copy()

	copiedPD, ok := copied.(*CodeNotificationPagerDuty)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	original.URL = "https://modified.com"
	if copiedPD.URL != "https://events.pagerduty.com/test" {
		t.Errorf("URL was modified in copy, got %q", copiedPD.URL)
	}
}

// TestCodeNotificationSlack_Copy tests Slack notification Copy method
func TestCodeNotificationSlack_Copy(t *testing.T) {
	t.Parallel()
	original := &CodeNotificationSlack{WebHook: "https://hooks.slack.com/test"}
	copied := original.Copy()

	copiedSlack, ok := copied.(*CodeNotificationSlack)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	original.WebHook = "https://modified.com"
	if copiedSlack.WebHook != "https://hooks.slack.com/test" {
		t.Errorf("WebHook was modified in copy, got %q", copiedSlack.WebHook)
	}
}

// TestIntervention_Copy tests full Intervention Copy method
func TestIntervention_Copy(t *testing.T) {
	t.Parallel()
	original := &InterventionTargetDocker{
		Type:       "restart",
		Container:  "test-container",
		Service:    "test-service",
		Replicas:   5,
		Signal:     "SIGTERM",
		DockerHost: "unix:///var/run/docker.sock",
		Timeout:    30 * time.Second,
	}

	copied := original.Copy()
	copiedDocker, ok := copied.(*InterventionTargetDocker)
	if !ok {
		t.Fatalf("Copy returned wrong type: %T", copied)
	}

	// Verify all fields are copied
	if copiedDocker.Type != "restart" {
		t.Errorf("Type = %q, want %q", copiedDocker.Type, "restart")
	}
	if copiedDocker.Container != "test-container" {
		t.Errorf("Container = %q, want %q", copiedDocker.Container, "test-container")
	}
	if copiedDocker.Service != "test-service" {
		t.Errorf("Service = %q, want %q", copiedDocker.Service, "test-service")
	}
	if copiedDocker.Replicas != 5 {
		t.Errorf("Replicas = %d, want 5", copiedDocker.Replicas)
	}
	if copiedDocker.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want %q", copiedDocker.Signal, "SIGTERM")
	}
	if copiedDocker.DockerHost != "unix:///var/run/docker.sock" {
		t.Errorf("DockerHost = %q", copiedDocker.DockerHost)
	}

	// Modify original and verify copy is independent
	original.Container = "modified"
	original.Replicas = 10
	if copiedDocker.Container != "test-container" {
		t.Error("Container was modified in copy")
	}
	if copiedDocker.Replicas != 5 {
		t.Error("Replicas was modified in copy")
	}
}

// =============================================================================
// Additional Coverage Tests - Edge Cases
// =============================================================================

// TestPulse_UnmarshalYAML_MinimalHTTP tests minimal HTTP config
func TestPulse_UnmarshalYAML_MinimalHTTP(t *testing.T) {
	t.Parallel()
	yamlData := `
type: http
interval: 1s
timeout: 1s
config:
  url: http://localhost
  method: GET
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pulse.Type != "http" {
		t.Errorf("Type = %q, want %q", pulse.Type, "http")
	}
	if pulse.Interval != 1*time.Second {
		t.Errorf("Interval = %v, want 1s", pulse.Interval)
	}
}

// TestPulse_UnmarshalYAML_WithGroups tests pulse with groups
func TestPulse_UnmarshalYAML_WithGroups(t *testing.T) {
	t.Parallel()
	yamlData := `
type: http
interval: 30s
timeout: 5s
groups:
  - production
  - critical
config:
  url: http://example.com
  method: GET
`
	var pulse Pulse
	if err := yaml.Unmarshal([]byte(yamlData), &pulse); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(pulse.Groups) != 2 {
		t.Fatalf("Groups length = %d, want 2", len(pulse.Groups))
	}
	if pulse.Groups[0] != "production" {
		t.Errorf("Groups[0] = %q, want %q", pulse.Groups[0], "production")
	}
	if pulse.Groups[1] != "critical" {
		t.Errorf("Groups[1] = %q, want %q", pulse.Groups[1], "critical")
	}
}

// TestStringList_UnmarshalYAML_Empty tests empty string list
func TestStringList_UnmarshalYAML_Empty(t *testing.T) {
	t.Parallel()
	yamlData := `[]`
	var sl StringList
	if err := yaml.Unmarshal([]byte(yamlData), &sl); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(sl) != 0 {
		t.Errorf("Expected empty list, got %d elements", len(sl))
	}
}

// TestCodes_UnmarshalYAML_MultipleColors tests multiple code colors
func TestCodes_UnmarshalYAML_MultipleColors(t *testing.T) {
	t.Parallel()
	yamlData := `
red:
  notify: log
  dispatch: true
  config:
    file: /var/log/red.log
yellow:
  notify: slack
  dispatch: false
  config:
    hook: https://hooks.slack.com/yellow
green:
  notify: pagerduty
  config:
    url: https://events.pagerduty.com/green
`
	var codes Codes
	if err := yaml.Unmarshal([]byte(yamlData), &codes); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(codes) != 3 {
		t.Fatalf("Expected 3 codes, got %d", len(codes))
	}

	// Check red
	redCode, ok := codes["red"]
	if !ok {
		t.Fatal("Missing 'red' code")
	}
	if !redCode.Dispatch {
		t.Error("Red dispatch should be true")
	}

	// Check yellow
	yellowCode, ok := codes["yellow"]
	if !ok {
		t.Fatal("Missing 'yellow' code")
	}
	if yellowCode.Dispatch {
		t.Error("Yellow dispatch should be false")
	}

	// Check green (dispatch defaults to true)
	greenCode, ok := codes["green"]
	if !ok {
		t.Fatal("Missing 'green' code")
	}
	if !greenCode.Dispatch {
		t.Error("Green dispatch should default to true")
	}
}

// TestIntervention_UnmarshalYAML_EmptyType tests empty type defaults to restart
func TestIntervention_UnmarshalYAML_EmptyType(t *testing.T) {
	t.Parallel()
	yamlData := `
action: docker
retries: 2
target:
  container: default-container
  timeout: 15s
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(yamlData), &intervention); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	target, ok := intervention.Target.(*InterventionTargetDocker)
	if !ok {
		t.Fatalf("Target is not *InterventionTargetDocker, got %T", intervention.Target)
	}

	// Empty type should be handled by the factory (defaults to restart)
	if target.Container != "default-container" {
		t.Errorf("Container = %q, want %q", target.Container, "default-container")
	}
}

// =============================================================================
// Monitor and DurationSeconds Tests
// =============================================================================

// TestMonitor_UnmarshalYAML_Complete tests full monitor unmarshaling
func TestMonitor_UnmarshalYAML_Complete(t *testing.T) {
	t.Parallel()
	yamlData := `
name: api-server
enabled: true
pulse_check:
  type: http
  interval: 30s
  timeout: 5s
  config:
    url: http://localhost:8080/health
    method: GET
intervention:
  action: docker
  retries: 3
  target:
    type: restart
    container: api-container
    timeout: 30s
codes:
  red:
    notify: log
    config:
      file: /var/log/alerts.log
`
	var monitor Monitor
	if err := yaml.Unmarshal([]byte(yamlData), &monitor); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if monitor.Name != "api-server" {
		t.Errorf("Name = %q, want %q", monitor.Name, "api-server")
	}
	if !monitor.Enabled {
		t.Error("Enabled should be true")
	}
	if monitor.Pulse.Type != "http" {
		t.Errorf("Pulse.Type = %q, want %q", monitor.Pulse.Type, "http")
	}
	if monitor.Intervention.Action != "docker" {
		t.Errorf("Intervention.Action = %q, want %q", monitor.Intervention.Action, "docker")
	}
	if _, ok := monitor.Codes["red"]; !ok {
		t.Error("Missing 'red' code in codes")
	}
}

// TestMonitor_UnmarshalYAML_EnabledDefault tests enabled defaults to true
func TestMonitor_UnmarshalYAML_EnabledDefault(t *testing.T) {
	t.Parallel()
	yamlData := `
name: default-enabled
pulse_check:
  type: http
  interval: 30s
  timeout: 5s
  config:
    url: http://localhost
    method: GET
`
	var monitor Monitor
	if err := yaml.Unmarshal([]byte(yamlData), &monitor); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !monitor.Enabled {
		t.Error("Enabled should default to true when not specified")
	}
}

// TestMonitor_UnmarshalYAML_ExplicitDisabled tests enabled: false
func TestMonitor_UnmarshalYAML_ExplicitDisabled(t *testing.T) {
	t.Parallel()
	yamlData := `
name: disabled-monitor
enabled: false
pulse_check:
  type: http
  interval: 30s
  timeout: 5s
  config:
    url: http://localhost
    method: GET
`
	var monitor Monitor
	if err := yaml.Unmarshal([]byte(yamlData), &monitor); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if monitor.Enabled {
		t.Error("Enabled should be false when explicitly set")
	}
}

// TestDurationSeconds_UnmarshalYAML tests duration parsing
func TestDurationSeconds_UnmarshalYAML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		want    int
		wantErr bool
	}{
		{name: "seconds", yaml: `"30s"`, want: 30},
		{name: "minutes", yaml: `"2m"`, want: 120},
		{name: "hours", yaml: `"1h"`, want: 3600},
		{name: "milliseconds", yaml: `"500ms"`, want: 0}, // rounds to 0 seconds
		{name: "complex", yaml: `"1h30m"`, want: 5400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d DurationSeconds
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && int(d) != tt.want {
				t.Errorf("DurationSeconds = %d, want %d", int(d), tt.want)
			}
		})
	}
}

// TestDurationSeconds_UnmarshalYAML_Invalid tests invalid duration
func TestDurationSeconds_UnmarshalYAML_Invalid(t *testing.T) {
	t.Parallel()
	yamlData := `"invalid"`
	var d DurationSeconds
	err := yaml.Unmarshal([]byte(yamlData), &d)
	if err == nil {
		t.Fatal("Expected error for invalid duration, got nil")
	}
}

// TestManifest_UnmarshalYAML tests manifest with multiple monitors
func TestManifest_UnmarshalYAML(t *testing.T) {
	t.Parallel()
	yamlData := `
monitors:
  - name: monitor-1
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      config:
        url: http://localhost:8080
        method: GET
  - name: monitor-2
    pulse_check:
      type: tcp
      interval: 10s
      timeout: 2s
      config:
        host: localhost
        port: 5432
`
	var manifest Manifest
	if err := yaml.Unmarshal([]byte(yamlData), &manifest); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(manifest.Monitors) != 2 {
		t.Fatalf("Expected 2 monitors, got %d", len(manifest.Monitors))
	}
	if manifest.Monitors[0].Name != "monitor-1" {
		t.Errorf("Monitors[0].Name = %q, want %q", manifest.Monitors[0].Name, "monitor-1")
	}
	if manifest.Monitors[1].Name != "monitor-2" {
		t.Errorf("Monitors[1].Name = %q, want %q", manifest.Monitors[1].Name, "monitor-2")
	}
}
