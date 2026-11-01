//go:build externaljobs

package localadmin

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestStoppedBackupRestorePreservesExternalRecoveryRules(t *testing.T) {
	testStoppedRuleBackup(t, persistence.WorkerDispatchFormatVersion, func(t *testing.T, store *persistence.Store) ruleBackupPolicy {
		at := time.Now().UTC()
		authority, err := store.ObserveOperatorAuthority(t.Context(), "oncall", at)
		if err != nil {
			t.Fatal(err)
		}
		wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{42}, 32))
		if err != nil {
			t.Fatal(err)
		}
		sealer, err := secureconfig.NewSealer(wrapper)
		if err != nil {
			t.Fatal(err)
		}
		record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "JobType", ID: "backup-rule-check"},
			UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "job-type", CreatedAt: at, UpdatedAt: at}
		record.Payload, err = sealer.Seal(t.Context(), record.Binding(store.Status().NodeID), []byte("fixture-job-type-schema"))
		if err != nil {
			t.Fatal(err)
		}
		job, err := store.CommitJobType(t.Context(), persistence.JobTypeCommand{Action: "create", Authority: authority,
			Value: persistence.JobTypeVersion{Record: record, Version: "v1", Category: "check", Handler: "probe", ProtocolVersion: "2", SchemaProfile: "cpra.schema.v1", FailureCodes: []string{"custom.cache.full"}}}, at)
		if err != nil {
			t.Fatal(err)
		}
		contract := job.Current.FailureContract()
		rules, err := persistence.NewExternalRecoveryRules(contract, api.FailureTaxonomyVersion,
			[]api.RulePredicate{{ID: "restart", Match: api.FailureMatch{Codes: []string{"network.connection_refused", "custom.cache.full"}}}},
			[]persistence.RecoveryRuleTarget{{RuleID: "restart", Driver: "webhook", Binding: strings.Repeat("b", 64), Budget: time.Minute}})
		if err != nil {
			t.Fatal(err)
		}
		failure, err := api.CanonicalWorkerFailure(contract, api.WorkerFailure{Code: "network.connection_refused", Phase: "connect"})
		if err != nil {
			t.Fatal(err)
		}
		// The built-in classification can be committed directly. Custom reports
		// require the authenticated worker-result path, tested separately. Here
		// the custom declaration and matcher cross the actual restore boundary.
		// No worker, handler, controller or provider client is started.
		return ruleBackupPolicy{rules: rules, failure: persistence.CheckFailure(failure), verify: func(t *testing.T, reopened *persistence.Store) {
			restored, found, err := reopened.JobType(t.Context(), record.Key.ID)
			if err != nil || !found || !reflect.DeepEqual(restored, job) {
				t.Fatal("restore changed the immutable external failure contract", err)
			}
			custom, err := api.CanonicalWorkerFailure(restored.Current.FailureContract(), api.WorkerFailure{Code: "custom.cache.full", Phase: "check"})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"queued", "started"} {
				m, found := reopened.Get(id)
				decision := m.Policy.RecoveryRules.Match(persistence.CheckFailure(custom))
				if !found || !decision.Matched || !decision.Intervention || decision.RuleID != "restart" {
					t.Fatal("restore changed custom failure matching")
				}
			}
		}}
	})
}
