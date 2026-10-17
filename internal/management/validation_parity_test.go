package management

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestSharedDriverSettingsCorpus(t *testing.T) {
	data, err := os.ReadFile("../../sdk/go/api/testdata/driver-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Category, Driver, Field, Reason string
		Config                                json.RawMessage
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			driver := api.DriverConfig{Type: test.Driver, Config: test.Config}
			config, err := decodeRuntimeDriver(test.Category, driver)
			if err != nil {
				t.Fatal("corpus must use representable shapes", err)
			}
			monitor := manifest.Monitor{Name: "corpus", Pulse: manifest.Pulse{Type: "tcp", Interval: time.Second, Timeout: time.Second, Config: &manifest.PulseTCPConfig{Host: "localhost", Port: 80}}}
			prefix := "pulse_check"
			switch test.Category {
			case "check":
				monitor.Pulse.Type, monitor.Pulse.Config = test.Driver, config.(manifest.PulseConfig)
			case "recovery":
				monitor.Intervention = manifest.Intervention{Action: test.Driver, Target: config.(manifest.InterventionTarget)}
				prefix = "intervention"
			case "notification":
				monitor.Codes = manifest.Codes{"red": manifest.CodeConfig{Notify: test.Driver, Config: config.(manifest.CodeNotification)}}
				prefix = "codes.red"
			}
			results := []struct {
				name  string
				err   error
				field string
			}{
				{"SDK", api.ValidateDriverSettings(test.Category, driver), test.Field},
				{"management", ValidateResolvedDriver(test.Category, driver), test.Field},
				{"file", manifest.ValidateMonitor(&monitor), prefix + "." + test.Field},
			}
			for _, result := range results {
				if test.Reason == "" {
					if result.err != nil {
						t.Error(result.name, result.err)
					}
					continue
				}
				field, ok := api.ValidationFieldError(result.err)
				if !ok || field.Field != result.field || field.Reason != test.Reason {
					t.Error(result.name, "lost rule/location", result.err)
				}
				if result.err != nil && strings.Contains(result.err.Error(), "private-marker") {
					t.Error(result.name, "leaked value")
				}
				if result.name == "management" && !errors.Is(result.err, ErrValidation) {
					t.Error("lost management error classification")
				}
			}
		})
	}
}

func TestMonitorSettingsRetainTypedCause(t *testing.T) {
	err := ValidateMonitorSettings(api.MonitorSpec{Check: api.CheckSpec{Interval: "1ms", Timeout: "1s"}})
	field, ok := api.ValidationFieldError(err)
	if !errors.Is(err, ErrValidation) || !ok || field.Field != "check.interval" || field.Reason != "minimumInterval" {
		t.Fatal(err)
	}
}
