package streaming

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStreamingYamlParserLoadsV1Manifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	input := `monitors:
- name: api
  pulse_check:
    type: http
    interval: 1s
    timeout: 1s
    max_failures: 2
    config:
      retries: 2
      url: http://127.0.0.1/health
      method: GET
  codes:
    yellow:
      dispatch: true
      notify: log
      config: {file: alerts.jsonl}
- name: database
  pulse_check:
    type: tcp
    interval: 2s
    timeout: 1s
    max_failures: 3
    config: {host: 127.0.0.1, port: 5432}
`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	parser, err := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	batches, errs := parser.ParseBatches(context.Background(), nil)
	names := []string{}
	for batch := range batches {
		if len(batch.Monitors) != 1 {
			t.Fatalf("batch length = %d", len(batch.Monitors))
		}
		m := batch.Monitors[0]
		names = append(names, m.Name)
		if m.Name == "api" && (m.Pulse.UnhealthyThreshold != 2 || m.Pulse.Interval != time.Second || m.Codes["yellow"].Notify != "log") {
			t.Fatalf("legacy HTTP configuration changed: %+v", m)
		}
		if m.Name == "database" && (m.Pulse.UnhealthyThreshold != 3 || m.Pulse.Type != "tcp") {
			t.Fatalf("legacy TCP configuration changed: %+v", m)
		}
	}
	if err := readParseError(errs); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "api" || names[1] != "database" {
		t.Fatalf("names = %v", names)
	}
}
