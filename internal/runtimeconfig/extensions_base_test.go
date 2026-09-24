//go:build !externaljobs

package runtimeconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeExtensionsExcludedFromBaseBuild(t *testing.T) {
	if _, exists := reflect.TypeFor[Config]().FieldByName("ExternalJobs"); exists {
		t.Fatal("base build exposes custom-job configuration")
	}
	for _, input := range []string{"external_jobs: {enabled: true}\n", "external_jobs: {enabled: false}\n", "external_jobs: {}\n", "external_jobs: null\n"} {
		if _, err := loadManagementYAML(t, input); err == nil || !strings.Contains(err.Error(), "external_jobs") {
			t.Fatal("base build accepted extension configuration", err)
		}
	}
	config := Default()
	for _, mode := range []string{"raft", "memory"} {
		config.Storage.Mode = mode
		if err := config.Validate(); err != nil {
			t.Fatal("extension exclusion changed ordinary configuration", err)
		}
		for _, encode := range []func(any) ([]byte, error){yaml.Marshal, json.Marshal} {
			raw, err := encode(config)
			if err != nil || strings.Contains(string(raw), "external_jobs") || strings.Contains(string(raw), "ExternalJobs") {
				t.Fatal("base configuration serialization contains extension settings", err)
			}
		}
	}
}
