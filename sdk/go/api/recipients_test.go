package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRecipientAndGroupContract(t *testing.T) {
	for _, tc := range []struct {
		name, kind, spec string
		valid            bool
	}{
		{"ordered contact", "Recipient", `{"endpointRefs":["secondary","primary"]}`, true},
		{"empty contact", "Recipient", `{"endpointRefs":[]}`, false},
		{"missing contact endpoints", "Recipient", `{}`, false},
		{"duplicate contact endpoint", "Recipient", `{"endpointRefs":["e","e"]}`, false},
		{"invalid identity", "Recipient", `{"endpointRefs":["../e"]}`, false},
		{"recipient-only group", "NotificationGroup", `{"recipientRefs":["oncall"]}`, true},
		{"mixed group", "NotificationGroup", `{"endpointRefs":["e"],"recipientRefs":["r"]}`, true},
		{"same ID different kinds", "NotificationGroup", `{"endpointRefs":["same"],"recipientRefs":["same"]}`, true},
		{"empty group", "NotificationGroup", `{}`, false},
		{"empty lists", "NotificationGroup", `{"endpointRefs":[],"recipientRefs":[]}`, false},
		{"duplicate member", "NotificationGroup", `{"recipientRefs":["r","r"]}`, false},
		{"nested group", "NotificationGroup", `{"groupRefs":["nested"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Resource{APIVersion: APIVersion, Kind: tc.kind, Metadata: Metadata{ID: "test"}, Spec: json.RawMessage(tc.spec)}
			if err := ValidateResource(r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	original := Recipient{APIVersion: APIVersion, Kind: "Recipient", Metadata: Metadata{ID: "oncall"}, Spec: RecipientSpec{EndpointRefs: []string{"secondary", "primary"}}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var got Recipient
	if err := DecodeResponse(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Spec.EndpointRefs, original.Spec.EndpointRefs) {
		t.Fatal("endpoint order changed")
	}
}

func TestCodeSelectsNotificationType(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		valid      bool
	}{
		{"person", `{"notifyType":"email","recipientRefs":["oncall"]}`, true},
		{"group", `{"notifyType":"twilio","groupRef":"ops"}`, true},
		{"both", `{"notifyType":"email","recipientRefs":["person"],"groupRef":"ops"}`, true},
		{"direct group", `{"groupRef":"ops"}`, true},
		{"inline driver with group", `{"groupRef":"ops","driver":{"type":"log","config":{"file":"output.log"}}}`, true},
		{"missing type", `{"recipientRefs":["oncall"]}`, false},
		{"abstract method", `{"notifyType":"sms","recipientRefs":["oncall"]}`, false},
		{"no target", `{"notifyType":"email"}`, false},
		{"empty target", `{"notifyType":"email","recipientRefs":[]}`, false},
		{"duplicate contact", `{"notifyType":"email","recipientRefs":["r","r"]}`, false},
		{"empty group", `{"notifyType":"email","groupRef":""}`, false},
		{"inline mixed", `{"notifyType":"email","recipientRefs":["r"],"driver":{"type":"email","config":{}}}`, false},
		{"direct mixed", `{"notifyType":"email","recipientRefs":["r"],"endpointRefs":["e"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"check":{"driver":{"type":"http","config":{"url":"https://example.test"}},"interval":"60s","timeout":"5s"},"notifications":{"red":` + tc.rule + `}}`
			r := Resource{APIVersion: APIVersion, Kind: "Monitor", Metadata: Metadata{ID: "m"}, Spec: json.RawMessage(raw)}
			if err := ValidateResource(r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestAccessRequiresExplicitPermissions(t *testing.T) {
	for _, raw := range []string{`{"principalId":"p","role":"operator"}`, `{"principalId":"p","role":"operator","permissions":null}`} {
		var access AccessInfo
		if DecodeResponse([]byte(raw), &access) == nil {
			t.Fatal("missing permissions accepted")
		}
	}
	var access AccessInfo
	if err := DecodeResponse([]byte(`{"principalId":"p","role":"operator","permissions":[]}`), &access); err != nil {
		t.Fatal(err)
	}
	if len(access.Permissions) != 0 {
		t.Fatal("role invented permissions")
	}
}
