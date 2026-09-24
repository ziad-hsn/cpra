package runtimeconfig

import (
	"bytes"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxPreviousManagementKeys = 32
	MaxManagementSourceFiles  = 16
	maxManagementPathBytes    = 4096
)

// Management is an explicit process-level opt-in. These are source descriptors,
// not secrets, loaded policies, clients, or evidence of backend availability.
// Startup must load/verify each source before opening management admission.
// No part of this configuration belongs in the read-only public config API.
type Management struct {
	Enabled             bool                  `yaml:"enabled" json:"-"`
	PolicyFile          string                `yaml:"policy_file,omitempty" json:"-"`
	PolicyReaderGroupID *int                  `yaml:"policy_reader_group_id,omitempty" json:"-"`
	PolicyReaderSID     string                `yaml:"policy_reader_sid,omitempty" json:"-"`
	TLS                 *ManagementTLS        `yaml:"tls,omitempty" json:"-"`
	TrustedProxy        *ManagementProxy      `yaml:"trusted_proxy,omitempty" json:"-"`
	Encryption          *ManagementEncryption `yaml:"encryption,omitempty" json:"-"`
}

type ManagementTLS struct {
	CertFile      string `yaml:"cert_file" json:"-"`
	KeyFile       string `yaml:"key_file" json:"-"`
	ReaderGroupID *int   `yaml:"reader_group_id,omitempty" json:"-"`
	ReaderSID     string `yaml:"reader_sid,omitempty" json:"-"`
}

type ManagementProxy struct {
	PublicOrigin string `yaml:"public_origin" json:"-"`
}

// ManagementEncryption describes exactly one approved wrapping source. Empty
// Backend selects local. Omitting this entire section is permitted only in
// explicit memory mode, where startup may generate a nonpersistent ephemeral key.
// A validated remote descriptor does not instantiate or certify that backend.
type ManagementEncryption struct {
	Backend string               `yaml:"backend,omitempty" json:"-"`
	Local   *ManagementLocalKeys `yaml:"local,omitempty" json:"-"`
	Transit *ManagementTransit   `yaml:"transit,omitempty" json:"-"`
	AWSKMS  *ManagementAWSKMS    `yaml:"aws_kms,omitempty" json:"-"`
}

type ManagementLocalKeys struct {
	ReaderGroupID    *int     `yaml:"reader_group_id,omitempty" json:"-"`
	ReaderSID        string   `yaml:"reader_sid,omitempty" json:"-"`
	ActiveKeyFile    string   `yaml:"active_key_file" json:"-"`
	PreviousKeyFiles []string `yaml:"previous_key_files,omitempty" json:"-"`
}

// ManagementTransit keeps the bootstrap token in a separate protected file.
// Address is an HTTPS origin; Mount and Key are paths/names, never URL fragments.
type ManagementTransit struct {
	ReaderGroupID *int   `yaml:"reader_group_id,omitempty" json:"-"`
	ReaderSID     string `yaml:"reader_sid,omitempty" json:"-"`
	Address       string `yaml:"address" json:"-"`
	Mount         string `yaml:"mount" json:"-"`
	Key           string `yaml:"key" json:"-"`
	TokenFile     string `yaml:"token_file" json:"-"`
	CAFile        string `yaml:"ca_file,omitempty" json:"-"`
	Namespace     string `yaml:"namespace,omitempty" json:"-"`
}

// ManagementAWSKMS requires an immutable customer-managed key ARN, never an
// alias. Startup must additionally verify symmetric-key capability and record
// the resolved ARN. SDK profile/file descriptors contain no access keys.
type ManagementAWSKMS struct {
	KeyARN                 string   `yaml:"key_arn" json:"-"`
	Region                 string   `yaml:"region" json:"-"`
	Profile                string   `yaml:"profile,omitempty" json:"-"`
	SharedConfigFiles      []string `yaml:"shared_config_files,omitempty" json:"-"`
	SharedCredentialsFiles []string `yaml:"shared_credentials_files,omitempty" json:"-"`
}

// String and GoString redact source locations from accidental config logging.
func (c Management) String() string {
	return fmt.Sprintf("management{enabled:%t, sources:redacted}", c.Enabled)
}
func (c Management) GoString() string { return c.String() }

