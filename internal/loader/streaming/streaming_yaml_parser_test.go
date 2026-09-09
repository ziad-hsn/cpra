package streaming

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamingYamlParserBatchIsolation(t *testing.T) {
	dir := t.TempDir()
	yamlContent := []byte(`monitors:
  - name: "api"
    enabled: true
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config:
        url: "https://api.example.com/health"
  - name: "db"
    enabled: true
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config:
        url: "https://db.example.com/health"
  - name: "cache"
    enabled: true
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config:
        url: "https://cache.example.com/health"
`)

	file := filepath.Join(dir, "monitors.yaml")
	if err := os.WriteFile(file, yamlContent, 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}

	parser, err := NewStreamingYamlParser(file, ParseConfig{BatchSize: 2})
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	batchChan, errChan := parser.ParseBatches(ctx, nil)

	var batches []MonitorBatch
	for batch := range batchChan {
		batches = append(batches, batch)
	}

	if err := readParseError(errChan); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}

	if got := len(batches[0].Monitors); got != 2 {
		t.Fatalf("first batch size: expected 2, got %d", got)
	}
	if got := len(batches[1].Monitors); got != 1 {
		t.Fatalf("second batch size: expected 1, got %d", got)
	}

	expectedNames := [][]string{{"api", "db"}, {"cache"}}
	for i, batch := range batches {
		for j, monitor := range batch.Monitors {
			if monitor.Name != expectedNames[i][j] {
				t.Fatalf("batch %d monitor %d: expected %q, got %q", i, j, expectedNames[i][j], monitor.Name)
			}
		}
	}

	if &batches[0].Monitors[0] == &batches[1].Monitors[0] {
		t.Fatalf("batches share backing array")
	}
}

func readParseError(ch <-chan error) error {
	if ch == nil {
		return nil
	}
	if err, ok := <-ch; ok {
		return err
	}
	return nil
}
