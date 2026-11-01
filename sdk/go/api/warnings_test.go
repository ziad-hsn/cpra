package api

import (
	"encoding/json"
	"testing"
)

func TestUnknownRecoveryDestinationWarning(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		warn        bool
	}{
		{"absent", `{}`, true},
		{"red-only", `{"red":{"endpointRefs":["ops"]}}`, true},
		{"disabled-blue", `{"blue":{"dispatch":false,"endpointRefs":["review"]}}`, true},
		{"explicit-blue", `{"blue":{"endpointRefs":["review"]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Resource{Kind: "Monitor", Spec: json.RawMessage(`{"check":{"interval":"60s","driver":{"type":"http","config":{"url":"https://example.invalid"}}},"recovery":{"driver":{"type":"webhook","config":{"url":"https://example.invalid/recover"}}},"notifications":` + tc.rules + `}`)}
			warnings := ResourceWarnings(r)
			if (len(warnings) != 0) != tc.warn {
				t.Fatal("unexpected warning", warnings)
			}
			if len(warnings) > 0 && (warnings[0].Reason != "unknownRecoveryDestination" || warnings[0].Message == "") {
				t.Fatal("warning has no stable cause")
			}
		})
	}
}

func TestRecoveryOverlapWarningUsesSourceField(t *testing.T) {
	spec := MonitorSpec{Check: CheckSpec{Driver: DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.invalid"}`)}}, Recovery: &RecoverySpec{TaxonomyVersion: "1", Rules: []RecoveryRule{
		{ID: "first", Match: FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{502, 503}}, None: true},
		{ID: "second", Match: FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{503, 504}}, None: true},
	}}}
	raw, _ := json.Marshal(spec)
	warnings := ResourceWarnings(Resource{Kind: "Monitor", Spec: raw})
	if len(warnings) != 1 || warnings[0].Reason != "partialOverlap" || warnings[0].Field != "spec.recovery.rules[1]" || warnings[0].Message == "" {
		t.Fatal(warnings)
	}
}

func TestRecoveryOnlyTriggerWarnsForUnmatchedAndNoneDecisions(t *testing.T) {
	for _, action := range []string{`"none":true`, `"driver":{"type":"webhook","credentialRefs":{"url":"recovery-url"}}`} {
		resource := Resource{Kind: "Monitor", Spec: json.RawMessage(`{"check":{"interval":"60s","driver":{"type":"http","config":{"url":"https://example.invalid"}}},"recovery":{"taxonomyVersion":"1","rules":[{"id":"one","match":{"codes":["network.connection_refused"]},` + action + `}]},"notifications":{"red":{"trigger":"recovery_failed","endpointRefs":["oncall"]}}}`)}
		warnings := ResourceWarnings(resource)
		coverage, blue := 0, 0
		for _, w := range warnings {
			if w.Reason == "recoveryOnlyCoverage" {
				coverage++
			}
			if w.Reason == "unknownRecoveryDestination" {
				blue++
			}
			if w.Message == "" {
				t.Fatal("missing public explanation")
			}
		}
		if coverage != 1 || action == `"none":true` && blue != 0 || action != `"none":true` && blue != 1 {
			t.Fatal("wrong rule warnings", warnings)
		}
	}
}
