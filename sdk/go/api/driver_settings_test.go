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
		{"check", "grpc", `{"host":"localhost","port":443,"mode":"health","service":"billing"}`},
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

func TestProductReviewDriverRejections(t *testing.T) {
	for _, fixture := range []struct{ category, kind, config string }{
		{"check", "udp", `{"host":"host","port":53}`},
		{"check", "http", `{"url":"https://example.test","method":"FOO BAR"}`},
		{"check", "http", `{"url":"https://example.test","expectedStatus":[999]}`},
		{"check", "dns", `{"host":"host","server":"bad:port"}`},
		{"check", "grpc", `{"host":"host","port":443,"service":"SECRET"}`},
		{"check", "mysql", `{"dsn":"user:SECRET@tcp(host)/db?allowAllFiles=TRUE&charset=utf8mb4"}`},
		{"check", "mysql", `{"dsn":"user:SECRET@tcp(host)/db?allowAllFiles=1"}`},
		{"notification", "log", `{"file":"/private/SECRET"}`},
		{"notification", "log", `{"file":"../SECRET"}`},
		{"notification", "pushover", `{"appToken":"SECRET","userKey":"user","priority":7}`},
		{"notification", "telegram", `{"botToken":"SECRET","chatId":"team","testMode":true,"url":"http://localhost"}`},
		{"notification", "email", `{"to":"not an address","from":"sender@example.test","server":"smtp.example.test:25"}`},
		{"notification", "email", `{"to":"ops@example.test","from":"not an address","server":"smtp.example.test:25"}`},
		{"recovery", "kubernetes", `{"kind":"statefulset","name":"app","namespace":"default"}`},
		{"recovery", "aws", `{"operation":"terminate-instance","instanceId":"i-fixture","region":"eu-west-1"}`},
		{"recovery", "systemd", `{"unit":"fixture.service","mode":"explode"}`},
		{"recovery", "docker", `{"container":"fixture","timeout":"23h59m40.000000001s"}`},
	} {
		t.Run(fixture.category+"/"+fixture.kind, func(t *testing.T) {
			err := ValidateDriverSettings(fixture.category, DriverConfig{Type: fixture.kind, Config: json.RawMessage(fixture.config)})
			if err == nil {
				t.Fatal("unsafe or unsupported settings accepted")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("validation echoed private input")
			}
		})
	}
	for _, fixture := range []struct{ category, kind, config string }{
		{"check", "udp", `{"host":"host","port":53,"payload":"ping"}`},
		{"check", "dns", `{"host":"host","server":"[::1]:53"}`},
		{"check", "mysql", `{"dsn":"user:SECRET@tcp(host)/db?allowAllFiles=FALSE"}`},
		{"recovery", "docker", `{"container":"fixture","timeout":"23h59m40s","operationTimeout":"24h"}`},
		{"notification", "log", `{"file":"service/events.log"}`},
	} {
		if err := ValidateDriverSettings(fixture.category, DriverConfig{Type: fixture.kind, Config: json.RawMessage(fixture.config)}); err != nil {
			t.Errorf("valid %s/%s: %v", fixture.category, fixture.kind, err)
		}
	}
}
