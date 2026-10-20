package localadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestStoppedBackupRecordsCommittedStateFormat(t *testing.T) {
	for _, phase := range []string{"log-only", "snapshot", "snapshot-and-newer-log"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			key := backupTestKey(t, root)
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = filepath.Join(root, "state")
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			monitor := persistence.Monitor{ID: "stable", Name: "example", Revision: "one", Policy: persistence.Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 1}}
			if _, err := store.Submit(t.Context(), []persistence.Command{{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: time.Now().UTC()}}); err != nil {
				t.Fatal(err)
			}
			if phase == "snapshot-and-newer-log" {
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			verifier := sha256.Sum256([]byte("fixture-token-never-written-to-state"))
			if _, err := store.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
				Principals: []persistence.AuthenticationPrincipal{{ID: "oncall", Role: "operator", TokenSHA256: hex.EncodeToString(verifier[:])}}}); err != nil {
				t.Fatal(err)
			}
			if phase == "snapshot" {
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(root, "backup")
			if err := Backup(cfg.Storage.Directory, backup, "", key); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(backup, "BACKUP.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				StorageFormat int `json:"storage_format"`
				StateFormat   int `json:"state_format"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.StateFormat != persistence.LatestFormatVersion {
				t.Fatalf("backup does not identify committed state: state_format=%d, want %d (storage container format=%d)", manifest.StateFormat, persistence.LatestFormatVersion, manifest.StorageFormat)
			}
			original, err := readBackupManifest(filepath.Join(backup, "BACKUP.json"))
			if err != nil {
				t.Fatal(err)
			}
			keyBytes, err := os.ReadFile(key)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(keyBytes)
			before, err := treeInventory(filepath.Join(backup, "data"))
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name   string
				format int
				sign   bool
				want   string
			}{
				{"matching", original.StateFormat, true, ""},
				{"missing", 0, true, ""},
				{"mismatch", persistence.FormatVersion, true, "does not match committed state"},
				{"future", persistence.LatestFormatVersion + 1, true, "incompatible backup state format"},
				{"negative", -1, true, "incompatible backup state format"},
				{"removed-without-authentication", 0, false, "backup manifest authentication failed"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					updated := original
					updated.StateFormat = tc.format
					if tc.sign {
						if err := signBackupManifest(&updated, keyBytes); err != nil {
							t.Fatal(err)
						}
					}
					data, err := json.Marshal(updated)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(backup, "BACKUP.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					destination := filepath.Join(root, "restore-"+tc.name)
					err = Restore(backup, destination, key)
					if tc.want == "" {
						if err != nil {
							t.Fatal(err)
						}
						restoredConfig := cfg
						restoredConfig.Storage.Directory = destination
						opened, err := persistence.Open(t.Context(), restoredConfig)
						if opened != nil {
							_ = opened.Close()
						}
						if !errors.Is(err, persistence.ErrAuthenticationResetRequired) {
							t.Fatal("restored state allowed access without fresh authentication", err)
						}
					} else {
						if err == nil || !strings.Contains(err.Error(), tc.want) {
							t.Fatalf("expected %q, got %v", tc.want, err)
						}
						if _, err := os.Stat(destination); !os.IsNotExist(err) {
							t.Fatal("rejected backup published a destination", err)
						}
					}
				})
			}
			after, err := treeInventory(filepath.Join(backup, "data"))
			if err != nil || !sameInventory(before, after) {
				t.Fatal("restore modified the original backup state", err)
			}
		})
	}
}
