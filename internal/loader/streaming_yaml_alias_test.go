package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamingYAMLAliasesAndBOM(t *testing.T) {
	const entry = "monitors:\n- name: first\n  pulse_check: &pulse {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n- name: second\n  pulse_check: *pulse\n"
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"cross-entry", entry, true}, {"bom", "\ufeff" + entry, true},
		{"forward", strings.Replace(entry, "pulse_check: &pulse", "pulse_check: *pulse\n  unused: &pulse", 1), false},
		{"cross-document", entry + "---\nversion: 2\n", false},
		{"metadata-before", "notification_groups: {team: [&name ops]}\n" + strings.Replace(entry, "name: first", "name: *name", 1) + "endpoints: {ops: {type: log, config: {file: events.jsonl}}}\n", true},
		{"metadata-after", strings.Replace(entry, "name: first", "name: *name", 1) + "notification_groups: {team: [&name ops]}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(file, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			parser, _ := NewStreamingYamlParser(file, ParseConfig{BatchSize: 1, StrictUnknownFields: true})
			batches, errors := parser.ParseBatches(t.Context(), nil)
			count := 0
			for batch := range batches {
				count += len(batch.Monitors)
			}
			err := <-errors
			if (err == nil) != tc.valid || tc.valid && count != 2 {
				t.Fatal("unexpected parser result", count, err)
			}
		})
	}
}

func TestStreamingYAMLRawAndExpandedQuotas(t *testing.T) {
	const raw = "monitors:\n- name: first\n  pulse_check: &pulse {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n"
	for _, tc := range []struct {
		name, input      string
		memory, expanded int64
		valid            bool
	}{
		{"raw-at-limit", raw, int64(len(raw)), 1, true},
		{"raw-over-limit", raw, int64(len(raw) - 1), 1 << 20, false},
		{"expanded-over-limit", raw + "- name: second\n  pulse_check: *pulse\n", 1 << 20, 1, false},
		{"expanded-within-limit", raw + "- name: second\n  pulse_check: *pulse\n", 1 << 20, 1 << 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			parser, err := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1, MaxMemory: tc.memory, MaxExpandedBytes: tc.expanded})
			if err != nil {
				t.Fatal(err)
			}
			batches, failures := parser.ParseBatches(t.Context(), nil)
			for range batches {
			}
			if err := <-failures; (err == nil) != tc.valid {
				t.Fatalf("quota acceptance: %v", err)
			}
		})
	}
}

func TestJSONInputPositionIgnoresBOM(t *testing.T) {
	const raw = "{\n  \"monitors\": []\n}\n"
	for _, prefix := range []string{"", "\ufeff"} {
		path := filepath.Join(t.TempDir(), "monitors.json")
		if err := os.WriteFile(path, []byte(prefix+raw), 0600); err != nil {
			t.Fatal(err)
		}
		line, column := inputPosition(t.Context(), path, 4)
		if line != 2 || column != 3 {
			t.Fatal("BOM changed original coordinates", line, column)
		}
	}
}
