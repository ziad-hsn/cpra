//go:build linux || darwin || windows

package localadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestStoppedBackupRestoreAfterActionRetirement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root := backupCollectionRoot(t)
	key := backupTestKey(t, root)
	config := runtimeconfig.Default()
	config.Storage.Directory = filepath.Join(root, "state")
	s, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	apply := func(command persistence.Command) persistence.Result {
		t.Helper()
		results, err := s.Submit(ctx, []persistence.Command{command})
		if err != nil || len(results) != 1 {
			t.Fatal("retention fixture submission", command.Kind, len(results), err)
		}
		if result := results[0]; result.Err != nil || command.Kind == "start" && !result.Allowed {
			t.Fatal("retention fixture transition", command.Kind, result.Err, "allowed", result.Allowed)
		}
		return results[0]
	}
	var ordinary []string
	var held persistence.Action
	// The old observation times are an age-boundary fixture, not a simulated
	// endurance run. All storage and backup operations use the real filesystem.
	at := time.Now().UTC().Add(-100 * 24 * time.Hour)
	for n := 0; n < 5; n++ {
		m := persistence.Monitor{ID: fmt.Sprintf("retention-%d", n), Revision: "one", Name: "Retention fixture",
			Policy: persistence.Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 1, RecoveryBypass: true, Endpoints: map[string]int{"red": 1}}}
		apply(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
		result := apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at.Add(time.Second)})
		if result.Monitor == nil || len(result.Monitor.Actions) != 1 {
			t.Fatal("fixture did not create its one notification")
		}
		var action persistence.Action
		for _, value := range result.Monitor.Actions {
			action = value
		}
		apply(persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID,
			NoticeRevision: persistence.ActionNoticeRevision(action), At: at.Add(2 * time.Second)})
		outcome := "success"
		if n == 4 {
			outcome = "failure"
		}
		apply(persistence.Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID,
			Outcome: outcome, Ambiguous: n == 4, At: at.Add(3 * time.Second)})
		apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: "success", At: at.Add(4 * time.Second)})
		if n == 4 {
			record, found, err := s.Action(action.ID)
			if err != nil || !found || record.Action.State != persistence.Unknown {
				t.Fatal("missing unknown fixture", err)
			}
			held = record.Action
		} else {
			ordinary = append(ordinary, action.ID)
		}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := s.Status()
		if !status.Ready {
			t.Fatal("retirement made durable storage unavailable", status.Error)
		}
		if status.Actions != nil && status.Actions.Retired == uint64(len(ordinary)) && status.Actions.OrdinaryRetained == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("bounded ordinary-action retirement did not complete")
		case <-ticker.C:
		}
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	backup, destination := filepath.Join(root, "backup"), filepath.Join(root, "restored")
	if err := Backup(config.Storage.Directory, backup, "", key); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(backup, "BACKUP.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		StateFormat int `json:"state_format"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.StateFormat != persistence.NotificationConditionFormatVersion {
		t.Fatal("backup did not preserve the committed built-in format", manifest.StateFormat, err)
	}
	if err := Restore(backup, destination, key); err != nil {
		t.Fatal(err)
	}
	config.Storage.Directory = destination
	admin, err := persistence.OpenAdministrative(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	authentication, err := admin.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	verifier := sha256.Sum256([]byte("retention-backup-fixture-only-operator"))
	if _, err := admin.CommitAuthentication(ctx, persistence.AuthenticationCommand{Mode: "provision", Epoch: authentication.Epoch,
		ExpectedEpoch: authentication.Epoch, ExpectedRevision: authentication.Revision, Revision: uuid.NewString(),
		Actor: "local-administrator", At: time.Now().UTC(), Principals: []persistence.AuthenticationPrincipal{{ID: "operator", Role: "operator", TokenSHA256: hex.EncodeToString(verifier[:])}}}); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, id := range ordinary {
		if _, found, err := restored.Action(id); err != nil || found {
			t.Fatal("restoration resurrected an aged ordinary action", err)
		}
	}
	record, found, err := restored.Action(held.ID)
	if err != nil || !found || record.Action.State != persistence.Unknown || !record.Held ||
		record.Action.DeliveryID != held.DeliveryID || record.Action.NotificationCorrelation != held.NotificationCorrelation || record.Action.Deadline != held.Deadline {
		t.Fatal("authenticated restore changed the unknown delivery evidence", err)
	}
}
