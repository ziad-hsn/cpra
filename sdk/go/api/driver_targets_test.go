package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDriverSettingsCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/driver-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Category, Driver, Field, Reason string
		Config                                json.RawMessage
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			err := ValidateDriverSettings(test.Category, DriverConfig{Type: test.Driver, Config: test.Config})
			if test.Reason == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			field, ok := ValidationFieldError(err)
			if !ok || field.Field != test.Field || field.Reason != test.Reason {
				t.Fatal("rule or location changed", err)
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("rejected value was exposed")
			}
		})
	}
}

func TestDeferredTLSTransportValidation(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql"} {
		driver := DriverConfig{Type: kind, Config: json.RawMessage(`{"tls":true}`), CredentialRefs: Pointer(map[string]string{"dsn": "connection"})}
		if err := ValidateDriverSettings("check", driver); err != nil {
			t.Fatal("unresolved secret rejected", err)
		}
	}
}

func TestValidationMessagesHaveNoDuplicateRules(t *testing.T) {
	var messages map[string]string
	if err := StrictDecode(validationMessageBytes, &messages); err != nil {
		t.Fatal(err)
	}
}
