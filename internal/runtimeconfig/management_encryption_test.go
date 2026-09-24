package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManagementTransitExplicitReaderPolicy(t *testing.T) {
	config := managementConfigFixture(t)
	source := &ManagementTransit{Address: "https://bao.example", Mount: "transit", Key: "cpra", TokenFile: filepath.Join(filepath.Dir(config.Management.PolicyFile), "bootstrap-token")}
	config.Management.Encryption = &ManagementEncryption{Backend: "openbao-transit", Transit: source}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		source.ReaderSID = "S-1-5-80-123-456-789-101-112"
	} else {
		group := 123
		source.ReaderGroupID = &group
	}
	if err := config.Validate(); err != nil {
		t.Fatal("explicit reader rejected", err)
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := loadManagementYAML(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if (parsed.Management.Encryption.Transit.ReaderGroupID == nil) != (source.ReaderGroupID == nil) || parsed.Management.Encryption.Transit.ReaderSID != source.ReaderSID {
		t.Fatal("reader policy lost")
	}
	encoded, err := json.Marshal(config.Management)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("management descriptor exposed", err)
	}
	if strings.Contains(fmt.Sprintf("%#v", config.Management), source.TokenFile) {
		t.Fatal("source leaked")
	}
	if runtime.GOOS == "windows" {
		group := 123
		source.ReaderGroupID = &group
	} else {
		source.ReaderSID = "S-1-5-80-123-456-789-101-112"
	}
	if err := config.Validate(); err == nil {
		t.Fatal("wrong OS reader ignored")
	}
	source.ReaderSID = ""
	source.ReaderGroupID = nil
	if runtime.GOOS != "windows" {
		for _, value := range []int{-1, int(uint64(1<<32) - 1)} {
			source.ReaderGroupID = &value
			if err := config.Validate(); err == nil {
				t.Fatal("invalid Unix group accepted")
			}
		}
	}
}

func TestEncryptionValidationStandalone(t *testing.T) {
	var encryption *ManagementEncryption
	if err := encryption.Validate(Storage{Mode: "memory"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"raft", "", "automatic"} {
		if err := encryption.Validate(Storage{Mode: mode}); err == nil {
			t.Fatal("invalid absence accepted", mode)
		}
	}
	config := managementConfigFixture(t)
	if err := config.Management.Encryption.Validate(config.Storage); err != nil {
		t.Fatal(err)
	}
	config.Management.Encryption.Local.ActiveKeyFile = filepath.Join(config.Storage.Directory, "inside.key")
	if err := config.Management.Encryption.Validate(config.Storage); err == nil {
		t.Fatal("standalone encryption bypassed data boundary")
	}
}

func TestManagementPolicyReaderDescriptors(t *testing.T) {
	config := managementConfigFixture(t)
	if runtime.GOOS == "windows" {
		config.Management.PolicyReaderSID = "S-1-5-80-123-456-789-101-112"
	} else {
		group := 123
		config.Management.PolicyReaderGroupID = &group
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := loadManagementYAML(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Management.PolicyReaderSID != config.Management.PolicyReaderSID || (parsed.Management.PolicyReaderGroupID == nil) != (config.Management.PolicyReaderGroupID == nil) {
		t.Fatal("policy reader identity lost")
	}
	if runtime.GOOS == "windows" {
		group := 123
		config.Management.PolicyReaderGroupID = &group
	} else {
		config.Management.PolicyReaderSID = "S-1-5-80-123-456-789-101-112"
	}
	if err := config.Validate(); err == nil {
		t.Fatal("wrong OS policy reader ignored")
	}
	config = managementConfigFixture(t)
	config.Management.Enabled = false
	config.Management.PolicyFile = ""
	config.Management.TLS, config.Management.Encryption = nil, nil
	config.Management.PolicyReaderSID = "supplied-reader"
	if err := config.Validate(); err == nil {
		t.Fatal("disabled management retained unexplained policy reader")
	}
}

func TestManagementTLSReaderDescriptors(t *testing.T) {
	config := managementConfigFixture(t)
	if runtime.GOOS == "windows" {
		config.Management.TLS.ReaderSID = "S-1-5-80-123-456-789-101-112"
	} else {
		group := 123
		config.Management.TLS.ReaderGroupID = &group
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := loadManagementYAML(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Management.TLS.ReaderSID != config.Management.TLS.ReaderSID || (parsed.Management.TLS.ReaderGroupID == nil) != (config.Management.TLS.ReaderGroupID == nil) {
		t.Fatal("TLS reader identity lost")
	}
	if runtime.GOOS == "windows" {
		group := 123
		config.Management.TLS.ReaderGroupID = &group
	} else {
		config.Management.TLS.ReaderSID = "S-1-5-80-123-456-789-101-112"
	}
	if err := config.Validate(); err == nil {
		t.Fatal("wrong OS TLS reader ignored")
	}
}