// UnmarshalYAML preserves strict field/type checks while keeping supplied values
// out of errors. Error locations consist only of known schema paths and line
// numbers, never attacker-selected field names or malformed secret values.
func (c *Management) UnmarshalYAML(node *yaml.Node) error {
	type plain Management
	if err := checkManagementNode(node, reflect.TypeFor[plain](), "management"); err != nil {
		return err
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return managementConfigError("management", node.Line, "invalid YAML structure")
	}
	defer clear(raw)
	var value plain
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return managementConfigError("management", node.Line, "invalid field type")
	}
	*c = Management(value)
	return nil
}

func managementConfigError(field string, line int, detail string) error {
	if line > 0 {
		return fmt.Errorf("%s at line %d: %s", field, line, detail)
	}
	return fmt.Errorf("%s: %s", field, detail)
}

func checkManagementNode(node *yaml.Node, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if node.Kind == yaml.AliasNode {
		return managementConfigError(path, node.Line, "YAML aliases are not supported")
	}
	if node.Tag == "!!null" {
		return managementConfigError(path, node.Line, "explicit null is not supported; omit optional settings")
	}
	switch typ.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
			return managementConfigError(path, node.Line, "expected a mapping")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			fields[name] = field.Type
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			field, known := fields[key.Value]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !known {
				return managementConfigError(path, key.Line, "unknown setting")
			}
			if seen[key.Value] {
				return managementConfigError(path+"."+key.Value, key.Line, "duplicate setting")
			}
			seen[key.Value] = true
			if err := checkManagementNode(value, field, path+"."+key.Value); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return managementConfigError(path, node.Line, "expected a list")
		}
		if len(node.Content) > MaxPreviousManagementKeys {
			return managementConfigError(path, node.Line, "too many source files")
		}
		for _, value := range node.Content {
			if err := checkManagementNode(value, typ.Elem(), path); err != nil {
				return err
			}
		}
	case reflect.Int:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return managementConfigError(path, node.Line, "expected an integer")
		}
	case reflect.Bool:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return managementConfigError(path, node.Line, "expected true or false")
		}
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return managementConfigError(path, node.Line, "expected a string")
		}
		if len(node.Value) > maxManagementPathBytes {
			return managementConfigError(path, node.Line, "value exceeds the configuration field limit")
		}
	}
	return nil
}

// EffectiveEncryptionBackend reports a selection without loading or generating
// keys. "ephemeral" is possible only with explicit memory storage and omission.
func (c Management) EffectiveEncryptionBackend(storageMode string) string {
	if !c.Enabled {
		return ""
	}
	if c.Encryption == nil {
		if storageMode == "memory" {
			return "ephemeral"
		}
		return "local"
	}
	if c.Encryption.Backend == "" {
		return "local"
	}
	return c.Encryption.Backend
}

// Validate performs only bounded structural checks. It never opens a key, token,
// policy, certificate, socket or provider. ValidateDataDirectory must be called
// again after a data-directory flag/default is resolved; actual file ownership,
// symlinks and backend capabilities require separate startup verification.
func (c Management) Validate(storage Storage) error {
	invalid := func(field, detail string) error { return managementConfigError("management."+field, 0, detail) }
	if !c.Enabled {
		if c.PolicyFile != "" || c.PolicyReaderGroupID != nil || c.PolicyReaderSID != "" || c.TLS != nil || c.TrustedProxy != nil || c.Encryption != nil {
			return invalid("enabled", "must be true when management settings are supplied")
		}
		return nil
	}
	if c.PolicyFile != "" && !managementPath(c.PolicyFile) {
		return invalid("policy_file", "requires an absolute path to a one-time bootstrap verifier policy file")
	}
	if c.PolicyReaderGroupID != nil && (runtime.GOOS == "windows" || *c.PolicyReaderGroupID < 0 || uint64(*c.PolicyReaderGroupID) >= 1<<32-1) {
		return invalid("policy_reader_group_id", "requires a valid Unix group identity on Unix")
	}
	if c.PolicyReaderSID != "" && (runtime.GOOS != "windows" || !managementSID(c.PolicyReaderSID)) {
		return invalid("policy_reader_sid", "requires a Windows service SID on Windows")
	}
	if (c.TLS == nil) == (c.TrustedProxy == nil) {
		return invalid("tls", "configure exactly one TLS certificate/key pair or trusted_proxy")
	}
	if c.TLS != nil {
		if c.TLS.ReaderGroupID != nil && (runtime.GOOS == "windows" || *c.TLS.ReaderGroupID < 0 || uint64(*c.TLS.ReaderGroupID) >= 1<<32-1) {
			return invalid("tls.reader_group_id", "requires a valid Unix group identity on Unix")
		}
		if c.TLS.ReaderSID != "" && (runtime.GOOS != "windows" || !managementSID(c.TLS.ReaderSID)) {
			return invalid("tls.reader_sid", "requires a Windows service SID on Windows")
		}
		if !managementPath(c.TLS.CertFile) {
			return invalid("tls.cert_file", "requires an absolute certificate file path")
		}
		if !managementPath(c.TLS.KeyFile) {
			return invalid("tls.key_file", "requires an absolute private-key file path")
		}
	} else if !managementHTTPSOrigin(c.TrustedProxy.PublicOrigin) {
		return invalid("trusted_proxy.public_origin", "requires one HTTPS origin without credentials, path, query or fragment")
	}
	if err := c.Encryption.Validate(storage); err != nil {
		return err
	}
	if storage.Directory != "" {
		return c.ValidateDataDirectory(storage.Directory)
	}
	return nil
}

