package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestDirectReferences(t *testing.T) {
	driver := func(id string) api.DriverConfig {
		return api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": id})}
	}
	cases := []struct {
		name  string
		input api.Resource
		want  []persistence.CatalogKey
	}{
		{"monitor", resource("Monitor", "service", api.MonitorSpec{
			Check:                 api.CheckSpec{Driver: driver("check-key")},
			Recovery:              &api.RecoverySpec{Driver: driver("recovery-key")},
			NotificationGroupRefs: api.Pointer([]string{"ops"}),
			Notifications: api.Pointer(map[string]api.AlertRule{
				"red": {Driver: api.Pointer(driver("notification-key")), GroupRef: api.Pointer("ops"),
					EndpointRefs: api.Pointer([]string{"chat", "chat"}), RecipientRefs: api.Pointer([]string{"oncall"})},
			}),
		}), []persistence.CatalogKey{{Kind: "Credential", ID: "check-key"}, {Kind: "Credential", ID: "recovery-key"},
			{Kind: "Credential", ID: "notification-key"}, {Kind: "NotificationGroup", ID: "ops"},
			{Kind: "NotificationEndpoint", ID: "chat"}, {Kind: "Recipient", ID: "oncall"}}},
		{"endpoint", resource("NotificationEndpoint", "chat", driver("endpoint-key")),
			[]persistence.CatalogKey{{Kind: "Credential", ID: "endpoint-key"}}},
		{"recipient", resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}),
			[]persistence.CatalogKey{{Kind: "NotificationEndpoint", ID: "chat"}}},
		{"group", resource("NotificationGroup", "ops", api.NotificationGroupSpec{EndpointRefs: []string{"chat"}, RecipientRefs: []string{"oncall"}}),
			[]persistence.CatalogKey{{Kind: "NotificationEndpoint", ID: "chat"}, {Kind: "Recipient", ID: "oncall"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := bytes.Clone(tc.input.Spec)
			got, err := directReferences(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			want := make(map[persistence.CatalogKey]bool, len(tc.want))
			for _, key := range tc.want {
				want[key] = true
			}
			actual := make(map[persistence.CatalogKey]bool, len(got))
			for _, key := range got {
				actual[key] = true
			}
			if len(got) != len(want) || !reflect.DeepEqual(actual, want) {
				t.Fatalf("references = %v, want %v", got, tc.want)
			}
			if !bytes.Equal(before, tc.input.Spec) {
				t.Fatal("reference inspection changed the resource")
			}
		})
	}
}

func TestDirectReferencesRejectInvalidSpec(t *testing.T) {
	for name, spec := range map[string]string{
		"duplicate field":       `{"check":{"driver":{"type":"http","credentialRefs":{"url":"first","url":"second"},"config":{}}}}`,
		"unknown field":         `{"check":{"driver":{"type":"http","credentialRefs":{"url":"key"},"config":{}},"unexpected":true}}`,
		"check identity":        `{"check":{"driver":{"type":"http","credentialRefs":{"url":"../key"},"config":{}}}}`,
		"recovery identity":     `{"check":{"driver":{"type":"http","config":{}}},"recovery":{"driver":{"type":"webhook","credentialRefs":{"url":"../key"},"config":{}}}}`,
		"notification identity": `{"check":{"driver":{"type":"http","config":{}}},"notifications":{"red":{"driver":{"type":"slack","credentialRefs":{"hook":"../key"},"config":{}}}}}`,
		"oversized identity":    `{"check":{"driver":{"type":"http","credentialRefs":{"url":"` + strings.Repeat("x", 257) + `"},"config":{}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := directReferences(api.Resource{Kind: "Monitor", Spec: json.RawMessage(spec)})
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("invalid references accepted: %v", err)
			}
		})
	}
}
