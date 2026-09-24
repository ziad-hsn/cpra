package localadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func backupTestKey(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "backup-keys", "authentication.key")
	if _, err := secureconfig.GenerateLocalKeyFile(t.Context(), secureconfig.KeyFileOptions{Path: path, DataDirectory: filepath.Join(root, "state")}); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestBackupManifestAuthenticationRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	key := backupTestKey(t, root)
	source, backup := filepath.Join(root, "state"), filepath.Join(root, "backup")
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = source
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(source, "operator-notes.txt")
	if err := os.WriteFile(note, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Backup(source, backup, "", key); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(backup, "BACKUP.json")
	original, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoot := t.TempDir()
	wrongKey := backupTestKey(t, wrongRoot)
	for _, tc := range []struct{ name, key string }{{"missing", ""}, {"wrong", wrongKey}} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(root, "reject-"+tc.name)
			if err := Restore(backup, destination, tc.key); err == nil {
				t.Fatal("untrusted backup admitted")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed authentication published state", err)
			}
		})
	}
	cases := []struct {
		name   string
		change func(*BackupManifest)
	}{
		{"node", func(m *BackupManifest) { m.NodeID = "foreign-node" }},
		{"created", func(m *BackupManifest) { m.Created = m.Created.Add(time.Hour) }},
		{"artifact", func(m *BackupManifest) { m.Artifact = "attacker-build" }},
		{"configuration", func(m *BackupManifest) { m.ConfigurationDigest = strings.Repeat("a", 64) }},
		{"storage-format", func(m *BackupManifest) { m.StorageFormat++ }},
		{"size", func(m *BackupManifest) { m.Files[0].Size++ }},
		{"path", func(m *BackupManifest) { m.Files[0].Path = "replacement" }},
		{"algorithm", func(m *BackupManifest) { m.Authentication.Algorithm = "sha256" }},
		{"key-id", func(m *BackupManifest) { m.Authentication.KeyID = strings.Repeat("b", 64) }},
		{"tag", func(m *BackupManifest) { m.Authentication.MAC = strings.Repeat("0", 64) }},
		{"unsigned", func(m *BackupManifest) { m.Version = 1; m.Authentication = BackupAuthentication{} }},
		{"recomputed-hashes", func(m *BackupManifest) {
			if err := os.WriteFile(filepath.Join(backup, "data", "operator-notes.txt"), []byte("attacker replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := treeInventory(filepath.Join(backup, "data"))
			if err != nil {
				t.Fatal(err)
			}
			m.Files = files
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m BackupManifest
			if err := json.Unmarshal(original, &m); err != nil {
				t.Fatal(err)
			}
			tc.change(&m)
			raw, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, raw, 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "reject-"+tc.name)
			if err := Restore(backup, destination, key); err == nil {
				t.Fatal("tampered authenticated manifest admitted")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("tampered backup published state", err)
			}
		})
	}
	for _, raw := range [][]byte{append([]byte(`{"node_id":"duplicate",`), original[1:]...), bytes.Replace(original, []byte(`"version": 2`), []byte(`"version": null`), 1), append(bytes.Clone(original), []byte(`{}`)...)} {
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := Restore(backup, filepath.Join(root, "malformed"), key); err == nil {
			t.Fatal("ambiguous manifest accepted")
		}
	}
}
func TestBackupKeyMustBeExternalToBothTrees(t *testing.T) {
	root := t.TempDir()
	external := backupTestKey(t, root)
	source, destination := filepath.Join(root, "state"), filepath.Join(root, "backup")
	for _, dir := range []string{source, destination} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	key, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	for _, dir := range []string{source, destination} {
		inside := filepath.Join(dir, "authentication.key")
		if err := os.WriteFile(inside, key, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := loadBackupKey(t.Context(), inside, source, destination); err == nil {
			clear(got)
			t.Fatal("key inside backed-up tree accepted")
		}
		alias := filepath.Join(root, "alias-"+filepath.Base(dir))
		if err := os.Symlink(dir, alias); err != nil {
			t.Fatal(err)
		}
		if got, err := loadBackupKey(t.Context(), filepath.Join(alias, "authentication.key"), source, destination); err == nil {
			clear(got)
			t.Fatal("aliased key inside tree accepted")
		}
	}
	if got, err := loadBackupKey(t.Context(), external, source, destination); err != nil {
		t.Fatal("independent key rejected", err)
	} else {
		clear(got)
	}
}
func TestBackupRequiresExactProtectedKey(t *testing.T) {
	for _, length := range []int{0, 31, 33} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			root := t.TempDir()
			key := backupTestKey(t, root)
			if err := os.WriteFile(key, bytes.Repeat([]byte{17}, length), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := loadBackupKey(t.Context(), key, filepath.Join(root, "state"), filepath.Join(root, "backup")); err == nil {
				clear(got)
				t.Fatal("invalid key length accepted", length)
			}
		})
	}
}
func TestServiceUpdateValidatesBackupKeyBeforeCandidateOrSupervisor(t *testing.T) {
	root := t.TempDir()
	layout := installpath.Layout{Scope: "system", BinDir: filepath.Join(root, "bin"), ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	for _, dir := range []string{layout.BinDir, layout.ConfigDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	existing, candidate := binaryPath(layout), filepath.Join(root, "candidate")
	if err := os.WriteFile(existing, []byte("existing installation"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("invalid executable; must never run"), 0700); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	record := Installation{Version: 1, Kind: "local", Binary: existing, BinarySHA256: hash.SHA256}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(layout.ConfigDir, "install.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), layout, candidate, "", true, ""); err == nil || !strings.Contains(err.Error(), "backup-auth-key") {
		t.Fatal("update reached candidate or supervisor before checking backup authority", err)
	}
	current, err := os.ReadFile(existing)
	if err != nil || string(current) != "existing installation" {
		t.Fatal("missing backup key changed installation", err)
	}
}
