package loader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultParserRejectsUnknownAndIncompleteConfiguration(t *testing.T) {
	valid := `{"monitors":[{"name":"fixture","pulse_check":{"type":"http","interval":"1s","timeout":"1s","config":{"url":"https://example.com"}}}]}`
	for _, fixture := range []struct{ name, content string }{
		{"enabled-typo", strings.Replace(valid, `"name":"fixture"`, `"name":"fixture","enabeld":false`, 1)},
		{"maintenance-typo", strings.Replace(valid, `"name":"fixture"`, `"name":"fixture","maintainance":{}`, 1)},
		{"threshold-typo", strings.Replace(valid, `"interval":"1s"`, `"interval":"1s","unhealthy_treshold":3`, 1)},
		{"method-typo", strings.Replace(valid, `"url":"https://example.com"`, `"url":"https://example.com","methd":"GET"`, 1)},
		{"duplicate-name", strings.Replace(valid, `"name":"fixture"`, `"name":"first","name":"fixture"`, 1)},
		{"duplicate-url", strings.Replace(valid, `"url":"https://example.com"`, `"url":"https://example.com","url":"https://elsewhere.example.com"`, 1)},
		{"duplicate-field-alias", strings.Replace(valid, `"url":"https://example.com"`, `"url":"https://example.com","URL":"https://elsewhere.example.com"`, 1)},
		{"wrong-type", strings.Replace(valid, `"interval":"1s"`, `"interval":false`, 1)},
		{"missing-discord", strings.Replace(valid, `"name":"fixture"`, `"name":"fixture","codes":{"red":{"notify":"discord","config":{}}}`, 1)},
		{"missing-group", strings.Replace(valid, `"name":"fixture"`, `"name":"fixture","codes":{"red":{"notify_group":"missing"}}`, 1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, extension := range []string{"json", "yaml"} {
				path := filepath.Join(t.TempDir(), "monitors."+extension)
				if err := os.WriteFile(path, []byte(fixture.content), 0600); err != nil {
					t.Fatal(err)
				}
				var batches <-chan MonitorBatch
				var failures <-chan error
				if extension == "json" {
					parser, _ := NewStreamingJsonParser(path, ParseConfig{})
					batches, failures = parser.ParseBatches(context.Background(), nil)
				} else {
					// YAML accepts JSON object syntax as a document. Exercise block
					// monitors through the same decoder contract separately below.
					var input map[string]json.RawMessage
					_ = json.Unmarshal([]byte(fixture.content), &input)
					var monitors []json.RawMessage
					_ = json.Unmarshal(input["monitors"], &monitors)
					if err := os.WriteFile(path, []byte("monitors:\n  - "+string(monitors[0])+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					parser, _ := NewStreamingYamlParser(path, ParseConfig{})
					batches, failures = parser.ParseBatches(context.Background(), nil)
				}
				for range batches {
				}
				failed := false
				for err := range failures {
					failed = failed || err != nil
				}
				if !failed {
					t.Errorf("default %s parser accepted %s", extension, fixture.name)
				}
			}
		})
	}
}

func TestYAMLScalarTypesAnchorsAndOriginalPositions(t *testing.T) {
	valid := "monitors:\n  - name: first\n    pulse_check: &check {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n  - name: second\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n"
	for _, test := range []struct{ name, input, position string }{
		{"number-name", strings.Replace(valid, "name: second", "name: 123", 1), "line 4"},
		{"number-host", strings.Replace(valid, "host: localhost", "host: 123", 1), "line 3"},
		{"boolean-name", strings.Replace(valid, "name: second", "name: true", 1), "line 4"},
		{"number-groups", strings.Replace(valid, "type: tcp", "groups: 123, type: tcp", 1), "line 3"},
		{"boolean-groups", strings.Replace(valid, "type: tcp", "groups: true, type: tcp", 1), "line 3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			parser, _ := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1})
			batches, errs := parser.ParseBatches(t.Context(), nil)
			for range batches {
			}
			err := <-errs
			if err == nil || !strings.Contains(err.Error(), test.position) || !strings.Contains(err.Error(), path) {
				t.Fatalf("missing original source position: %v", err)
			}
		})
	}
}

func TestDefaultParserErrorsNeverEchoRejectedValues(t *testing.T) {
	const marker = "PRIVATE_REJECTED_MARKER"
	for _, input := range []struct{ extension, body string }{
		{"json", `"PRIVATE_REJECTED_MARKER"`},
		{"json", `{"monitors":"PRIVATE_REJECTED_MARKER"}`},
		{"json", `{"PRIVATE_REJECTED_MARKER":{},"monitors":[]}`},
		{"yaml", "monitors:\n  - name: sample\n    PRIVATE_REJECTED_MARKER: private\n    pulse_check: {type: http, interval: 1s, timeout: 1s, config: {url: 'https://example.test'}}\n"},
		{"yaml", "monitors:\n  - name: sample\n    pulse_check: {type: http, interval: 1s, timeout: 1s, config: {url: 'https://example.test', headers: {PRIVATE_REJECTED_MARKER: [private]}}}\n"},
	} {
		path := filepath.Join(t.TempDir(), "input."+input.extension)
		if err := os.WriteFile(path, []byte(input.body), 0600); err != nil {
			t.Fatal(err)
		}
		var batches <-chan MonitorBatch
		var failures <-chan error
		if input.extension == "json" {
			parser, _ := NewStreamingJsonParser(path, ParseConfig{})
			batches, failures = parser.ParseBatches(t.Context(), nil)
		} else {
			parser, _ := NewStreamingYamlParser(path, ParseConfig{})
			batches, failures = parser.ParseBatches(t.Context(), nil)
		}
		for range batches {
		}
		err := <-failures
		if err == nil {
			t.Fatal("rejected input was accepted")
		}
		for current := err; current != nil; current = errors.Unwrap(current) {
			if strings.Contains(current.Error(), marker) {
				t.Fatal("error retained rejected data", current)
			}
		}
	}
}
