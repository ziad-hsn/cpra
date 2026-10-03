//go:build externaljobs

package management

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerEvidenceAuthenticatesOriginalContractAndReceipt(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	resource := testJobType()
	resource.Spec.ResultSchema = json.RawMessage(`{"const":"private-evidence-canary"}`)
	resource = commitTestJobType(t, c, resource, "", true)
	_, binding, _ := workerOutcomeFixture("check", "noData")
	binding.JobType = persistence.JobTypeReference{JobTypeID: resource.Metadata.ID, JobTypeUID: resource.Metadata.UID, Version: resource.Spec.Version,
		Revision: resource.Metadata.ResourceVersion, Category: resource.Spec.Kind}
	record := persistence.WorkerExecutionRecord{
		Intent:    persistence.WorkerExecutionIntent{ID: binding.ExecutionID, Revision: binding.ExecutionRevision, JobType: binding.JobType, Category: "check"},
		Lifecycle: &persistence.WorkerExecutionLifecycle{ServerID: binding.ServerID, WorkerUID: binding.WorkerUID, GrantID: binding.GrantID},
		Result:    &persistence.WorkerResultRecord{Receipt: persistence.WorkerReceipt{ReceiptID: "original-receipt"}},
	}
	input := api.LateEvidenceRequest{ServerID: binding.ServerID, WorkerUID: binding.WorkerUID, ExecutionID: binding.ExecutionID, OriginalReceiptID: "original-receipt",
		EvidenceID: "evidence-one", Diagnostic: "private-diagnostic-canary", Evidence: []string{"private-reference-canary"}, Data: json.RawMessage(`"private-evidence-canary"`)}
	encode := func(r *persistence.WorkerExecutionRecord, value api.LateEvidenceRequest, checked bool) {
		t.Helper()
		var raw []byte
		var err error
		if checked {
			raw, err = validateWorkerEvidence(t.Context(), resource, *r, value)
		} else {
			raw, err = json.Marshal(value)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer clear(raw)
		key := workerEvidenceBinding(c.storeID, *r, value.EvidenceID)
		payload, err := c.sealer.Seal(t.Context(), key, raw)
		if err != nil {
			t.Fatal(err)
		}
		r.Evidence = []persistence.WorkerEvidenceRecord{{Input: persistence.WorkerEvidenceInput{EvidenceID: value.EvidenceID,
			Digest: workerEvidenceDigest(key, raw), Payload: payload}}}
	}
	encode(&record, input, true)
	if err := c.verifyWorkerEvidence(t.Context(), record); err != nil {
		t.Fatal("valid evidence rejected", err)
	}
	for name, change := range map[string]func(*persistence.WorkerExecutionRecord){
		"ciphertext":  func(r *persistence.WorkerExecutionRecord) { r.Evidence[0].Input.Payload.Ciphertext[0] ^= 1 },
		"key":         func(r *persistence.WorkerExecutionRecord) { r.Evidence[0].Input.Payload.KeyID = "missing-key" },
		"digest":      func(r *persistence.WorkerExecutionRecord) { r.Evidence[0].Input.Digest = "different-evidence" },
		"evidence-id": func(r *persistence.WorkerExecutionRecord) { r.Evidence[0].Input.EvidenceID = "different-id" },
		"receipt":     func(r *persistence.WorkerExecutionRecord) { r.Result.Receipt.ReceiptID = "different-receipt" },
		"worker":      func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.WorkerUID = "different-worker" },
		"server":      func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.ServerID = "different-server" },
		"grant":       func(r *persistence.WorkerExecutionRecord) { r.Lifecycle.GrantID = "different-grant" },
		"contract":    func(r *persistence.WorkerExecutionRecord) { r.Intent.JobType.Revision = "different-contract" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := record.Clone()
			change(&bad)
			if c.verifyWorkerEvidence(t.Context(), bad) == nil {
				t.Fatal("unauthenticated evidence accepted")
			}
		})
	}
	for name, change := range map[string]func(*api.LateEvidenceRequest){
		"schema":          func(in *api.LateEvidenceRequest) { in.Data = json.RawMessage(`"other-schema"`) },
		"identity":        func(in *api.LateEvidenceRequest) { in.ExecutionID = "another-execution" },
		"evidence-id":     func(in *api.LateEvidenceRequest) { in.EvidenceID = "" },
		"nil-references":  func(in *api.LateEvidenceRequest) { in.Evidence = nil },
		"diagnostic":      func(in *api.LateEvidenceRequest) { in.Diagnostic = strings.Repeat("x", (64<<10)+1) },
		"reference-count": func(in *api.LateEvidenceRequest) { in.Evidence = make([]string, 65) },
		"reference-size":  func(in *api.LateEvidenceRequest) { in.Evidence = []string{strings.Repeat("x", 4097)} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := input
			change(&bad)
			if raw, err := validateWorkerEvidence(t.Context(), resource, record, bad); err == nil {
				clear(raw)
				t.Fatal("invalid evidence prepared")
			}
			copy := record.Clone()
			encode(&copy, bad, false)
			if err := c.verifyWorkerEvidence(t.Context(), copy); err == nil {
				t.Fatal("encryption hid invalid evidence")
			}
		})
	}
	resource.Spec.Version = "next"
	resource.Spec.ResultSchema = json.RawMessage(`{"const":"changed-contract"}`)
	commitTestJobType(t, c, resource, resource.Metadata.ResourceVersion, false)
	if err := c.verifyWorkerEvidence(t.Context(), record); err != nil {
		t.Fatal("new contract reinterpreted old evidence", err)
	}
}
