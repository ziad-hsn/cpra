package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func managementConfigFixture(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	config := Default()
	config.Storage.Directory = filepath.Join(base, "state")
	config.Management = Management{Enabled: true, PolicyFile: filepath.Join(base, "config", "principals.yaml"), TLS: &ManagementTLS{CertFile: filepath.Join(base, "config", "tls.pem"), KeyFile: filepath.Join(base, "config", "tls-key.pem")}, Encryption: &ManagementEncryption{Local: &ManagementLocalKeys{ActiveKeyFile: filepath.Join(base, "config", "keys", "active.key")}}}
	return config
}
func loadManagementYAML(t *testing.T, raw string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}
func TestManagementExplicitActivationAndNoSourceIO(t *testing.T) {
	config := Default()
	if err := config.Validate(); err != nil || config.Management.Enabled || config.Management.EffectiveEncryptionBackend(config.Storage.Mode) != "" {
		t.Fatal("management enabled by default", err)
	}
	config = managementConfigFixture(t)
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(config.Management.PolicyFile)); !os.IsNotExist(err) {
		t.Fatal("validation touched absent source directory", err)
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := loadManagementYAML(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Management.Enabled || parsed.Management.PolicyFile != config.Management.PolicyFile || parsed.Management.EffectiveEncryptionBackend(parsed.Storage.Mode) != "local" {
		t.Fatal("descriptor round trip changed source")
	}
	if _, err := os.Stat(filepath.Dir(config.Management.Encryption.Local.ActiveKeyFile)); !os.IsNotExist(err) {
		t.Fatal("loading runtime created key files", err)
	}
	parsed.Management.Encryption = nil
	if err := parsed.Validate(); err == nil || !strings.Contains(err.Error(), "cpractl encryption init") {
		t.Fatal("durable management silently generated key", err)
	}
	parsed.Storage.Mode = "memory"
	if err := parsed.Validate(); err != nil || parsed.Management.EffectiveEncryptionBackend("memory") != "ephemeral" {
		t.Fatal("explicit memory mode needs persistent key", err)
	}
	parsed.Storage.Mode = "typo"
	if err := parsed.Validate(); err == nil {
		t.Fatal("unknown storage mode treated as ephemeral")
	}
}
func TestManagementSourceValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"disabled with settings", func(c *Config) { c.Management.Enabled = false }},
		{"relative policy", func(c *Config) { c.Management.PolicyFile = "principals.yaml" }},
		{"control path", func(c *Config) { c.Management.PolicyFile += "\nsecret" }},
		{"no transport", func(c *Config) { c.Management.TLS = nil }},
		{"missing TLS key", func(c *Config) { c.Management.TLS.KeyFile = "" }},
		{"conflicting proxy", func(c *Config) { c.Management.TrustedProxy = &ManagementProxy{PublicOrigin: "https://cpra.example"} }},
		{"missing local source", func(c *Config) { c.Management.Encryption.Local = nil }},
		{"unknown backend", func(c *Config) { c.Management.Encryption.Backend = "automatic" }},
		{"conflicting backend", func(c *Config) { c.Management.Encryption.Transit = &ManagementTransit{} }},
		{"inline-like key path", func(c *Config) { c.Management.Encryption.Local.ActiveKeyFile = "plaintext-key-fixture" }},
		{"active key in state", func(c *Config) {
			c.Management.Encryption.Local.ActiveKeyFile = filepath.Join(c.Storage.Directory, "keys", "active")
		}},
		{"policy in state", func(c *Config) { c.Management.PolicyFile = filepath.Join(c.Storage.Directory, "policy.yaml") }},
		{"TLS in state", func(c *Config) { c.Management.TLS.KeyFile = filepath.Join(c.Storage.Directory, "key.pem") }},
		{"previous in state", func(c *Config) {
			c.Management.Encryption.Local.PreviousKeyFiles = []string{filepath.Join(c.Storage.Directory, "old.key")}
		}},
		{"duplicate active", func(c *Config) {
			c.Management.Encryption.Local.PreviousKeyFiles = []string{c.Management.Encryption.Local.ActiveKeyFile}
		}},
		{"duplicate previous", func(c *Config) {
			path := filepath.Join(filepath.Dir(c.Management.PolicyFile), "old.key")
			c.Management.Encryption.Local.PreviousKeyFiles = []string{path, path}
		}},
		{"too many previous", func(c *Config) {
			for i := 0; i <= MaxPreviousManagementKeys; i++ {
				c.Management.Encryption.Local.PreviousKeyFiles = append(c.Management.Encryption.Local.PreviousKeyFiles, filepath.Join(filepath.Dir(c.Management.PolicyFile), fmt.Sprintf("old-%d", i)))
			}
		}},
		{"wrong OS reader", func(c *Config) {
			if runtime.GOOS == "windows" {
				value := 1
				c.Management.Encryption.Local.ReaderGroupID = &value
			} else {
				c.Management.Encryption.Local.ReaderSID = "S-1-5-80-123"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := managementConfigFixture(t)
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid management setting accepted")
			}
		})
	}
	config := managementConfigFixture(t)
	config.Management.Encryption.Local.ActiveKeyFile = filepath.Join(config.Storage.Directory+"-keys", "active")
	if err := config.Validate(); err != nil {
		t.Fatal("sibling key directory mistaken for state descendant", err)
	}
	newState := filepath.Dir(config.Management.PolicyFile)
	if err := config.Management.ValidateDataDirectory(newState); err == nil {
		t.Fatal("resolved -data-dir override bypasses source boundary")
	}
}
func TestManagementApprovedBackendDescriptors(t *testing.T) {
	for _, backend := range []string{"openbao-transit", "vault-transit"} {
		config := managementConfigFixture(t)
		config.Management.Encryption = &ManagementEncryption{Backend: backend, Transit: &ManagementTransit{Address: "https://bao.example:8200", Mount: "team/transit", Key: "cpra-data", TokenFile: filepath.Join(filepath.Dir(config.Management.PolicyFile), "transit-token"), Namespace: "platform/reliability"}}
		if err := config.Validate(); err != nil {
			t.Fatal(backend, err)
		}
		for _, address := range []string{"http://bao.example", "https://user:credential-marker@bao.example", "https://bao.example?token=credential-marker", "https://bao.example/#credential-marker", "https://bao.example/v1/transit"} {
			config.Management.Encryption.Transit.Address = address
			if err := config.Validate(); err == nil || strings.Contains(err.Error(), "credential-marker") {
				t.Fatal("unsafe Transit address or leak", err)
			}
		}
	}
	config := managementConfigFixture(t)
	config.Management.Encryption = &ManagementEncryption{Backend: "aws-kms", AWSKMS: &ManagementAWSKMS{KeyARN: "arn:aws:kms:eu-west-1:123456789012:key/12345678-1234-1234-1234-123456789012", Region: "eu-west-1", Profile: "cpra-runtime", SharedConfigFiles: []string{filepath.Join(filepath.Dir(config.Management.PolicyFile), "aws-config")}, SharedCredentialsFiles: []string{filepath.Join(filepath.Dir(config.Management.PolicyFile), "aws-credentials")}}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, arn := range []string{"alias/cpra", "arn:aws:kms:eu-west-1:123456789012:alias/cpra", "arn:aws:kms:us-east-1:123456789012:key/key1", "arn:aws:s3:eu-west-1:123456789012:key/key1", "arn:aws:kms:eu-west-1:123:key/key1"} {
		config.Management.Encryption.AWSKMS.KeyARN = arn
		if err := config.Validate(); err == nil {
			t.Fatal("mutable or mismatched KMS identity accepted")
		}
	}
}
func TestManagementProxyOriginRules(t *testing.T) {
	config := managementConfigFixture(t)
	config.Management.TLS = nil
	config.Management.TrustedProxy = &ManagementProxy{PublicOrigin: "https://cpra.example:8443/"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://cpra.example", "https://cpra.example/path", "https://cpra.example?token=credential-marker", "https://user:credential-marker@cpra.example", "https://cpra.example#credential-marker", "https://cpra.example?", "https://cpra.example:bad"} {
		config.Management.TrustedProxy.PublicOrigin = origin
		if err := config.Validate(); err == nil || strings.Contains(err.Error(), "credential-marker") {
			t.Fatal("bad origin or leak", err)
		}
	}
}
func TestManagementYAMLTypeErrorsNeverEchoValues(t *testing.T) {
	for _, raw := range []string{
		"management:\n  enabled: credential-marker\n",
		"management:\n  encryption:\n    local:\n      reader_group_id: credential-marker\n",
		"management:\n  encryption:\n    local:\n      active_key_file: [credential-marker]\n",
		"management:\n  encryption:\n    local:\n      previous_key_files: credential-marker\n",
		"management:\n  credential-marker: value\n",
		"management:\n  policy_file:\n    credential-marker: value\n",
		"management:\n  policy_file: credential-marker\n  policy_file: other\n",
		"management:\n  policy_file: &source credential-marker\n  encryption:\n    local:\n      active_key_file: *source\n",
		"management:\n  enabled: null\n",
	} {
		_, err := loadManagementYAML(t, raw)
		if err == nil || strings.Contains(err.Error(), "credential-marker") || !strings.Contains(err.Error(), "management") || !strings.Contains(err.Error(), "line") {
			t.Fatalf("error loses safe location or leaks source: %v", err)
		}
	}
}
func TestManagementExcludedFromDiagnosticSerialization(t *testing.T) {
	config := managementConfigFixture(t)
	config.Management.PolicyFile = filepath.Join(t.TempDir(), "credential-marker-policy")
	config.Management.Encryption.Local.ActiveKeyFile = filepath.Join(t.TempDir(), "credential-marker-key")
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range []string{string(encoded), fmt.Sprint(config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config)} {
		if strings.Contains(diagnostic, "credential-marker") || strings.Contains(diagnostic, "policy_file") || strings.Contains(diagnostic, "active_key_file") {
			t.Fatal("management source leaked through config diagnostic", diagnostic)
		}
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["Management"]; present {
		t.Fatal("management present in public JSON")
	}
}

func TestRuntimeLoadDetectsOversizedSuffix(t *testing.T) {
	prefix := "storage:\n  mode: memory\n"
	for _, suffix := range []string{strings.Repeat(" ", 1<<20), strings.Repeat(" ", 1<<20) + "\nmanagement:\n  enabled: true\n"} {
		_, err := loadManagementYAML(t, prefix+suffix)
		if err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
			t.Fatal("oversized document was truncated or accepted", err)
		}
	}
	config, err := loadManagementYAML(t, prefix+strings.Repeat(" ", (1<<20)-len(prefix)))
	if err != nil || config.Storage.Mode != "memory" {
		t.Fatal("exact document limit rejected", err)
	}
}
