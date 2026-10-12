package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorTableReportsObservedState(t *testing.T) {
	var resources []api.Resource
	for _, health := range []string{"healthy", "unhealthy", "warning", "unrecognized", ""} {
		r := cliResource("Monitor", "monitor-"+health)
		r.Status, _ = json.Marshal(api.MonitorStatus{Health: health, LastCheckedAt: time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)})
		resources = append(resources, r)
	}
	data, _ := json.Marshal(map[string]any{"items": resources})
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := writeManagementTable(cmd, &options{}, data, false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"OBSERVED HEALTH", "up", "down", "degraded", "unknown"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatal("missing operational state", expected, out.String())
		}
	}
}

func TestMonitorDescribeDoesNotPrintRawConfigurationOrZeroTimes(t *testing.T) {
	r := cliResource("Monitor", "service")
	r.Metadata.Name = api.Pointer("name\x1b[31m\nforged-row")
	r.Status, _ = json.Marshal(api.MonitorStatus{Health: "healthy"})
	data, _ := json.Marshal(r)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := writeManagementTable(cmd, &options{}, data, true); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"0001-01-01", "https://example.test/health", "\x1b", "\nforged-row", `{"check"`} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatal("unsafe or raw human describe output", forbidden)
		}
	}
	for _, expected := range []string{"Check driver:", "http", "unknown", "unavailable"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatal("missing descriptive field", expected)
		}
	}
}

func TestTableAndKeyValueEscapeTerminalControls(t *testing.T) {
	for _, render := range []func(*bytes.Buffer) error{
		func(w *bytes.Buffer) error {
			return table(w, []string{"NAME"}, [][]string{{"unsafe\x1b[31m\nnext\tcolumn\u202e"}})
		},
		func(w *bytes.Buffer) error { return kv(w, [][2]string{{"name", "unsafe\x1b[31m\nnext\tcolumn\u202e"}}) },
	} {
		var out bytes.Buffer
		if err := render(&out); err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(out.String(), "\x1b\u202e") || strings.Contains(out.String(), "\nnext") {
			t.Fatal("terminal controls reached output")
		}
	}
}