// Validate checks encryption descriptors without loading files or contacting a
// backend. A nil descriptor is accepted only in explicit memory mode. The caller
// must supply the resolved storage boundary before any source is loaded.
func (c *ManagementEncryption) Validate(storage Storage) error {
	invalid := func(field, detail string) error { return managementConfigError("management."+field, 0, detail) }
	if storage.Mode != "raft" && storage.Mode != "memory" {
		return invalid("encryption", "requires an explicit supported storage mode")
	}
	if c == nil {
		if storage.Mode != "memory" {
			return invalid("encryption.local.active_key_file", "initialize encryption explicitly with cpractl encryption init, or configure an approved wrapping backend before enabling durable management")
		}
	} else {
		encryption := c
		backend := c.Backend
		if backend == "" {
			backend = "local"
		}
		switch backend {
		case "local":
			if encryption.Local == nil || encryption.Transit != nil || encryption.AWSKMS != nil {
				return invalid("encryption.local", "select only the local key source")
			}
			if encryption.Local.ReaderGroupID != nil && (runtime.GOOS == "windows" || *encryption.Local.ReaderGroupID < 0 || uint64(*encryption.Local.ReaderGroupID) >= 1<<32-1) {
				return invalid("encryption.local.reader_group_id", "requires a valid Unix group identity on Unix")
			}
			if encryption.Local.ReaderSID != "" && (runtime.GOOS != "windows" || !managementSID(encryption.Local.ReaderSID)) {
				return invalid("encryption.local.reader_sid", "requires a Windows service SID on Windows")
			}
			if !managementPath(encryption.Local.ActiveKeyFile) {
				return invalid("encryption.local.active_key_file", "requires an absolute initialized key file path")
			}
			if err := validateSourceFiles(encryption.Local.PreviousKeyFiles, MaxPreviousManagementKeys, "management.encryption.local.previous_key_files", encryption.Local.ActiveKeyFile); err != nil {
				return err
			}
		case "openbao-transit", "vault-transit":
			source := encryption.Transit
			if source == nil || encryption.Local != nil || encryption.AWSKMS != nil {
				return invalid("encryption.transit", "select only the Transit source")
			}
			if source.ReaderGroupID != nil && (runtime.GOOS == "windows" || *source.ReaderGroupID < 0 || uint64(*source.ReaderGroupID) >= 1<<32-1) {
				return invalid("encryption.transit.reader_group_id", "requires a valid Unix group identity on Unix")
			}
			if source.ReaderSID != "" && (runtime.GOOS != "windows" || !managementSID(source.ReaderSID)) {
				return invalid("encryption.transit.reader_sid", "requires a Windows service SID on Windows")
			}
			if !managementHTTPSOrigin(source.Address) {
				return invalid("encryption.transit.address", "requires an HTTPS origin without credentials, path, query or fragment")
			}
			if !managementNamePath(source.Mount, 256, true) {
				return invalid("encryption.transit.mount", "requires a bounded mount path")
			}
			if !managementNamePath(source.Key, 256, false) {
				return invalid("encryption.transit.key", "requires a key name without path or URL syntax")
			}
			if !managementPath(source.TokenFile) {
				return invalid("encryption.transit.token_file", "requires an absolute bootstrap token file path")
			}
			if source.CAFile != "" && !managementPath(source.CAFile) {
				return invalid("encryption.transit.ca_file", "requires an absolute certificate file path")
			}
			if source.Namespace != "" && !managementNamePath(source.Namespace, 256, true) {
				return invalid("encryption.transit.namespace", "requires a bounded namespace path")
			}
		case "aws-kms":
			source := encryption.AWSKMS
			if source == nil || encryption.Local != nil || encryption.Transit != nil {
				return invalid("encryption.aws_kms", "select only the AWS KMS source")
			}
			if !managementKMSARN(source.KeyARN, source.Region) {
				return invalid("encryption.aws_kms.key_arn", "requires a key ARN in the configured region; aliases are not accepted")
			}
			if source.Profile != "" && !managementText(source.Profile, 128) {
				return invalid("encryption.aws_kms.profile", "requires a bounded profile name")
			}
			if err := validateSourceFiles(source.SharedConfigFiles, MaxManagementSourceFiles, "management.encryption.aws_kms.shared_config_files", ""); err != nil {
				return err
			}
			if err := validateSourceFiles(source.SharedCredentialsFiles, MaxManagementSourceFiles, "management.encryption.aws_kms.shared_credentials_files", ""); err != nil {
				return err
			}
		default:
			return invalid("encryption.backend", "must be local, openbao-transit, vault-transit or aws-kms")
		}
	}
	if storage.Directory != "" {
		return (Management{Enabled: true, Encryption: c}).ValidateDataDirectory(storage.Directory)
	}
	return nil
}

