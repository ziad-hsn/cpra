package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStorageEncryptionSelectionAndBoundaries(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			c := managementConfigFixture(t)
			c.Storage.Encryption = c.Management.Encryption
			c.Management.Encryption = nil
			if !managed {
				c.Management = Management{}
			}
			if err := c.Validate(); err != nil || c.EncryptionSource() != c.Storage.Encryption {
				t.Fatal("storage keyring was not selected", err)
			}
			raw, err := yaml.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := loadManagementYAML(t, string(raw))
			if err != nil || parsed.EncryptionSource().Local.ActiveKeyFile != c.EncryptionSource().Local.ActiveKeyFile || parsed.Management.Enabled != managed {
				t.Fatal("storage configuration did not round trip", err)
			}
			public, _ := json.Marshal(c)
			for _, text := range []string{string(public), fmt.Sprintf("%#v", c)} {
				if strings.Contains(text, c.EncryptionSource().Local.ActiveKeyFile) {
					t.Fatal("configuration exposed key source")
				}
			}
			c.Management.Encryption = c.Storage.Encryption
			if err := c.Validate(); err == nil {
				t.Fatal("two source locations accepted")
			}
			c.Management.Encryption = nil
			c.Storage.Encryption.Local.ActiveKeyFile = filepath.Join(c.Storage.Directory, "wrapping.key")
			if err := c.Validate(); err == nil || !strings.HasPrefix(err.Error(), "storage") {
				t.Fatal("key inside state directory accepted", err)
			}
		})
	}
}

func TestStorageEncryptionDecoderRedactsInvalidInputs(t *testing.T) {
	for _, input := range []string{
		"storage: {encryption: {private-canary: true}}",
		"storage: {encryption: {backend: [private-canary]}}",
		"storage: {encryption: {local: {active_key_file: [private-canary]}}}",
		"storage: {encryption: {local: {active_key_file: private-canary, private-canary: 1}}}",
		"storage: {encryption: null}",
	} {
		_, err := loadManagementYAML(t, input)
		if err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatal("invalid encryption input accepted or echoed", err)
		}
	}
	c, err := loadManagementYAML(t, "storage: {mode: memory}")
	if err != nil || c.Storage.BatchSize != Default().Storage.BatchSize || c.Storage.SnapshotInterval != Default().Storage.SnapshotInterval {
		t.Fatal("partial storage configuration erased defaults", err)
	}
}
