//go:build !externaljobs

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestBaseSchemaExcludesExtension(t *testing.T) {
	for _, name := range []string{"external-workers", "JobTypeSpec", "PollRequest", "ExternalConfig"} {
		if bytes.Contains(Schema(), []byte(name)) {
			t.Fatalf("extension schema %s compiled", name)
		}
	}
	if !errors.Is(ValidateDriver("check", DriverConfig{Type: "external", Config: json.RawMessage(`{}`)}), ErrUnsupportedDriver) {
		t.Fatal("default build accepted custom driver")
	}
}
