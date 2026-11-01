package collection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRecoveryRulesSharedFileContract(t *testing.T) {
	input := `monitors:
  - id: example
    name: Example
    pulse_check: {type: http, interval: 60s, timeout: 5s, config: {url: 'https://example.invalid/health'}}
    intervention:
      taxonomy_version: "1"
      max_attempts: 1
      rules:
        - id: unauthorized
          match: {codes: [unexpected_http_status], http_statuses: [401, 403]}
          none: true
        - id: restart
          match: {codes: [network.connection_refused]}
          action: docker
          target: {container: designated}
`
	var item NormalizedItem
	err := NormalizeFile(context.Background(), strings.NewReader(input), FileNormalizationProfile, DecodeOptions{}, func(value NormalizedItem) error { item = value; return nil })
	if err != nil {
		t.Fatal(err)
	}
	var resource api.Resource
	if err = api.StrictDecode(item.JSON, &resource); err != nil {
		t.Fatal(err)
	}
	if err = api.ValidateResourceSettings(resource); err != nil {
		t.Fatal(err)
	}
	var spec api.MonitorSpec
	if err = json.Unmarshal(resource.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := api.CompileRecoveryRules(spec.Check.Driver, *spec.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	observation, _ := api.CanonicalFailure("1", "unexpected_http_status", "response")
	status := 401
	observation.HTTPStatus = &status
	if decision := compiled.Match(observation); decision.RuleID != "unauthorized" || decision.Intervention {
		t.Fatalf("file contract changed decision: %+v", decision)
	}
	for _, profile := range []string{fileNormalizationV1, fileNormalizationV2, fileNormalizationV3} {
		if err := ValidateFileProfileResource(profile, resource); err == nil {
			t.Fatalf("new rule accepted into historical profile %s", profile)
		}
	}
}

func TestRecoveryRuleFinalInvalidBranchPreventsSubmission(t *testing.T) {
	last := `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"last"},"spec":{"check":{"driver":{"type":"http","config":{"url":"https://example.invalid"}},"interval":"60s","timeout":"5s"},"recovery":{"taxonomyVersion":"1","rules":[{"id":"invalid","match":{"codes":["operation_failed"]},"driver":{"type":"webhook","config":{"url":"https://example.invalid/recover"}}}]}}}`
	frozen, err := Freeze(context.Background(), []Source{Reader("first.json", strings.NewReader(monitor("first"))), Reader("last.json", strings.NewReader(last))}, Options{})
	if err != nil {
		return
	} // Rejection during parsing also prevents submission.
	t.Cleanup(func() { _ = frozen.Close() })
	operations := &fakeOperations{valid: true}
	_, err = Apply(context.Background(), operations, frozen)
	if err == nil || len(operations.calls) != 0 {
		t.Fatalf("invalid final rule submitted changes: %v %v", err, operations.calls)
	}
}
