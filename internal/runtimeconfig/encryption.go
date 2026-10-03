package runtimeconfig

import (
	"bytes"
	"reflect"

	"gopkg.in/yaml.v3"
)

// EncryptionSource selects the single process keyring after Validate rejects
// conflicting sources. Storage encryption also supports file-only operation.
func (c Config) EncryptionSource() *ManagementEncryption {
	if c.Storage.Encryption != nil {
		return c.Storage.Encryption
	}
	return c.Management.Encryption
}

// UnmarshalYAML keeps key locations and unexpected supplied values out of
// decoder errors. Preserve defaults when only part of storage is specified.
func (s *Storage) UnmarshalYAML(node *yaml.Node) error {
	type plain Storage
	if err := checkManagementNode(node, reflect.TypeFor[plain](), "storage"); err != nil {
		return err
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return managementConfigError("storage", node.Line, "invalid YAML structure")
	}
	defer clear(raw)
	value := plain(*s)
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return managementConfigError("storage", node.Line, "invalid field type")
	}
	*s = Storage(value)
	return nil
}

func (*ManagementEncryption) String() string     { return "encryption{sources:redacted}" }
func (c *ManagementEncryption) GoString() string { return c.String() }
