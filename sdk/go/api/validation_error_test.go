package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidationErrorsPreserveSafeFields(t *testing.T) {
	resource := Resource{APIVersion: APIVersion, Kind: "Monitor", Metadata: Metadata{ID: "sample"}, Spec: json.RawMessage(`{"check":{"interval":"1s","timeout":"1s","driver":{"type":"http","config":{}}}}`)}
	err := ValidateResourceSettings(resource)
	var detail *ValidationError
	if !errors.As(err, &detail) || detail.Field != "spec.check.driver.config.url" || detail.Reason != "required" {
		t.Fatalf("missing specific diagnostic: %v", err)
	}
	field, ok := ValidationFieldError(err)
	if !ok || field.Message != ValidationMessage("required") || field.Field != detail.Field {
		t.Fatal("wire and SDK diagnostics differ", field)
	}
	wrapped := PrefixValidationError("items[12]", errors.Join(errors.ErrUnsupported, err))
	if !errors.Is(wrapped, errors.ErrUnsupported) || !errors.As(wrapped, &detail) || detail.Field != "items[12].spec.check.driver.config.url" {
		t.Fatal("wrapped diagnostics lost cause/path", wrapped)
	}
}

func TestValidationErrorsDoNotRetainRejectedValues(t *testing.T) {
	const marker = "PRIVATE_CANARY_123456789"
	for _, input := range []string{
		`{"` + marker + `":true}`, `{"port":"` + marker + `"}`,
		`{"` + marker + `":1,"` + marker + `":2}`, `{"port":` + marker + `}`,
	} {
		var target PulseTCPConfig
		err := StrictDecode([]byte(input), &target)
		if err == nil {
			t.Fatal("invalid input accepted")
		}
		for current := err; current != nil; current = errors.Unwrap(current) {
			if strings.Contains(current.Error(), marker) {
				t.Fatal("decoder retained rejected value")
			}
		}
	}
	for _, path := range []string{"spec." + marker, "spec.config.headers." + marker, "spec.config./private/key", "spec\nvalue"} {
		if SafeValidationField(path) != "" {
			t.Fatal("unreviewed property escaped", path)
		}
	}
	if err := InvalidField(marker, marker); strings.Contains(err.Error(), marker) {
		t.Fatal("unknown rule/path was echoed")
	}
	if ValidationMessage("__proto__") != "" {
		t.Fatal("unknown message accepted")
	}
}

func TestValidationRuleLocationsAndMeanings(t *testing.T) {
	for _, test := range []struct{ category, driver, config, field, reason string }{
		{"check", "tcp", `{"host":"localhost","port":0}`, "config.port", "portRange"},
		{"check", "http", `{"url":"http://localhost","insecureSkipVerify":true}`, "config.url", "httpsRequired"},
		{"notification", "discord", `{}`, "config.webhookUrl", "required"},
		{"notification", "log", `{"file":"../private"}`, "config.file", "logPath"},
		{"recovery", "kubernetes", `{"namespace":"default","name":"app","kind":"pod"}`, "config.kind", "kubernetesKind"},
	} {
		err := ValidateDriverSettings(test.category, DriverConfig{Type: test.driver, Config: json.RawMessage(test.config)})
		field, ok := ValidationFieldError(err)
		if !ok || field.Field != test.field || field.Reason != test.reason {
			t.Fatalf("%s: %v", test.driver, err)
		}
	}
}
