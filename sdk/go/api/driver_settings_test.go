package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDriverSettingsRequireCompleteConfiguration(t *testing.T) {
	for _, fixture := range []struct{ category, kind, config string }{
		{"notification", "discord", `{}`},
		{"notification", "slack", `{"hook":""}`},
		{"notification", "webhook", `{"url":"not a URL"}`},
		{"check", "tcp", `{"host":"localhost","port":0}`},
		{"check", "http", `{"url":"https://example.com","retries":-1}`},
		{"recovery", "docker", `{"container":"fixture","timeout":"10s","operationTimeout":"29s"}`},
		{"check", "grpc", `{"host":"localhost","port":443,"mode":"future"}`},
		{"check", "grpc", `{"host":"localhost","port":443,"allowInsecure":true}`},
		{"check", "grpc", `{"host":"localhost","port":443,"mode":"health","allowInsecure":true,"caFile":"private-file"}`},
	} {
		d := DriverConfig{Type: fixture.kind, Config: json.RawMessage(fixture.config)}
		if err := ValidateDriverSettings(fixture.category, d); err == nil {
			t.Errorf("accepted incomplete %s/%s", fixture.category, fixture.kind)
		} else if strings.Contains(err.Error(), "private-file") {
			t.Error("validation echoed a private value")
		}
	}
}

func TestDriverSettingsPreserveCredentialsAndOptionalValues(t *testing.T) {
	for _, fixture := range []struct{ category, kind, config string }{
		{"check", "http", `{"url":"https://example.com","retries":0,"insecureSkipVerify":false}`},
		{"check", "grpc", `{"host":"localhost","port":443,"service":"billing"}`},
		{"check", "grpc", `{"host":"localhost","port":443,"mode":"health","caFile":"/file-that-does-not-exist"}`},
		{"recovery", "docker", `{"container":"fixture","timeout":"10s","operationTimeout":"30s"}`},
	} {
		if err := ValidateDriverSettings(fixture.category, DriverConfig{Type: fixture.kind, Config: json.RawMessage(fixture.config)}); err != nil {
			t.Errorf("rejected valid %s/%s: %v", fixture.category, fixture.kind, err)
		}
	}
	endpoint := DriverConfig{Type: "discord", Config: json.RawMessage(`{}`), CredentialRefs: Pointer(map[string]string{"webhookUrl": "destination"})}
	if err := ValidateDriverSettings("notification", endpoint); err != nil {
		t.Fatal(err)
	}
	endpoint.CredentialRefs = nil
	if ValidateDriverSettings("notification", endpoint) == nil {
		t.Fatal("missing credential reference satisfied destination presence")
	}
}
