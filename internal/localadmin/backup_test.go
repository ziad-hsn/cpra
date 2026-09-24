package localadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestStoppedBackupRestorePreservesIncidentHistoryAndUnknown(t *testing.T) {
	root := t.TempDir()
	key := backupTestKey(t, root)
	source := filepath.Join(root, "state")
	backup := filepath.Join(root, "backup")
	restored := filepath.Join(root, "restored")
	c := runtimeconfig.Default()
	c.Storage.Directory = source
	s, err := persistence.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	m := persistence.Monitor{ID: "stable", Revision: "one", Name: "example", Policy: persistence.Policy{Interval: time.Second, Unhealthy: 1, Healthy: 2, Enabled: true, RecoveryBypass: true, Endpoints: map[string]int{"red": 2}}}
	apply := func(cmd persistence.Command) {
		t.Helper()
		if _, err := s.Submit(context.Background(), []persistence.Command{cmd}); err != nil {
			t.Fatal(err)
		}
	}
	apply(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: now})
	apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: now, Outcome: "failure", Scheduled: now})
	m, _ = s.Get(m.ID)
	if len(m.Actions) == 0 {
		t.Fatal("fixture did not create action")
	}
	var action string
	for id := range m.Actions {
		action = id
		break
	}
	apply(persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action, At: now})
	if err = Backup(source, backup, "", key); err == nil {
		t.Fatal("backup admitted a running store")
	}
	if _, err = os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("failed backup published a destination")
	}
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = Backup(source, backup, "", key); err != nil {
		t.Fatal(err)
	}
	if err = Restore(backup, restored, key); err != nil {
		t.Fatal(err)
	}
	if err = Restore(backup, restored, key); err == nil {
		t.Fatal("restore overwrote state")
	}
	c.Storage.Directory = restored
	if rejected, openErr := persistence.Open(context.Background(), c); !errors.Is(openErr, persistence.ErrAuthenticationResetRequired) {
		if rejected != nil {
			_ = rejected.Close()
		}
		t.Fatal("explicit restore reopened with old authentication", openErr)
	}
	admin, err := persistence.OpenAdministrative(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	state, err := admin.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("fresh-explicit-local-provisioning-token"))
	_, err = admin.CommitAuthentication(context.Background(), persistence.AuthenticationCommand{Mode: "provision", Epoch: state.Epoch,
		ExpectedEpoch: state.Epoch, ExpectedRevision: state.Revision, Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
		Principals: []persistence.AuthenticationPrincipal{{ID: "operator", Role: "operator", TokenSHA256: hex.EncodeToString(digest[:])}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = persistence.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	if !m.Incident || m.Actions[action].State != persistence.Unknown {
		t.Fatalf("unsafe recovery: %+v", m)
	}
	history, err := s.History().Page(m.ID, "", 100)
	if err != nil || len(history.Events) == 0 {
		t.Fatalf("history lost: %v %+v", err, history)
	}
	// Changing an inventory-covered file must prevent restoration entirely.
	if err = os.WriteFile(filepath.Join(backup, "data", "identity.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(backup, filepath.Join(root, "bad"), key); err == nil || !strings.Contains(err.Error(), "inventory mismatch") {
		t.Fatalf("corrupt backup accepted: %v", err)
	}
}

func TestBackupRejectsNestedAndLinkedState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "state")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := separatePaths(source, filepath.Join(source, "nested")); err == nil {
		t.Fatal("nested backup accepted")
	}
	link := filepath.Join(source, "secret")
	if err := os.Symlink(filepath.Join(root, "outside"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := treeInventory(source); err == nil {
		t.Fatal("symlink included in state inventory")
	}
}
