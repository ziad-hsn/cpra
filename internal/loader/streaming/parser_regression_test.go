package streaming

import (
	"context"
	"encoding/json"
	"github.com/mlange-42/ark/ecs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestYAMLStreamingPreservesBlockTextAndLateMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	body := "first\n\n# literal comment\nmonitors: []\nlast\n"
	data := `version: 2
monitors:
  - name: text
    enabled: true
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config:
        url: http://127.0.0.1
        body: |
          first

          # literal comment
          monitors: []
          last
    codes:
      red:
        dispatch: true
        notify_group: ops
endpoints:
  sink:
    type: webhook
    config: {url: "http://127.0.0.1/alert"}
notification_groups:
  ops: [sink]
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1, StrictUnknownFields: true})
	batches, errs := p.ParseBatches(context.Background(), nil)
	count := 0
	for batch := range batches {
		count += len(batch.Monitors)
		cfg := batch.Monitors[0].Pulse.Config
		if !strings.Contains(stringMustJSON(t, cfg), `first\n\n# literal comment\nmonitors: []\nlast\n`) {
			t.Fatalf("block scalar changed, want %q; config=%s", body, stringMustJSON(t, cfg))
		}
		if len(batch.NotificationGroups["ops"]) != 1 || len(batch.Endpoints) != 1 {
			t.Fatal("metadata after monitors lost")
		}
	}
	for err := range errs {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d monitors", count)
	}
}
func TestJSONRejectsTruncatedAndTrailingDocuments(t *testing.T) {
	for _, input := range []string{`{"monitors":[]`, `{"monitors":[]} {}`, `{"monitors":[],"monitors":[]}`} {
		pth := filepath.Join(t.TempDir(), "input.json")
		if err := os.WriteFile(pth, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		p, _ := NewStreamingJsonParser(pth, ParseConfig{})
		batches, errs := p.ParseBatches(context.Background(), nil)
		for range batches {
		}
		if err := <-errs; err == nil {
			t.Fatalf("accepted malformed input %q", input)
		}
	}
}
func TestLoaderCancelsParserAfterUnsupportedDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.yaml")
	entry := `  - name: fixture
    pulse_check:
      type: http
      interval: 1s
      timeout: 1s
      config: {url: "http://127.0.0.1"}
    codes:
      red:
        dispatch: true
        notify: slack
        config: {hook: ""}
`
	if err := os.WriteFile(path, []byte("monitors:\n"+strings.Repeat(entry, 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	w := ecs.NewWorld()
	cfg := DefaultStreamingConfig()
	cfg.ParseBatchSize = 1
	cfg.PreAllocateCount = 1
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := NewStreamingLoader(path, &w, cfg).Load(ctx); err == nil {
		t.Fatal("accepted unsupported configuration")
	}
	if ctx.Err() != nil {
		t.Fatal("loader waited for timeout instead of cancelling its parser")
	}
}
func TestDecompressedInputBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(`{"padding":"`+strings.Repeat("x", 4096)+`","monitors":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := NewStreamingJsonParser(path, ParseConfig{MaxMemory: 1024})
	batches, errs := p.ParseBatches(context.Background(), nil)
	for range batches {
	}
	if err := <-errs; err == nil {
		t.Fatal("ignored input budget")
	}
}

func stringMustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
