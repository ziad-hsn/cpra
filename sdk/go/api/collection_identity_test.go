package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCollectionPrivateRequestFormatting(t *testing.T) {
	key := strings.Repeat("ab", 32)
	fingerprint := strings.Repeat("cd", 32)
	secret := "credential-payload-not-for-diagnostics"
	preflight := PreflightRequest{IdentityKey: &key, SourceFingerprint: &fingerprint, Items: []ApplyItem{{Resource: Resource{Kind: "Credential", Spec: json.RawMessage(`{"value":"` + secret + `"}`)}}}}
	create := OperationCreateRequest{AdmissionTicket: secret, IdentityKey: &key, SourceFingerprint: &fingerprint}
	prepare := CollectionPrepareRequest{IdentityKey: &key, SourceFingerprint: &fingerprint}
	for _, value := range []any{preflight, &preflight, create, &create, prepare, &prepare} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%#x"} {
			formatted := fmt.Sprintf(verb, value)
			if strings.Contains(formatted, key) || strings.Contains(formatted, fingerprint) || strings.Contains(formatted, secret) || !strings.Contains(formatted, "input omitted") {
				t.Fatalf("private request was not redacted for %s", verb)
			}
		}
		// The actual wire encoder must still send the original key to the
		// authenticated origin; diagnostic redaction must not alter the protocol.
		raw, err := json.Marshal(value)
		if err != nil || !bytes.Contains(raw, []byte(key)) || !bytes.Contains(raw, []byte(fingerprint)) {
			t.Fatal("private wire identity was changed")
		}
		clear(raw)
	}
}

func TestCollectionIdentitySchemaKeepsPrivateInputsWriteOnly(t *testing.T) {
	var schema struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					WriteOnly bool `json:"writeOnly"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PreflightRequest", "OperationCreateRequest", "CollectionPrepareRequest"} {
		model := schema.Components.Schemas[name]
		for _, field := range []string{"identityKey", "sourceFingerprint"} {
			required := false
			for _, value := range model.Required {
				required = required || value == field
			}
			if !required || !model.Properties[field].WriteOnly {
				t.Fatalf("%s.%s must be required on writes and write-only", name, field)
			}
		}
	}
	for _, name := range []string{"Operation", "OperationList", "Preflight", "ApplyResult"} {
		for _, field := range []string{"identityKey", "sourceFingerprint"} {
			if _, exists := schema.Components.Schemas[name].Properties[field]; exists {
				t.Fatalf("private collection identity exposed through %s.%s", name, field)
			}
		}
	}
}

func TestCollectionReceiptCountPresence(t *testing.T) {
	for _, test := range []struct{ name, base string }{
		{"operation", `{"id":"op","state":"staging","contentDigest":""`},
		{"preflight", `{"valid":false`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, suffix := range []string{"}", `,"itemCount":0}`, `,"itemCount":null}`} {
				var count *int64
				var err error
				if test.name == "operation" {
					var value Operation
					err = DecodeResponse([]byte(test.base+suffix), &value)
					count = value.ItemCount
				} else {
					var value Preflight
					err = DecodeResponse([]byte(test.base+suffix), &value)
					count = value.ItemCount
				}
				switch suffix {
				case "}":
					if err != nil || count != nil {
						t.Fatal("absent count was not preserved", err)
					}
				case `,"itemCount":0}`:
					if err != nil || count == nil || *count != 0 {
						t.Fatal("explicit empty count was lost", err)
					}
				default:
					if err == nil {
						t.Fatal("unsupported null count was accepted")
					}
				}
			}
		})
	}
}

func TestCollectionAdmissionFormatting(t *testing.T) {
	ticket := "private-admission-ticket-canary"
	admission := CollectionAdmission{Ticket: ticket}
	for _, value := range []any{admission, &admission, struct{ Admission CollectionAdmission }{admission}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%#x"} {
			formatted := fmt.Sprintf(verb, value)
			if strings.Contains(formatted, ticket) || !strings.Contains(formatted, "input omitted") {
				t.Fatalf("ticket exposed for %s", verb)
			}
		}
	}
	raw, err := json.Marshal(admission)
	if err != nil || !bytes.Contains(raw, []byte(ticket)) {
		t.Fatal("wire ticket changed")
	}
}
