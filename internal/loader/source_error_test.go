package loader

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func TestSemanticErrorOriginalFieldPositions(t *testing.T) {
	for _, test := range []struct{ name, input, field, point string }{
		{"yaml", "# leading comment\nmonitors:\n  - name: café\n    pulse_check:\n      type: tcp\n      interval: 1ns\n      timeout: 1s\n      config: {host: localhost, port: 80}\n", "pulse_check.interval", "line 6 column 17"},
		{"json", "{\n\"monitors\": [\n {\"name\": \"café\",\n  \"pulse_check\": {\n   \"type\": \"tcp\",\n   \"interval\": \"1ns\",\n   \"timeout\": \"1s\",\n   \"config\": {\"host\": \"localhost\", \"port\": 80}}}]}\n", "pulse_check.interval", "line 6 column 16"},
		{"json", "{\"monitors\":[{\"name\":\"\\ud83d\\ude00\",\"pulse_check\":{\"type\":\"tcp\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{\"host\":\"localhost\",\n\"port\":0}}}]}\n", "pulse_check.config.port", "line 2 column 8"},
		{"yaml", "monitors:\n  - name: example\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n    codes:\n      red:\n        notify_group: missing\n", "codes.red.notify_group", "line 6 column 23"},
		{"yaml", "endpoints: {}\nnotification_groups:\n  private-name:\n    - missing\nmonitors: []\n", "notification_groups[0][0]", "line 4 column 7"},
		{"json", "{\"endpoints\":{},\"notification_groups\":{\"private-name\":[\n\"missing\"]},\"monitors\":[]}", "notification_groups[0][0]", "line 2 column 1"},
	} {
		for _, compressed := range []bool{false, true} {
			t.Run(test.name+"/"+test.field+map[bool]string{false: "", true: "/gzip"}[compressed], func(t *testing.T) {
				name := "monitors." + test.name
				if compressed {
					name += ".gz"
				}
				path := filepath.Join(t.TempDir(), name)
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if compressed {
					writer := gzip.NewWriter(file)
					_, err = writer.Write([]byte(test.input))
					if closeErr := writer.Close(); err == nil {
						err = closeErr
					}
				} else {
					_, err = file.WriteString(test.input)
				}
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					t.Fatal(err)
				}
				var batches <-chan MonitorBatch
				var failures <-chan error
				if test.name == "yaml" {
					parser, _ := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1})
					batches, failures = parser.ParseBatches(t.Context(), nil)
				} else {
					parser, _ := NewStreamingJsonParser(path, ParseConfig{BatchSize: 1})
					batches, failures = parser.ParseBatches(t.Context(), nil)
				}
				for range batches {
				}
				err = <-failures
				field, ok := api.ValidationFieldError(err)
				if !ok || field.Field != test.field || !strings.Contains(err.Error(), test.point) || !strings.Contains(err.Error(), path+" document 1") {
					t.Fatalf("wrong original diagnostic: %v; field %+v", err, field)
				}
				if strings.Contains(err.Error(), "private-name") || strings.Contains(err.Error(), "café") {
					t.Fatal("diagnostic copied rejected values", err)
				}
			})
		}
	}
}

