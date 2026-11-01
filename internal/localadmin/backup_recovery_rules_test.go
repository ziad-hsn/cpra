package localadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type ruleBackupPolicy struct {
	rules   persistence.RecoveryRules
	failure persistence.FailureDiagnostic
	verify  func(*testing.T, *persistence.Store)
}

func TestStoppedBackupRestorePreservesRecoveryRules(t *testing.T) {
	testStoppedRuleBackup(t, persistence.DispatchFormatVersion, func(t *testing.T, _ *persistence.Store) ruleBackupPolicy {
		rules, err := persistence.NewRecoveryRules("http", "", api.FailureTaxonomyVersion,
			[]api.RulePredicate{{ID: "restart", Match: api.FailureMatch{Codes: []string{"network.connection_refused"}}}},
			[]persistence.RecoveryRuleTarget{{RuleID: "restart", Driver: "webhook", Binding: strings.Repeat("a", 64), Budget: time.Minute}})
		if err != nil {
			t.Fatal(err)
		}
		failure, err := api.CanonicalFailure(api.FailureTaxonomyVersion, "network.connection_refused", "connect")
		if err != nil {
			t.Fatal(err)
		}
		return ruleBackupPolicy{rules: rules, failure: persistence.CheckFailure(failure)}
	})
}

func testStoppedRuleBackup(t *testing.T, wantFormat int, prepare func(*testing.T, *persistence.Store) ruleBackupPolicy) {
	t.Helper()
	for _, phase := range []string{"log-only", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			root := backupCollectionRoot(t)
			key := backupTestKey(t, root)
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = filepath.Join(root, "state")
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			verifier := sha256.Sum256([]byte("backup-rules-fixture-operator-before-restore"))
			if _, err := store.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
				Principals: []persistence.AuthenticationPrincipal{{ID: "oncall", Role: "operator", TokenSHA256: hex.EncodeToString(verifier[:])}}}); err != nil {
				t.Fatal(err)
			}
			policy := prepare(t, store)
			apply := func(command persistence.Command) persistence.Result {
				t.Helper()
				results, err := store.Submit(t.Context(), []persistence.Command{command})
				if err != nil || len(results) != 1 || results[0].Err != nil {
					t.Fatal("rule fixture submission", command.Kind, results, err)
				}
				return results[0]
			}
			before := map[string]persistence.Monitor{}
			actions := map[string]persistence.Action{}
			for _, id := range []string{"queued", "started"} {
				at := time.Now().UTC()
				revision := sha256.Sum256([]byte("backup-rule-config/" + id))
				m := persistence.Monitor{ID: id, Name: id, Revision: hex.EncodeToString(revision[:]), Policy: persistence.Policy{
					Enabled: true, Interval: time.Second, Unhealthy: 2, Healthy: 2, Intervention: true, RecoveryMaxAttempts: 3, RecoveryRules: policy.rules}}
				apply(persistence.Command{Kind: "configure", MonitorID: id, Revision: m.Revision, Config: &m, At: at})
				for generation := uint64(1); generation <= 2; generation++ {
					apply(persistence.Command{Kind: "pulse", MonitorID: id, Revision: m.Revision, Generation: generation, Outcome: "failure", Failure: policy.failure, At: at.Add(time.Duration(generation) * time.Millisecond)})
				}
				m, found := monitorWithActionHistory(t, store, id)
				if !found || len(m.Actions) != 1 || m.RecoveryDecision.Count != 2 {
					t.Fatal("fixture did not qualify one rule action")
				}
				var action persistence.Action
				for _, value := range m.Actions {
					action = value
				}
				if action.Rule.RuleID != "restart" || action.State != persistence.Queued || action.Attempt != 1 || !action.Deadline.IsZero() || action.ExecutionBudget <= 0 {
					t.Fatal("fixture action lost its qualified binding")
				}
				if id == "started" {
					result := apply(persistence.Command{Kind: "start", MonitorID: id, Revision: m.Revision, ActionID: action.ID, Driver: action.Rule.Target.Driver,
						NoticeRevision: persistence.ActionNoticeRevision(action), At: at.Add(3 * time.Millisecond)})
					if !result.Allowed {
						t.Fatal("fixture start was denied")
					}
					m, _ = monitorWithActionHistory(t, store, id)
					action = m.Actions[action.ID]
					if m.RecoveryDecision.Selection != action.Rule || action.State != persistence.Started || !action.Deadline.Equal(action.StartedAt.Add(action.ExecutionBudget)) {
						t.Fatal("first start did not pin selection")
					}
				}
				before[id], actions[id] = m, action
			}
			if phase == "snapshot" {
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			backup, restored := filepath.Join(root, "backup"), filepath.Join(root, "restored")
			if err := Backup(cfg.Storage.Directory, backup, "", key); err != nil {
				t.Fatal(err)
			}
			manifest, err := readBackupManifest(filepath.Join(backup, "BACKUP.json"))
			if err != nil || manifest.StateFormat != wantFormat {
				t.Fatal("wrong rule backup format", manifest.StateFormat, wantFormat, err)
			}
			if err := Restore(backup, restored, key); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(restored, "RESTORE.json"))
			if err != nil {
				t.Fatal(err)
			}
			var marker persistence.RestoreMarker
			if err := json.Unmarshal(data, &marker); err != nil || marker.At.IsZero() {
				t.Fatal("restore time missing", err)
			}
			cfg.Storage.Directory = restored
			if opened, err := persistence.Open(t.Context(), cfg); !errors.Is(err, persistence.ErrAuthenticationResetRequired) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatal("restored rule state did not require fresh authority", err)
			}
			admin, err := persistence.OpenAdministrative(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = admin.Close() })
			auth, err := admin.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			fresh := sha256.Sum256([]byte("backup-rules-fixture-operator-after-restore"))
			if _, err := admin.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "provision", Epoch: auth.Epoch,
				ExpectedEpoch: auth.Epoch, ExpectedRevision: auth.Revision, Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
				Principals: []persistence.AuthenticationPrincipal{{ID: "oncall", Role: "operator", TokenSHA256: hex.EncodeToString(fresh[:])}}}); err != nil {
				t.Fatal(err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if policy.verify != nil {
				policy.verify(t, reopened)
			}
			for _, id := range []string{"queued", "started"} {
				m, found := reopened.Get(id)
				old := before[id]
				if !found || m.Policy.RecoveryRules.Digest() != policy.rules.Digest() || m.Policy.RecoveryRules.Taxonomy() != policy.rules.Taxonomy() ||
					m.RecoveryDecision != old.RecoveryDecision || m.InterventionAttempts != old.InterventionAttempts || m.LastFailure != old.LastFailure ||
					m.FileIncarnation != old.FileIncarnation || m.IncidentID != old.IncidentID {
					t.Fatal("restore changed rule policy, qualification, selection or incident identity")
				}
				original := actions[id]
				record, found, err := reopened.Action(original.ID)
				if err != nil || !found {
					t.Fatal("restored action missing", err)
				}
				want := original
				want.State, want.Outcome = persistence.Cancelled, "explicit_restore"
				if id == "started" {
					want.State, want.Outcome = persistence.Unknown, "explicit_restore_while_started"
				}
				want.FinishedAt = marker.At
				if want.FinishedAt.Before(original.StartedAt) {
					want.FinishedAt = original.StartedAt
				}
				if !reflect.DeepEqual(record.Action, want) || record.Held != (id == "started") {
					t.Fatal("restore changed original action facts or failed to fence execution")
				}
				results, err := reopened.Submit(t.Context(), []persistence.Command{{Kind: "start", MonitorID: id, Revision: m.Revision, ActionID: original.ID,
					Driver: original.Rule.Target.Driver, NoticeRevision: persistence.ActionNoticeRevision(record.Action), At: time.Now().UTC()}})
				if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Allowed {
					t.Fatal("restored original action regained execution permission", err)
				}
			}
		})
	}
}
