//go:build externaljobs

package management

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestStructuredWorkerOutcomeContract(t *testing.T) {
	c, b, input := workerOutcomeFixture("check", "failure")
	c.Spec.ProtocolVersion = "2"
	c.Spec.RejectionCodes = nil
	c.Spec.FailureCodes = []string{"custom.disk.full"}
	input.Failure = &api.WorkerFailure{Code: "custom.disk.full", Phase: "check"}
	d, _, err := validateWorkerOutcome(t.Context(), c, b, input)
	if err != nil || d.Failure.Code != input.Failure.Code || !d.Observation || d.Retryable {
		t.Fatal(d, err)
	}
	input.Failure.Code = "custom.not_declared"
	if _, _, err := validateWorkerOutcome(t.Context(), c, b, input); err == nil {
		t.Fatal("undeclared failure accepted")
	}
	input.Failure = &api.WorkerFailure{Code: "ca_file_missing", Phase: "local"}
	if _, _, err := validateWorkerOutcome(t.Context(), c, b, input); err == nil {
		t.Fatal("unavailable became target failure")
	}
	input.Status = "noData"
	d, _, err = validateWorkerOutcome(t.Context(), c, b, input)
	if err != nil || !d.Failure.MonitoringUnavailable() || d.Observation {
		t.Fatal(d, err)
	}
	c.Spec.ProtocolVersion = "1"
	c.Spec.FailureCodes = nil
	if _, _, err := validateWorkerOutcome(t.Context(), c, b, input); err == nil {
		t.Fatal("protocol1 accepted structured fields")
	}
}

func TestExternalRuleCollectionValidatesExactVocabularyBeforeActivation(t *testing.T) {
	for _, code := range []string{"custom.disk.full", "custom.disk.undeclared"} {
		t.Run(code, func(t *testing.T) {
			monitor := externalReferenceResource("check", false)
			var spec api.MonitorSpec
			if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Recovery = &api.RecoverySpec{TaxonomyVersion: "1", Rules: []api.RecoveryRule{{ID: "disk", Match: api.FailureMatch{Codes: []string{code}}, None: true}}}
			monitor.Spec, _ = json.Marshal(spec)
			inputs := []api.Resource{collectionMonitor("independent", "https://example.test"), monitor}
			f := stagedValidationFixture(t, inputs...)
			if _, err := f.store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			f.catalog = enabledReferenceCatalog(t, f.catalog)
			job := testJobType()
			job.Spec.ProtocolVersion, job.Spec.FailureCodes = "2", []string{"custom.disk.full"}
			commitTestJobType(t, f.catalog, job, "", true)
			before := f.store.Status().CommittedIndex
			local, localErr := validateCollection(t, f.catalog, collectionInputs(inputs...))
			staged, stagedErr, _ := stagedValidationRun(t, &f, collectionReadAll)
			planned, plan, planErr := compilePlanFixture(t, &f)
			want := code == "custom.disk.full"
			if local.Valid != want || staged.Valid != want || planned.Valid != want || (localErr == nil) != want || (stagedErr == nil) != want || (planErr == nil) != want || (plan != nil) != want {
				t.Fatalf("exact vocabulary differs across paths: local=%v/%v staged=%v/%v plan=%v/%v", local.Valid, localErr, staged.Valid, stagedErr, planned.Valid, planErr)
			}
			if f.store.Status().CommittedIndex != before {
				t.Fatal("validation mutated committed state")
			}
			for _, input := range inputs {
				if _, exists, err := f.store.CatalogGet(persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}); err != nil || exists {
					t.Fatal("preflight activated an item", err)
				}
			}
		})
	}
}
