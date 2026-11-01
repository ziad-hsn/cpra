package management

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionDurationComparisonPreservesInputAndGeneration(t *testing.T) {
	c, store := testCatalog(t)
	original := collectionMonitor("duration", "https://service.example/health")
	saved := createResource(t, c, original)
	updated := original
	updated.Spec = json.RawMessage(strings.Replace(string(original.Spec), `"60s"`, `"0h1m0s"`, 1))
	before := bytes.Clone(updated.Spec)
	index := store.Status().CommittedIndex
	result, err := validateCollection(t, c, collectionInputs(updated))
	if err != nil || !result.Valid || len(result.Items) != 1 {
		t.Fatal("preflight failed", err, result)
	}
	if item := result.Items[0]; item.Change != "unchanged" || len(item.ChangedFields) != 0 || item.ResourceVersion != saved.Metadata.ResourceVersion {
		t.Fatal("equivalent duration changed desired configuration", item)
	}
	if store.Status().CommittedIndex != index || !bytes.Equal(before, updated.Spec) {
		t.Fatal("comparison changed source bytes or durable state")
	}
	prepared, err := c.Prepare(t.Context(), updated, saved.Metadata.ResourceVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := c.Commit(t.Context(), prepared)
	if err != nil || committed.Resource.Metadata.Generation != saved.Metadata.Generation || !bytes.Contains(committed.Resource.Spec, []byte(`"0h1m0s"`)) {
		t.Fatal("explicit replacement lost the spelling or changed semantic generation", err)
	}
}

func TestDurationComparisonTouchesOnlyDeclaredFields(t *testing.T) {
	for _, test := range []struct {
		name, kind, a, b string
		equal            bool
	}{
		{"check", "Monitor", `{"check":{"interval":"60s","timeout":"1000ms"}}`, `{"check":{"interval":"1m","timeout":"1s"}}`, true},
		{"rules", "Monitor", `{"recovery":{"cooldown":"60s","rules":[{"driver":{"type":"docker","config":{"timeout":"60s","operationTimeout":"120s"}}}]}}`, `{"recovery":{"cooldown":"1m","rules":[{"driver":{"type":"docker","config":{"timeout":"1m","operationTimeout":"2m"}}}]}}`, true},
		{"endpoint", "NotificationEndpoint", `{"type":"a2a","config":{"requestTimeout":"1000ms","timeout":"60s"}}`, `{"type":"a2a","config":{"requestTimeout":"1s","timeout":"1m"}}`, true},
		{"custom parameters", "Monitor", `{"recovery":{"driver":{"type":"external","config":{"parameters":{"timeout":"60s"}}}}}`, `{"recovery":{"driver":{"type":"external","config":{"parameters":{"timeout":"1m"}}}}}`, false},
		{"credential", "Credential", `{"value":"60s"}`, `{"value":"1m"}`, false},
		{"body", "Monitor", `{"recovery":{"driver":{"type":"webhook","config":{"body":"60s"}}}}`, `{"recovery":{"driver":{"type":"webhook","config":{"body":"1m"}}}}`, false},
		{"omitted", "Monitor", `{"recovery":{}}`, `{"recovery":{"cooldown":"0s"}}`, false},
		{"null", "Monitor", `{"recovery":{"cooldown":null}}`, `{"recovery":{"cooldown":"0s"}}`, false},
		{"zero", "Monitor", `{"recovery":{"cooldown":"0ms"}}`, `{"recovery":{"cooldown":"0s"}}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := json.RawMessage(test.a), json.RawMessage(test.b)
			if got := desiredSpecEqual(test.kind, a, b); got != test.equal {
				t.Fatal("incorrect comparison", got)
			}
			if string(a) != test.a || string(b) != test.b {
				t.Fatal("comparison mutated input")
			}
		})
	}
	a := api.Resource{Kind: "Monitor", Metadata: api.Metadata{ID: "a"}, Spec: json.RawMessage(`{"check":{"interval":"60s"}}`)}
	b := a
	b.Metadata.Name = api.Pointer("changed")
	b.Spec = json.RawMessage(`{"check":{"interval":"1m"}}`)
	if collectionDesiredEqual(a, b) {
		t.Fatal("semantic duration comparison ignored changed metadata")
	}
}