// ValidateDataDirectory performs lexical containment checks after all storage
// path overrides are resolved. File loading must additionally reject symlinks or
// otherwise prove resolved key/bootstrap files lie outside the real data tree.
func (c Management) ValidateDataDirectory(directory string) error {
	if !c.Enabled || directory == "" {
		return nil
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return managementConfigError("management", 0, "cannot resolve data-directory boundary")
	}
	paths := []string{c.PolicyFile}
	if c.TLS != nil {
		paths = append(paths, c.TLS.KeyFile, c.TLS.CertFile)
	}
	if c.Encryption != nil {
		if source := c.Encryption.Local; source != nil {
			paths = append(paths, source.ActiveKeyFile)
			paths = append(paths, source.PreviousKeyFiles...)
		}
		if source := c.Encryption.Transit; source != nil {
			paths = append(paths, source.TokenFile, source.CAFile)
		}
		if source := c.Encryption.AWSKMS; source != nil {
			paths = append(paths, source.SharedConfigFiles...)
			paths = append(paths, source.SharedCredentialsFiles...)
		}
	}
	for _, source := range paths {
		if source == "" {
			continue
		}
		rel, err := filepath.Rel(absolute, filepath.Clean(source))
		if err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return managementConfigError("management", 0, "policy, keys and bootstrap source files must remain outside the data directory")
		}
	}
	return nil
}

func managementPath(path string) bool {
	return managementText(path, maxManagementPathBytes) && filepath.IsAbs(path) && filepath.Clean(path) != string(filepath.Separator)
}
func managementText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func managementHTTPSOrigin(value string) bool {
	if !managementText(value, 2048) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Opaque == "" &&
		(parsed.Path == "" || parsed.Path == "/") && parsed.RawPath == "" && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawFragment == ""
}
func managementNamePath(value string, limit int, path bool) bool {
	if !managementText(value, limit) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return path || !strings.Contains(value, "/")
}
func validateSourceFiles(paths []string, limit int, field, active string) error {
	if len(paths) > limit {
		return managementConfigError(field, 0, "too many source files")
	}
	seen := map[string]bool{}
	normalize := func(path string) string {
		path = filepath.Clean(path)
		if runtime.GOOS == "windows" {
			path = strings.ToLower(path)
		}
		return path
	}
	if active != "" {
		seen[normalize(active)] = true
	}
	for _, path := range paths {
		if !managementPath(path) {
			return managementConfigError(field, 0, "requires absolute file paths")
		}
		identity := normalize(path)
		if seen[identity] {
			return managementConfigError(field, 0, "duplicate source path")
		}
		seen[identity] = true
	}
	return nil
}
func managementKMSARN(value, region string) bool {
	if !managementText(value, 2048) || !managementNamePath(region, 64, false) {
		return false
	}
	parts := strings.SplitN(value, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "kms" || parts[3] != region || len(parts[4]) != 12 {
		return false
	}
	switch parts[1] {
	case "aws", "aws-cn", "aws-us-gov":
	default:
		return false
	}
	for _, digit := range parts[4] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	key, ok := strings.CutPrefix(parts[5], "key/")
	return ok && managementNamePath(key, 128, false)
}

// Structural validation only; Windows key loading validates the resolved SID.
func managementSID(value string) bool {
	if !managementText(value, 256) || !strings.HasPrefix(value, "S-1-") {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) < 4 || len(parts) > 18 {
		return false
	}
	for _, part := range parts[2:] {
		if part == "" || len(part) > 20 {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
