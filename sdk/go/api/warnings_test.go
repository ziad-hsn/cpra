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