func TestCustomDecoderFieldLocations(t *testing.T) {
	for _, test := range []struct{ name, input, field, reason, token string }{
		{"check-type.json", "{\n\"monitors\":[{\"name\":\"private-marker\",\n\"pulse_check\":{\"type\":\"tcp\",\"interval\":\"1s\",\"timeout\":\"1s\",\n\"config\":{\"host\":\"localhost\",\n\"port\":\"private-marker\"}}}]}", "pulse_check.config.port", "invalidType", "\"port\":"},
		{"check-case.json", "{\"monitors\":[{\"name\":\"demo\",\"pulse_check\":{\"type\":\"tcp\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{\"host\":\"localhost\",\n\"PORT\":\"private-marker\"}}}]}", "pulse_check.config.port", "invalidType", "\"PORT\":"},
		{"icmp-count.json", "{\"monitors\":[{\"name\":\"demo\",\"pulse_check\":{\"type\":\"icmp\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{\"host\":\"localhost\",\n\"Count\":\"private-marker\"}}}]}", "pulse_check.config.count", "invalidType", "\"Count\":"},
		{"action-type.json", "{\"monitors\":[{\"name\":\"private-marker\",\n\"pulse_check\":{\"type\":\"tcp\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{\"host\":\"localhost\",\"port\":80}},\n\"intervention\":{\"action\":\"docker\",\"target\":{\"container\":\"demo\",\n\"timeout\":\"private-marker\"}}}]}", "intervention.target.timeout", "positiveDuration", "\"timeout\":\"private-marker\""},
		{"endpoint-type.json", "{\"monitors\":[],\n\"endpoints\":{\"private-marker\":{\"type\":\"log\",\"config\":{\n\"file\":false}}}}", "endpoints[0].config.file", "invalidType", "\"file\":"},
		{"endpoint-selector.yaml", "endpoints:\n  private-marker:\n    type: private-marker\n    config: {}\nmonitors: []\n", "endpoints[0].type", "unsupportedDriver", "type: "},
		{"action-duration.yaml", "monitors:\n  - name: demo\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n    intervention:\n      action: docker\n      target: {container: demo, timeout: private-marker}\n", "intervention.target.timeout", "positiveDuration", "timeout: private-marker"},
		{"duration.json", "{\"monitors\":[{\"name\":\"demo\",\"pulse_check\":{\"type\":\"tcp\",\n\"interval\":\"private-marker\",\"timeout\":\"1s\",\"config\":{\"host\":\"localhost\",\"port\":80}}}]}", "pulse_check.interval", "positiveDuration", "\"interval\":"},
		{"duration.yaml", "monitors:\n  - name: demo\n    pulse_check:\n      type: tcp\n      interval: private-marker\n      config: {host: localhost, port: 80}\n", "pulse_check.interval", "positiveDuration", "interval: "},
		{"selector.json", "{\"monitors\":[{\"name\":\"demo\",\"pulse_check\":{\n\"type\":\"private-marker\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{}}}]}", "pulse_check.type", "unsupportedDriver", "\"type\":"},
		{"selector.yaml", "monitors:\n  - name: demo\n    pulse_check:\n      type: private-marker\n      interval: 1s\n      config: {}\n", "pulse_check.type", "unsupportedDriver", "type: "},
		{"notification.yaml", "monitors:\n  - name: demo\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\n    codes:\n      red:\n        notify: private-marker\n        config: {}\n", "codes.red.notify", "unsupportedDriver", "notify: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parseInvalidInput(t, test.name, test.input)
			detail, ok := api.ValidationFieldError(err)
			// Expected position is the start of the rejected value, independent of
			// any decoder's local offset into a nested configuration object.
			offset := strings.Index(test.input, test.token) + strings.Index(test.token, ":") + 1
			for offset < len(test.input) && test.input[offset] == ' ' {
				offset++
			}
			line := strings.Count(test.input[:offset], "\n") + 1
			column := offset - strings.LastIndex(test.input[:offset], "\n")
			point := fmt.Sprintf("line %d column %d", line, column)
			if !ok || detail.Field != test.field || detail.Reason != test.reason || !strings.Contains(err.Error(), point) {
				t.Fatalf("diagnostic=%v, detail=%+v; want %s %s at %s", err, detail, test.field, test.reason, point)
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("diagnostic retained rejected data: %v", err)
			}
		})
	}
}

