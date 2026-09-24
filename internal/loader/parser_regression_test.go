package loader

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

func TestYAMLCommentOnlyMetadataNeedsNoDocument(t *testing.T) {
	for _, test := range []struct {
		name, input string
		count       int
		wantError   bool
	}{
		{"starter", "# Intentionally empty. Add monitors when ready.\n# No provider credentials are imported.\nmonitors: []\n", 0, false},
		{"comments around monitors", "# Operator comment\n\nmonitors:\n  - name: example\n    pulse_check:\n      type: http\n      interval: 1m\n      timeout: 5s\n      config: {url: http://127.0.0.1}\n# Another comment\n", 1, false},
		{"malformed metadata", "# Operator comment\nversion: [\nmonitors: []\n", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			parser, err := NewStreamingYamlParser(path, ParseConfig{StrictUnknownFields: true})
			if err != nil {
				t.Fatal(err)
			}
			batches, errs := parser.ParseBatches(context.Background(), nil)
			count := 0
			for batch := range batches {
				count += len(batch.Monitors)
			}
			parseErr := <-errs
			if (parseErr != nil) != test.wantError || count != test.count {
				t.Fatalf("count=%d error=%v; want count=%d error=%v", count, parseErr, test.count, test.wantError)
			}
		})
	}
}
