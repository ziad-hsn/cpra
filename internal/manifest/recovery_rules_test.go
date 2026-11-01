package manifest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func TestInterventionRulesYAMLAndJSON(t *testing.T) {
	input := `taxonomy_version: "1"
max_attempts: 2
rules:
  - id: no-auth-restart
    match: {codes: [unexpected_http_status], http_statuses: [401, 403]}
    none: true
  - id: restart-refused
    match: {codes: [network.connection_refused], phases: [connect]}
    action: docker
    target: {container: designated, timeout: 10s, operation_timeout: 60s}
`
	var intervention Intervention
	if err := yaml.Unmarshal([]byte(input), &intervention); err != nil {
		t.Fatal(err)
	}
	policy, err := intervention.RecoveryRules()
	if err != nil {
		t.Fatal(err)
	}
	check := api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.invalid/health"}`)}
	compiled, _, err := api.CompileRecoveryRules(check, policy)
	if err != nil {
		t.Fatal(err)
	}
	observation, _ := api.CanonicalFailure("1", "network.connection_refused", "connect")
	if decision := compiled.Match(observation); decision.RuleID != "restart-refused" || !decision.Intervention {
		t.Fatalf("match: %+v", decision)
	}
	data, err := json.Marshal(intervention)
	if err != nil {
		t.Fatal(err)
	}
	var copy Intervention
	if err = json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	if copy.MaxAttempts != 2 || copy.Rules[1].Target.(*InterventionTargetDocker).OperationTimeout != time.Minute {
		t.Fatal("rule settings were lost")
	}
	copyPolicy, err := copy.RecoveryRules()
	if err != nil {
		t.Fatal(err)
	}
	copyCompiled, _, err := api.CompileRecoveryRules(check, copyPolicy)
	if err != nil || copyCompiled.Digest() != compiled.Digest() {
		t.Fatalf("different file decisions: %v", err)
	}
}

func TestInterventionRulesValidateEveryBranch(t *testing.T) {
	input := `{"taxonomy_version":"1","rules":[{"id":"none","none":true,"match":{"codes":["network.connection_refused"]}},{"id":"incomplete","action":"webhook","target":{},"match":{"codes":["unexpected_http_status"]}}]}`
	var intervention Intervention
	if err := json.Unmarshal([]byte(input), &intervention); err != nil {
		t.Fatal(err)
	}
	m := Monitor{Name: "service", Pulse: Pulse{Type: "http", Interval: time.Minute, Timeout: time.Second, Config: &PulseHTTPConfig{Url: "https://example.invalid/health"}}, Intervention: intervention}
	if err := ValidateMonitor(&m); err == nil || !strings.Contains(err.Error(), "rules[1]") {
		t.Fatalf("inactive branch bypassed validation: %v", err)
	}
}