func TestSyntaxErrorOriginalSource(t *testing.T) {
	for _, test := range []struct{ name, input, reason, point string }{
		{"syntax.json", "{\"monitors\":[\n{},\n{\"name\": invalid}\n]}", "invalidJSON", "document 1 monitor 2 at line 3"},
		{"metadata.json", "{\"monitors\":[],\n\"endpoints\":{\n\"private-marker\":invalid}}", "invalidJSON", "document 1 at line 3 column 18"},
		{"syntax.json.gz", "{\"monitors\":[\n{},\n{\"name\": invalid}\n]}", "invalidJSON", "document 1 monitor 2 at line 3 column 10"},
		{"escape.json", "{\"monitors\":[\n{},\n{\"name\": \"bad\\q\"}\n]}", "invalidJSON", "document 1 monitor 2 at line 3 column 15"},
		{"number.json", "{\"monitors\":[\n{},\n{\"name\": 123e+bad}\n]}", "invalidJSON", "document 1 monitor 2 at line 3 column 15"},
		{"refill.json", "{\"monitors\":[\n{\"name\":\"" + strings.Repeat("a", 70_000) + "\"},\n{\"name\": invalid}\n]}", "invalidJSON", "document 1 monitor 2 at line 3 column 10"},
		{"comma.json", "{\"monitors\":[\n,invalid]}", "invalidJSON", "line 2 column 1"},
		{"repeated-comma.json", "{\"monitors\":[\n{},\n,invalid]}", "invalidJSON", "line 3 column 1"},
		{"unexpected-colon.json", "{\"monitors\":[\n{},\n:invalid]}", "invalidJSON", "line 3 column 1"},
		{"repeated-colon.json", "{\"monitors\":[\n{},\n{\"name\": :invalid}]}", "invalidJSON", "line 3 column 10"},
		{"whitespace.json", "{\"monitors\":[\n{},\n{\"name\": \n \tinvalid}]}", "invalidJSON", "document 1 monitor 2 at line 4 column 3"},
		{"colon.json", "{\"monitors\":[\n{},\n{\"name\"\ntrue}\n]}", "invalidJSON", "document 1 monitor 2 near line 4 column 1 (parser context)"},
		{"colon-escape.json", "{\"monitors\":[\n{},\n{\"name\"\n\"bad\\q\"}\n]}", "invalidJSON", "document 1 monitor 2 near line 4 column 1 (parser context)"},
		{"truncated.json", "{\"monitors\":[\n{},\n{\"name\": \"unfinished", "invalidJSON", "document 1 monitor 2 near line 3 column 10 (parser context)"},
		{"bounded-probe.json", "{\"monitors\":[\n{},\n{\"name\": \"" + strings.Repeat("a", 1<<20) + "\\q\"}\n]}", "invalidJSON", "document 1 monitor 2 near line 3"},
		{"metadata.yaml", "monitors:\n  - name: demo\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\nendpoints: [}\n", "invalidYAML", "document 1: near line 4"},
		{"syntax.yaml", "monitors:\n  - name: demo\n    pulse_check:\n      config: [}\n", "invalidYAML", "document 1 monitor 1: near line 3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parseInvalidInput(t, test.name, test.input)
			detail, ok := api.ValidationFieldError(err)
			if !ok || detail.Reason != test.reason || !strings.Contains(err.Error(), test.point) {
				t.Fatalf("diagnostic=%v detail=%+v; want %s at %s", err, detail, test.reason, test.point)
			}
			if strings.Contains(err.Error(), "unfinished") || strings.Contains(err.Error(), "bad") {
				t.Fatalf("diagnostic retained rejected data: %v", err)
			}
			if test.name == "bounded-probe.json" && !strings.Contains(err.Error(), "(parser context)") {
				t.Fatalf("bounded probe claimed an exact location: %v", err)
			}
		})
	}
	err := parseInvalidInput(t, "duplicate.yaml", "monitors:\n  - name: first\n    name: private-marker\n    pulse_check: {type: tcp, interval: 1s, config: {host: localhost, port: 80}}\n")
	if detail, ok := api.ValidationFieldError(err); !ok || detail.Reason != "duplicateField" || !strings.Contains(err.Error(), "line 3 column 5") || strings.Contains(err.Error(), "private-marker") {
		t.Fatalf("duplicate-key diagnostic lost its rule/location or exposed data: %v", err)
	}
	err = parseInvalidInput(t, "unknown.yaml", "unexpected: {type: private-marker}\nmonitors: []\n")
	if detail, ok := api.ValidationFieldError(err); !ok || detail.Reason != "unknownField" || strings.Contains(detail.Field, "endpoints") || strings.Contains(err.Error(), "private-marker") {
		t.Fatalf("missing endpoint map changed the original failure: %v", err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := jsonDecoderError(newJSONDecoder(strings.NewReader("")), cause, ""); !errors.Is(got, cause) {
			t.Fatalf("cancellation changed: %v", got)
		}
	}
}

