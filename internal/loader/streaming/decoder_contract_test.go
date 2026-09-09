package streaming

import (
	"context"
	"cpra/internal/loader/schema"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONInterventionDurationString(t *testing.T) {
	p := filepath.Join(t.TempDir(), "monitor.json")
	data := `{"monitors":[{"name":"fixture","pulse_check":{"type":"http","interval":"1s","timeout":"1s","config":{"url":"http://127.0.0.1"}},"intervention":{"action":"webhook","target":{"url":"http://127.0.0.1/restart","timeout":"30s"}}}]}`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	parser, _ := NewStreamingJsonParser(p, ParseConfig{})
	b, e := parser.ParseBatches(context.Background(), nil)
	count := 0
	for batch := range b {
		for _, monitor := range batch.Monitors {
			count++
			target, ok := monitor.Intervention.Target.(*schema.InterventionTargetWebhook)
			if !ok || target.Timeout != 30*time.Second {
				t.Fatalf("webhook timeout was not preserved: %#v", monitor.Intervention.Target)
			}
		}
	}
	for err := range e {
		t.Error(err)
	}
	if count != 1 {
		t.Fatalf("loaded %d monitors, want 1", count)
	}
}
func TestStrictUnknownMonitorFields(t *testing.T) {
	fixtures := map[string]string{
		"json": `{"monitors":[{"name":"fixture","pulse_check":{"type":"http","interval":"1s","timeout":"1s","config":{"url":"http://127.0.0.1","expected_sttaus":[418]}}}]}`,
		"yaml": "monitors:\n  - name: fixture\n    pulse_check:\n      type: http\n      interval: 1s\n      timeout: 1s\n      config:\n        url: http://127.0.0.1\n        expected_sttaus: [418]\n",
	}
	for ext, data := range fixtures {
		t.Run(ext, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "monitor."+ext)
			if err := os.WriteFile(p, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var b <-chan MonitorBatch
			var e <-chan error
			if ext == "json" {
				parser, _ := NewStreamingJsonParser(p, ParseConfig{StrictUnknownFields: true})
				b, e = parser.ParseBatches(context.Background(), nil)
			} else {
				parser, _ := NewStreamingYamlParser(p, ParseConfig{StrictUnknownFields: true})
				b, e = parser.ParseBatches(context.Background(), nil)
			}
			count := 0
			for batch := range b {
				count += len(batch.Monitors)
			}
			gotError := false
			for range e {
				gotError = true
			}
			if !gotError {
				t.Errorf("strict parser accepted misspelled expected_sttaus and loaded %d monitor(s)", count)
			}
		})
	}
}
