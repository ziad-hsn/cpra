package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamingBatchesRespectDecodedPayloadBytes(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var records []map[string]any
			for i := 0; i < 30; i++ {
				records = append(records, map[string]any{"name": fmt.Sprintf("monitor-%d", i), "pulse_check": map[string]any{"type": "http", "interval": "60s", "timeout": "5s", "config": map[string]any{"url": "https://example.test/" + strings.Repeat("x", 8192)}}})
			}
			var data []byte
			if format == "json" {
				data, _ = json.Marshal(map[string]any{"monitors": records})
			} else {
				var text strings.Builder
				text.WriteString("monitors:\n")
				for _, record := range records {
					raw, _ := json.Marshal(record)
					text.WriteString("- ")
					text.Write(raw)
					text.WriteByte('\n')
				}
				data = []byte(text.String())
			}
			path := filepath.Join(t.TempDir(), "monitors."+format)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			config := ParseConfig{BatchSize: 500, MaxBatchBytes: 32 << 10, BufferSize: 32 << 10}
			var parser interface {
				ParseBatches(context.Context, chan<- Progress) (<-chan MonitorBatch, <-chan error)
			}
			var err error
			if format == "json" {
				parser, err = NewStreamingJsonParser(path, config)
			} else {
				parser, err = NewStreamingYamlParser(path, config)
			}
			if err != nil {
				t.Fatal(err)
			}
			batches, failures := parser.ParseBatches(t.Context(), nil)
			count, batchCount := 0, 0
			for batch := range batches {
				if batch.DecodedBytes <= 0 || batch.DecodedBytes > 32<<10 {
					t.Fatal("byte-bound batch exceeded", batch.DecodedBytes)
				}
				for _, monitor := range batch.Monitors {
					if monitor.Name != fmt.Sprintf("monitor-%d", count) {
						t.Fatal("order changed", monitor.Name)
					}
					count++
				}
				batchCount++
			}
			if err := <-failures; err != nil {
				t.Fatal(err)
			}
			if count != 30 || batchCount < 2 {
				t.Fatal("byte cap was not applied", count, batchCount)
			}
		})
	}
}

func TestStreamingJSONBOMPreservesMonitor(t *testing.T) {
	input := "\ufeff" + `{"monitors":[{"name":"api","pulse_check":{"type":"tcp","interval":"60s","timeout":"5s","config":{"host":"localhost","port":80}}}]}`
	path := filepath.Join(t.TempDir(), "monitors.json")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	parser, err := NewStreamingJsonParser(path, ParseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	batches, failures := parser.ParseBatches(t.Context(), nil)
	count := 0
	for batch := range batches {
		for _, monitor := range batch.Monitors {
			if monitor.Name != "api" {
				t.Fatal("wrong monitor")
			}
			count++
		}
	}
	if err := <-failures; err != nil || count != 1 {
		t.Fatal("BOM rejected or input lost", err, count)
	}
}
