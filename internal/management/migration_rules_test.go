package management

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestBootstrapExtractsEveryRecoveryBranchWithoutChangingIdentity(t *testing.T) {
	input := resource("Monitor", "rules", json.RawMessage(`{"check":{"driver":{"type":"http","config":{"url":"https://example.test/health"}},"interval":"60s","timeout":"5s"},"recovery":{"taxonomyVersion":"1","rules":[{"id":"refused","match":{"codes":["network.connection_refused"]},"driver":{"type":"webhook","config":{"url":"https://example.test/private-one"}}},{"id":"reset","match":{"codes":["network.connection_reset"]},"driver":{"type":"webhook","config":{"url":"https://example.test/private-two"}}},{"id":"no-action","match":{"codes":["unexpected_http_status"]},"none":true}]}}`))
	converted, credentials, err := ExtractInlineCredentials(input)
	if err != nil || len(credentials) != 2 {
		t.Fatal("rule extraction", err, len(credentials))
	}
	if credentials[0].Metadata.ID == credentials[1].Metadata.ID {
		t.Fatal("branches share a credential identity")
	}
	raw, _ := json.Marshal(converted)
	if bytes.Contains(raw, []byte("private-one")) || bytes.Contains(raw, []byte("private-two")) {
		t.Fatal("inline secret escaped conversion")
	}
	var spec api.MonitorSpec
	if err := json.Unmarshal(converted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if !spec.Recovery.Rules[2].None {
		t.Fatal("none branch changed")
	}
	rotated := input
	rotated.Spec = bytes.ReplaceAll(input.Spec, []byte("private-one"), []byte("rotated-secret"))
	_, next, err := ExtractInlineCredentials(rotated)
	if err != nil {
		t.Fatal(err)
	}
	for i := range credentials {
		if credentials[i].Metadata.ID != next[i].Metadata.ID {
			t.Fatal("rotation changed identity")
		}
	}
	again, repeated, err := ExtractInlineCredentials(input)
	if err != nil || !reflect.DeepEqual(converted, again) || !reflect.DeepEqual(credentials, repeated) {
		t.Fatal("repeated bootstrap changed identity", err)
	}
}
