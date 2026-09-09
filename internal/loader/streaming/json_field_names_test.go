package streaming

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cpra/internal/loader/schema"
)

func TestStrictJSONPreservesDecodedFieldNames(t *testing.T) {
	cases := map[string]string{
		"untagged-tcp-fields":         `{"monitors":[{"name":"fixture","pulse_check":{"type":"tcp","interval":"1s","timeout":"1s","config":{"Host":"127.0.0.1","Port":80}}}]}`,
		"json-name-differs-yaml-name": `{"monitors":[{"name":"fixture","pulse_check":{"type":"icmp","interval":"1s","timeout":"1s","config":{"host":"127.0.0.1","privilege":true}}}]}`,
		"tagged-json-case-folding":    `{"monitors":[{"name":"fixture","pulse_check":{"type":"http","interval":"1s","timeout":"1s","config":{"URL":"http://127.0.0.1","EXPECTED_STATUS":[418]}}}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			for _, strict := range []bool{false, true} {
				parser, _ := NewStreamingJsonParser(path, ParseConfig{StrictUnknownFields: strict})
				batches, errs := parser.ParseBatches(context.Background(), nil)
				var monitors []schema.Monitor
				for batch := range batches {
					monitors = append(monitors, batch.Monitors...)
				}
				for err := range errs {
					t.Errorf("strict=%v: %v", strict, err)
				}
				if len(monitors) != 1 {
					t.Errorf("strict=%v: loaded %d", strict, len(monitors))
					continue
				}
				switch cfg := monitors[0].Pulse.Config.(type) {
				case *schema.PulseTCPConfig:
					if cfg.Host != "127.0.0.1" || cfg.Port != 80 {
						t.Errorf("wrong TCP config: %+v", cfg)
					}
				case *schema.PulseICMPConfig:
					if !cfg.Privilege {
						t.Errorf("JSON privilege field was not decoded")
					}
				case *schema.PulseHTTPConfig:
					if cfg.Url != "http://127.0.0.1" || len(cfg.ExpectedStatus) != 1 || cfg.ExpectedStatus[0] != 418 {
						t.Errorf("wrong HTTP config: %+v", cfg)
					}
				}
			}
		})
	}
}
