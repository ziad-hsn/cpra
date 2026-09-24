//go:build externaljobs

package runtimeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeExternalJobsRequireExplicitDurableManagement(t *testing.T) {
	config := Default()
	if config.ExternalJobs.Enabled || config.Validate() != nil {
		t.Fatal("tagged build enabled external jobs implicitly")
	}
	for _, test := range []struct {
		name string
		edit func(*Config)
	}{
		{"management-disabled", func(c *Config) { c.Management = Management{} }},
		{"memory", func(c *Config) { c.Storage.Mode = "memory" }},
		{"memory-ephemeral", func(c *Config) { c.Storage.Mode = "memory"; c.Management.Encryption = nil }},
		{"no-wrapping-source", func(c *Config) { c.Management.Encryption = nil }},
		{"no-protected-transport", func(c *Config) { c.Management.TLS = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := managementConfigFixture(t)
			c.ExternalJobs.Enabled = true
			test.edit(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("external jobs accepted incomplete durable management")
			}
		})
	}
	config = managementConfigFixture(t)
	config.ExternalJobs.Enabled = true
	if err := config.Validate(); err != nil {
		t.Fatal("explicit protected durable configuration rejected", err)
	}
	if _, err := os.Stat(filepath.Dir(config.Management.PolicyFile)); !os.IsNotExist(err) {
		t.Fatal("structural validation touched absent sources", err)
	}
	raw, err := yaml.Marshal(config)
	if err != nil || !strings.Contains(string(raw), "external_jobs:") {
		t.Fatal("tagged YAML omitted explicit configuration", err)
	}
	parsed, err := loadManagementYAML(t, string(raw))
	if err != nil || !parsed.ExternalJobs.Enabled {
		t.Fatal("tagged extension did not round trip", err)
	}
	if _, err := os.Stat(config.Storage.Directory); !os.IsNotExist(err) {
		t.Fatal("configuration load opened durable state", err)
	}
	public, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(public, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["ExternalJobs"] != nil || fields["external_jobs"] != nil {
		t.Fatal("process extension entered public configuration JSON", err)
	}
}

func TestRuntimeExternalJobsDisabledPreservesMemoryMode(t *testing.T) {
	for _, setting := range []string{"", "external_jobs: {enabled: false}\n", "external_jobs: {}\n"} {
		config, err := loadManagementYAML(t, "storage: {mode: memory}\n"+setting)
		if err != nil || config.ExternalJobs.Enabled || config.Storage.Mode != "memory" {
			t.Fatal("disabled extension changed disposable configuration", err)
		}
	}
	config := managementConfigFixture(t)
	config.Storage.Mode = "memory"
	config.Management.Encryption = nil
	if err := config.Validate(); err != nil || config.ExternalJobs.Enabled {
		t.Fatal("disabled extension changed ephemeral management", err)
	}
}

func TestRuntimeExternalJobsStrictBooleanAndRedactedErrors(t *testing.T) {
	for _, value := range []string{`"private-invalid-value"`, `1`, `yes`, `null`, `[true]`, `{value: true}`} {
		_, err := loadManagementYAML(t, "external_jobs: {enabled: "+value+"}\n")
		if err == nil || strings.Contains(err.Error(), "private-invalid-value") {
			t.Fatal("invalid opt-in value accepted or disclosed", err)
		}
	}
	for _, input := range []string{
		"external_jobs: {enabled: false, private-unknown-setting: secret-value}\n",
		"external_jobs: {enabled: false, enabled: true}\n",
		"external_jobs: private-invalid-value\n",
		"external_jobs: [true]\n",
	} {
		_, err := loadManagementYAML(t, input)
		if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "secret-value") {
			t.Fatal("invalid extension shape accepted or disclosed", err)
		}
	}
}
