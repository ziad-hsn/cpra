package localadmin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const backupManifestLimit = 32 << 20
const backupMACDomain = "CPRa authenticated stopped backup v2\x00"

// BackupAuthentication authenticates the complete manifest with a key retained
// separately from the state and backup. The digest identifies that key; it does
// not embed key material or establish trust without the supplied external key.
type BackupAuthentication struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	MAC       string `json:"mac,omitempty"`
}

func loadBackupKey(ctx context.Context, path, source, destination string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("--backup-auth-key must name a protected external 32-byte backup authentication key")
	}
	var key []byte
	for _, excluded := range []string{source, destination} {
		current, err := secureconfig.ReadProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: path, DataDirectory: excluded, MaxBytes: 32})
		if err != nil {
			clear(key)
			return nil, fmt.Errorf("backup authentication key: %w", err)
		}
		if len(current) != 32 {
			clear(current)
			clear(key)
			return nil, errors.New("backup authentication key must contain exactly 32 bytes")
		}
		if key == nil {
			key = current
			continue
		}
		equal := hmac.Equal(key, current)
		clear(current)
		if !equal {
			clear(key)
			return nil, errors.New("backup authentication key changed during access")
		}
	}
	return key, nil
}

func backupKeyID(key []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("CPRa backup authentication key v2\x00"))
	_, _ = hash.Write(key)
	return hex.EncodeToString(hash.Sum(nil))
}
func backupManifestMAC(m BackupManifest, key []byte) ([]byte, error) {
	m.Authentication.MAC = ""
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(backupMACDomain))
	_, _ = mac.Write(raw)
	return mac.Sum(nil), nil
}
func signBackupManifest(m *BackupManifest, key []byte) error {
	if len(key) != 32 {
		return errors.New("backup authentication key must contain exactly 32 bytes")
	}
	m.Authentication = BackupAuthentication{Algorithm: "hmac-sha256", KeyID: backupKeyID(key)}
	mac, err := backupManifestMAC(*m, key)
	if err != nil {
		return err
	}
	m.Authentication.MAC = hex.EncodeToString(mac)
	return nil
}
func authenticateBackupManifest(m BackupManifest, key []byte) error {
	if m.Version == 1 {
		return errors.New("unsigned backup format 1 is not accepted; create an authenticated backup from the original stopped store")
	}
	if m.Version != 2 || m.StorageFormat != persistence.FormatVersion || m.NodeID == "" || m.Created.IsZero() || m.Artifact == "" || len(m.Files) == 0 {
		return errors.New("incompatible or empty backup inventory")
	}
	if len(key) != 32 || m.Authentication.Algorithm != "hmac-sha256" || m.Authentication.KeyID != backupKeyID(key) {
		return errors.New("backup manifest authentication failed")
	}
	got, err := hex.DecodeString(m.Authentication.MAC)
	if err != nil || len(got) != sha256.Size {
		return errors.New("backup manifest authentication failed")
	}
	expected, err := backupManifestMAC(m, key)
	if err != nil {
		return errors.New("backup manifest authentication failed")
	}
	if !hmac.Equal(got, expected) {
		return errors.New("backup manifest authentication failed")
	}
	return nil
}

func readBackupManifest(path string) (BackupManifest, error) {
	var m BackupManifest
	file, err := os.Open(path)
	if err != nil {
		return m, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, backupManifestLimit+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return m, err
	}
	if len(raw) > backupManifestLimit {
		return m, errors.New("backup manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, errors.New("invalid backup manifest")
	}
	// Legacy inventories are rejected without inventing a trust root or silently
	// treating a failed authentication as an unsigned compatibility backup.
	if m.Version == 1 {
		return m, errors.New("unsigned backup format 1 is not accepted; create an authenticated backup from the original stopped store")
	}
	var compact bytes.Buffer
	canonical, err := json.Marshal(m)
	// Generated manifests have a canonical field order/shape. Whitespace is
	// harmless; duplicate keys, omitted fields, null scalars and trailing input
	// cannot be normalized into the authenticated object.
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return m, errors.New("invalid backup manifest shape")
	}
	return m, nil
}
