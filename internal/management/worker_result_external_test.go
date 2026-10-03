//go:build externaljobs

package management

import (
	"encoding/json"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestWorkerResultRetainedCiphertextAndClassification(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	resource := testJobType()
	resource.Spec.ResultSchema = json.RawMessage(`{"const":"private-result-canary"}`)
	resource = commitTestJobType(t, c, resource, "", true)
	_, binding, input := workerOutcomeFixture("check", "success")
	binding.JobType = persistence.JobTypeReference{JobTypeID: resource.Metadata.ID, JobTypeUID: resource.Metadata.UID,
		Version: resource.Spec.Version, Revision: resource.Metadata.ResourceVersion, Category: resource.Spec.Kind}
	input.Data = json.RawMessage(`"private-result-canary"`)
	prepared, err := c.PrepareWorkerOutcome(t.Context(), binding, input)
	if err != nil {
		t.Fatal(err)
	}
	record := persistence.WorkerExecutionRecord{
		Intent:    persistence.WorkerExecutionIntent{ID: binding.ExecutionID, Revision: binding.ExecutionRevision, JobType: binding.JobType},
		Lifecycle: &persistence.WorkerExecutionLifecycle{ServerID: binding.ServerID, WorkerUID: binding.WorkerUID, GrantID: binding.GrantID},
		Result:    &persistence.WorkerResultRecord{Input: persistence.WorkerResultInput{Digest: prepared.Digest(), Status: "success", Payload: prepared.Envelope()}},
	}
	if err := c.verifyWorkerOutcome(t.Context(), record); err != nil {
		t.Fatal("valid retained outcome", err)
	}
	for name, change := range map[string]func(*persistence.WorkerExecutionRecord){
		"ciphertext": func(r *persistence.WorkerExecutionRecord) { r.Result.Input.Payload.Ciphertext[0] ^= 1 },
		"key":        func(r *persistence.WorkerExecutionRecord) { r.Result.Input.Payload.KeyID = "missing-key" },
		"digest":     func(r *persistence.WorkerExecutionRecord) { r.Result.Input.Digest = "different-result" },
		"status":     func(r *persistence.WorkerExecutionRecord) { r.Result.Input.Status = "failure" },
		"retry":      func(r *persistence.WorkerExecutionRecord) { r.Result.Input.Retryable = true },
		"worker":     func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.WorkerUID = "different-worker" },
		"server":     func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.ServerID = "different-server" },
		"grant":      func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.GrantID = "different-grant" },
		"revision":   func(r *persistence.WorkerExecutionRecord) { r.Intent.Revision = "different-execution-version" },
		"type":       func(r *persistence.WorkerExecutionRecord) { r.Intent.JobType.Version = "different-version" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := record.Clone()
			change(&bad)
			if err := c.verifyWorkerOutcome(t.Context(), bad); err == nil {
				t.Fatal("unauthenticated retained result accepted")
			}
		})
	}
	for name, raw := range map[string][]byte{
		"malformed":     []byte(`{"status":`),
		"unknown-field": []byte(`{"private":"private-result-canary"}`),
		"wrong-schema":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			if raw == nil {
				bad := input
				bad.Data = json.RawMessage(`"another-result"`)
				var err error
				raw, err = json.Marshal(bad)
				if err != nil {
					t.Fatal(err)
				}
			}
			bad := record.Clone()
			var err error
			bad.Result.Input.Payload, err = c.sealer.Seal(t.Context(), binding.encryptionBinding(c.storeID), raw)
			if err != nil {
				t.Fatal(err)
			}
			bad.Result.Input.Digest = workerOutcomeDigest(binding, raw)
			if err := c.verifyWorkerOutcome(t.Context(), bad); err == nil {
				t.Fatal("valid encryption hid an invalid result contract")
			}
		})
	}
	// Replacing the current contract must not reinterpret an old result.
	resource.Spec.Version = "next"
	resource.Spec.ResultSchema = json.RawMessage(`{"const":"new-contract"}`)
	commitTestJobType(t, c, resource, resource.Metadata.ResourceVersion, false)
	if err := c.verifyWorkerOutcome(t.Context(), record); err != nil {
		t.Fatal("current schema replaced original outcome contract", err)
	}
}
