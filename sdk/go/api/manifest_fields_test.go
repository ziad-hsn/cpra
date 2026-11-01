package api

import (
	"encoding/json"
	"testing"
)

func TestManifestFieldSpellings(t *testing.T) {
	for _, tt := range []struct {
		kind, raw   string
		json, valid bool
	}{
		{"http", `{"expected_status":[200],"headers":{"X_Arbitrary-Value":"secret"}}`, false, true},
		{"http", `{"expectedStatus":[200]}`, false, false}, {"http", `{"expectedStatus":[200]}`, true, false},
		{"http", `{"EXPECTED_STATUS":[200],"URL":"https://example.invalid"}`, true, true},
		{"http", `{"URL":"https://example.invalid"}`, false, false},
		{"icmp", `{"ignore_privilege":true}`, false, true}, {"icmp", `{"Privilege":true,"Host":"localhost"}`, true, true},
		{"icmp", `{"ignore_privilege":true}`, true, false},
		{"tcp", `{"Host":"localhost","Port":443,"Retries":0}`, true, true},
	} {
		err := ValidateManifestDriverFields("check", tt.kind, []byte(tt.raw), tt.json)
		if (err == nil) != tt.valid {
			t.Errorf("kind=%s json=%v valid=%v: %v", tt.kind, tt.json, tt.valid, err)
		}
	}
}
func TestRecoveryDecodeErrorPaths(t *testing.T) {
	for _, tt := range []struct{ recovery, field string }{
		{`{"taxonomyVersion":1,"rules":[]}`, "spec.recovery.taxonomyVersion"},
		{`{"taxonomyVersion":"1","rules":[{"id":"first","match":{"codes":null},"none":true}]}`, "spec.recovery.rules[0].match.codes"},
		{`{"taxonomyVersion":"1","rules":[{"id":"first","match":{"codes":["network.connection_refused"]},"none":true},{"id":"second","match":{"phases":12},"none":true}]}`, "spec.recovery.rules[1].match.phases"},
	} {
		r := Resource{APIVersion: APIVersion, Kind: "Monitor", Metadata: Metadata{ID: "test"}, Spec: json.RawMessage(`{"check":{"driver":{"type":"http","config":{"url":"https://example.invalid"}},"interval":"60s","timeout":"5s"},"recovery":` + tt.recovery + `}`)}
		detail, ok := ValidationFieldError(ValidateResource(r))
		if !ok || detail.Field != tt.field {
			t.Fatalf("want %s, got %+v", tt.field, detail)
		}
	}
}
