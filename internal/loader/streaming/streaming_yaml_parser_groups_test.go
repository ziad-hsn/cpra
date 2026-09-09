package streaming

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamingYamlParserEndpointsAndGroups(t *testing.T) {
	dir := t.TempDir()
	yamlContent := []byte(`version: 2
endpoints:
  ops_email:
    type: email
    config:
      to: ops@example.com
  status_hook:
    type: webhook
    config:
      url: https://hooks.example.com
notification_groups:
  oncall: [ops_email, status_hook]
monitors:
  - name: "api"
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config:
        url: "https://api.example.com/health"
    codes:
      red:
        dispatch: true
        notify_group: oncall
`)

	file := filepath.Join(dir, "monitors.yaml")
	if err := os.WriteFile(file, yamlContent, 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}

	parser, err := NewStreamingYamlParser(file, ParseConfig{BatchSize: 10})
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

	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	batch := batches[0]

	if len(batch.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(batch.Endpoints))
	}
	if batch.Endpoints["ops_email"].Type != "email" {
		t.Fatalf("expected email endpoint, got %q", batch.Endpoints["ops_email"].Type)
	}
	if len(batch.NotificationGroups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(batch.NotificationGroups))
	}
	if got := batch.NotificationGroups["oncall"]; len(got) != 2 || got[0] != "ops_email" || got[1] != "status_hook" {
		t.Fatalf("unexpected oncall group: %v", got)
	}

	mon := batch.Monitors[0]
	if mon.Codes["red"].NotifyGroup != "oncall" {
		t.Fatalf("expected red notify_group oncall, got %q", mon.Codes["red"].NotifyGroup)
	}
}
