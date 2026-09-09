package streaming

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStrictParserPreservesValidSyntax(t *testing.T) {
	fixtures := map[string]struct{ ext, data string }{
		"json-surrogate-pair": {"json", `{"monitors":[{"name":"monitor-\ud83d\ude00","pulse_check":{"type":"http","interval":"1s","timeout":"1s","config":{"url":"http://127.0.0.1"}}}]}`},
		"yaml-overridden-merge": {"yaml", `monitors:
  - name: fixture
    pulse_check:
      <<: &defaults
        type: http
        interval: 1s
        timeout: 1s
        config: {url: "http://127.0.0.1"}
      type: tcp
      config: {host: "127.0.0.1", port: 80}
`},
	}
	for name, tt := range fixtures {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config."+tt.ext)
			if err := os.WriteFile(p, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			for _, strict := range []bool{false, true} {
				var batches <-chan MonitorBatch
				var errs <-chan error
				if tt.ext == "json" {
					parser, _ := NewStreamingJsonParser(p, ParseConfig{StrictUnknownFields: strict})
					batches, errs = parser.ParseBatches(context.Background(), nil)
				} else {
					parser, _ := NewStreamingYamlParser(p, ParseConfig{StrictUnknownFields: strict})
					batches, errs = parser.ParseBatches(context.Background(), nil)
				}
				n := 0
				for batch := range batches {
					n += len(batch.Monitors)
				}
				for err := range errs {
					t.Errorf("strict=%v valid configuration rejected: %v", strict, err)
				}
				if n != 1 {
					t.Errorf("strict=%v loaded %d monitors", strict, n)
				}
			}
		})
	}
}