func parseInvalidInput(t *testing.T, name, input string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	data := []byte(input)
	if strings.HasSuffix(name, ".gz") {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		data = compressed.Bytes()
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var batches <-chan MonitorBatch
	var failures <-chan error
	if strings.HasSuffix(strings.TrimSuffix(name, ".gz"), ".yaml") {
		parser, _ := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1})
		batches, failures = parser.ParseBatches(t.Context(), nil)
	} else {
		parser, _ := NewStreamingJsonParser(path, ParseConfig{BatchSize: 1})
		batches, failures = parser.ParseBatches(t.Context(), nil)
	}
	for range batches {
	}
	err := <-failures
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing source diagnostic: %v", err)
	}
	return err
}

func TestYAMLDurationAliases(t *testing.T) {
	for _, check := range []string{
		"type: tcp\n      interval: &period 1s\n      timeout: *period\n      config: {host: localhost, port: 80}",
		"<<: &defaults {type: tcp, interval: &period 1s, timeout: *period}\n      config: {host: localhost, port: 80}",
	} {
		path := filepath.Join(t.TempDir(), "aliases.yaml")
		if err := os.WriteFile(path, []byte("monitors:\n  - name: demo\n    pulse_check:\n      "+check+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		parser, _ := NewStreamingYamlParser(path, ParseConfig{BatchSize: 1})
		batches, failures := parser.ParseBatches(t.Context(), nil)
		count := 0
		for batch := range batches {
			count += len(batch.Monitors)
			for _, monitor := range batch.Monitors {
				if monitor.Pulse.Interval != time.Second || monitor.Pulse.Timeout != time.Second {
					t.Fatalf("duration alias changed decoded values: %+v", monitor.Pulse)
				}
			}
		}
		if err := <-failures; err != nil || count != 1 {
			t.Fatalf("same-entry duration aliases rejected: count=%d error=%v", count, err)
		}
	}
}

func TestRuleTargetDiagnosticsKeepSourceSpellingAndCanonicalCause(t *testing.T) {
	for _, jsonInput := range []bool{false, true} {
		root := t.TempDir()
		path := filepath.Join(root, "monitor.yaml")
		raw := []byte("intervention:\n  rules:\n    - target:\n        operation_timeout: -1s\n")
		if jsonInput {
			raw = []byte(`{"intervention":{"rules":[{"target":{"operation_timeout":"-1s"}}]}}`)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		canonical := "intervention.rules[0].driver.config.operationTimeout"
		cause := api.InvalidField(canonical, "positiveDuration")
		var err error
		if jsonInput {
			err = jsonSourceError(t.Context(), path, 1, raw, int64(len(raw)), cause)
		} else {
			var node yaml.Node
			if e := yaml.Unmarshal(raw, &node); e != nil {
				t.Fatal(e)
			}
			err = yamlSourceError(t.Context(), path, 1, &node, cause)
		}
		if !strings.Contains(err.Error(), "intervention.rules[0].target.operation_timeout") || strings.Contains(err.Error(), "operationTimeout") || strings.Contains(err.Error(), "-1s") {
			t.Fatal("unsafe or canonical-only source presentation", err)
		}
		detail, ok := api.ValidationFieldError(err)
		if !ok || detail.Field != canonical {
			t.Fatal("typed cause changed", detail)
		}
		if !jsonInput && !strings.Contains(err.Error(), "line 4 column 28") {
			t.Fatal("rule target lost position", err)
		}
	}
}
