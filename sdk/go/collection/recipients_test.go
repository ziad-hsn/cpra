package collection

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRecipientReferencesAcrossSources(t *testing.T) {
	sources := []Source{
		Reader("service.json", strings.NewReader(`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"service"},"spec":{"check":{"driver":{"type":"http","config":{"url":"https://example.test"}},"interval":"60s","timeout":"5s"},"notifications":{"red":{"notifyType":"email","recipientRefs":["oncall"],"groupRef":"ops"}}}}`)),
		Reader("people.yaml", strings.NewReader("apiVersion: cpra.io/v2\nkind: Recipient\nmetadata:\n  id: oncall\nspec:\n  endpointRefs: [secondary, primary]\n")),
		Reader("groups.json", strings.NewReader(`{"apiVersion":"cpra.io/v2","kind":"NotificationGroup","metadata":{"id":"ops"},"spec":{"recipientRefs":["oncall"]}}`)),
	}
	f, err := Freeze(context.Background(), sources, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var order []Reference
	observed, err := ValidateReferences(context.Background(), f, func(_ context.Context, ref Reference) (string, error) { order = append(order, ref); return "rv-2", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{{Kind: "NotificationEndpoint", ID: "secondary"}, {Kind: "NotificationEndpoint", ID: "primary"}}
	if !reflect.DeepEqual(order, want) || len(observed) != 2 {
		t.Fatalf("references=%v observed=%v", order, observed)
	}
	if _, err := ValidateReferences(context.Background(), f, nil); err == nil || !strings.Contains(err.Error(), "people.yaml") {
		t.Fatalf("missing source attribution: %v", err)
	}
}

func TestRuleRecipientReferences(t *testing.T) {
	item := Item{Resource: api.Resource{Kind: "Monitor", Spec: json.RawMessage(`{"notifications":{"red":{"notifyType":"email","recipientRefs":["direct"],"groupRef":"ops"}}}`)}}
	refs, err := itemReferences(item)
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{{Kind: "NotificationGroup", ID: "ops"}, {Kind: "Recipient", ID: "direct"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("got %v want %v", refs, want)
	}
}
