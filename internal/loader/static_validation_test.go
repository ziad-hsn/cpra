package loader

import (
	"context"
	"encoding/json"
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
