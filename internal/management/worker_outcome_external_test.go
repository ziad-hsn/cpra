//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func workerOutcomeFixture(category, status string) (api.JobType, WorkerOutcomeBinding, api.Outcome) {
	contract := testJobType()
	contract.Metadata.UID, contract.Metadata.ResourceVersion = "type-uid", "type-version-1"
	contract.Spec.Kind = category
	contract.Spec.RejectionCodes = []string{"busy"}
	contract.Spec.ResultSchema = json.RawMessage(`{"type":"object","required":["count"],"properties":{"count":{"type":"integer"}},"additionalProperties":false}`)
	binding := WorkerOutcomeBinding{ServerID: "server", WorkerUID: "worker", ExecutionID: "execution", ExecutionRevision: "execution-version", GrantID: "grant",
		JobType: persistence.JobTypeReference{JobTypeID: contract.Metadata.ID, JobTypeUID: contract.Metadata.UID, Version: contract.Spec.Version, Revision: contract.Metadata.ResourceVersion, Category: category}}
	input := api.Outcome{ServerID: binding.ServerID, WorkerUID: binding.WorkerUID, ExecutionID: binding.ExecutionID, GrantID: binding.GrantID, Kind: category,
		Status: status, Evidence: []string{}, Data: json.RawMessage(`{"count":1}`)}
	if status == "rejected" {
		input.RejectionCode = "busy"
	}
	return contract, binding, input
}

func TestWorkerOutcomeClassifications(t *testing.T) {
	for category, statuses := range map[string][]string{
		"check":        {"success", "failure", "noData"},
		"recovery":     {"accepted", "completed", "rejected", "unknown"},
		"notification": {"accepted", "delivered", "rejected", "unknown"},
	} {
		for _, status := range statuses {
			t.Run(category+"/"+status, func(t *testing.T) {
				contract, binding, input := workerOutcomeFixture(category, status)
				got, raw, err := validateWorkerOutcome(t.Context(), contract, binding, input)
				if err != nil || got.Status != status || got.Retryable != (status == "rejected") || got.Observation != (category == "check" && status != "noData") {
					t.Fatalf("classification changed: %+v %v", got, err)
				}
				var decoded api.Outcome
				if api.StrictDecode(raw, &decoded) != nil || !reflect.DeepEqual(decoded, input) {
					t.Fatal("validated report changed")
				}
			})
		}
	}
}

func TestWorkerOutcomeInterruptedReportWithoutSupplementalData(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		status := "unknown"
		if category == "check" {
			status = "noData"
		}
		contract, binding, input := workerOutcomeFixture(category, status)
		for _, data := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" \n null\t ")} {
			input.Data = data
			if _, _, err := validateWorkerOutcome(t.Context(), contract, binding, input); err != nil {
				t.Fatalf("interrupted %s rejected by handler result schema: %v", category, err)
			}
		}
		input.Data = json.RawMessage(`{"unexpected":true}`)
		if _, _, err := validateWorkerOutcome(t.Context(), contract, binding, input); !errors.Is(err, ErrJobValueInvalid) {
			t.Fatal("supplemental interrupted report bypassed schema", err)
		}
		if category == "check" {
			input.Status = "success"
		} else {
			input.Status = "accepted"
		}
		input.Data = nil
		if _, _, err := validateWorkerOutcome(t.Context(), contract, binding, input); !errors.Is(err, ErrJobValueInvalid) {
			t.Fatal("confirmed result bypassed its required schema", err)
		}
	}
}

func TestWorkerOutcomeRejectsIdentityAndShapeWithoutEchoingData(t *testing.T) {
	canary := "private-result-canary"
	tests := map[string]func(*api.Outcome){
		"server":                 func(v *api.Outcome) { v.ServerID = canary },
		"worker":                 func(v *api.Outcome) { v.WorkerUID = canary },
		"execution":              func(v *api.Outcome) { v.ExecutionID = canary },
		"grant":                  func(v *api.Outcome) { v.GrantID = canary },
		"category":               func(v *api.Outcome) { v.Kind = "check" },
		"status":                 func(v *api.Outcome) { v.Status = "success" },
		"missing-code":           func(v *api.Outcome) { v.Status = "rejected" },
		"unregistered-code":      func(v *api.Outcome) { v.Status, v.RejectionCode = "rejected", canary },
		"code-without-rejection": func(v *api.Outcome) { v.RejectionCode = "busy" },
		"null-evidence":          func(v *api.Outcome) { v.Evidence = nil },
		"evidence-count":         func(v *api.Outcome) { v.Evidence = make([]string, 65) },
		"evidence-size":          func(v *api.Outcome) { v.Evidence = []string{strings.Repeat("x", 4097)} },
		"evidence-line":          func(v *api.Outcome) { v.Evidence = []string{canary + "\n"} },
		"diagnostic-size":        func(v *api.Outcome) { v.Diagnostic = strings.Repeat("x", 65537) },
		"diagnostic-utf8":        func(v *api.Outcome) { v.Diagnostic = string([]byte{0xff}) },
		"data-size":              func(v *api.Outcome) { v.Data = json.RawMessage(strings.Repeat("x", workerOutcomeBytes+1)) },
		"data-utf8":              func(v *api.Outcome) { v.Data = json.RawMessage{0xff} },
		"data-shape":             func(v *api.Outcome) { v.Data = json.RawMessage(`{"count":"` + canary + `"}`) },
		"data-duplicate":         func(v *api.Outcome) { v.Data = json.RawMessage(`{"count":1,"count":2}`) },
		"envelope-size": func(v *api.Outcome) {
			v.Diagnostic = strings.Repeat("x", 64<<10)
			v.Evidence = make([]string, 64)
			for i := range v.Evidence {
				v.Evidence[i] = strings.Repeat("x", 4096)
			}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			contract, binding, input := workerOutcomeFixture("recovery", "completed")
			change(&input)
			_, raw, err := validateWorkerOutcome(t.Context(), contract, binding, input)
			if err == nil || raw != nil || strings.Contains(err.Error(), canary) {
				t.Fatalf("invalid outcome accepted or exposed: %v", err)
			}
		})
	}
	contract, binding, input := workerOutcomeFixture("check", "success")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := validateWorkerOutcome(ctx, contract, binding, input); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, _, err := validateWorkerOutcome(nil, contract, binding, input); !errors.Is(err, ErrWorkerOutcomeInvalid) {
		t.Fatal("nil context accepted", err)
	}
}

func TestWorkerOutcomePreparationPinsRetainedContractAndSealsReport(t *testing.T) {
	c, store := jobTypeCatalog(t)
	resource := testJobType()
	resource.Spec.ResultSchema = json.RawMessage(`{"type":"object","required":["old"],"properties":{"old":{"type":"string"}},"additionalProperties":false}`)
	resource = commitTestJobType(t, c, resource, "", true)
	_, binding, input := workerOutcomeFixture("check", "success")
	binding.JobType = persistence.JobTypeReference{JobTypeID: resource.Metadata.ID, JobTypeUID: resource.Metadata.UID, Version: resource.Spec.Version, Revision: resource.Metadata.ResourceVersion, Category: resource.Spec.Kind}
	canary := "private-worker-result-canary"
	input.Data = json.RawMessage(`{"old":"` + canary + `"}`)
	input.Diagnostic, input.Evidence = canary, []string{canary}
	resource.Spec.Version = "2"
	resource.Spec.ResultSchema = json.RawMessage(`{"const":"new-result-contract"}`)
	resource = commitTestJobType(t, c, resource, resource.Metadata.ResourceVersion, false)
	deleted, err := c.PrepareDeleteJobType(t.Context(), resource.Metadata.ID, resource.Metadata.ResourceVersion, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.CommitJobType(t.Context(), deleted); err != nil {
		t.Fatal(err)
	}
	// Old executions retain their original contract even across type recreation.
	replacement := testJobType()
	replacement.Spec.Version = "3"
	commitTestJobType(t, c, replacement, "", true)
	before, _, _ := store.JobType(t.Context(), resource.Metadata.ID)
	prepared, err := c.PrepareWorkerOutcome(t.Context(), binding, input)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := c.PrepareWorkerOutcome(t.Context(), binding, input)
	if err != nil || repeated.Digest() != prepared.Digest() || prepared.Binding() != binding {
		t.Fatal("identical outcome lost replay identity", err)
	}
	if reflect.DeepEqual(repeated.Envelope(), prepared.Envelope()) {
		t.Fatal("sealing reused encryption randomness")
	}
	encoded, err := json.Marshal(prepared.Envelope())
	if err != nil || bytes.Contains(encoded, []byte(canary)) {
		t.Fatal("plaintext escaped sealed report", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, prepared), canary) {
			t.Fatal("format exposed report")
		}
	}
	if _, err := json.Marshal(prepared); err == nil {
		t.Fatal("private preparation serialized")
	}
	plain, err := c.sealer.Open(t.Context(), binding.encryptionBinding(c.storeID), prepared.Envelope())
	if err != nil || !bytes.Contains(plain, []byte(canary)) {
		t.Fatal("report not recoverable", err)
	}
	clear(plain)
	for _, field := range []string{"server", "worker", "execution", "revision", "grant", "type-version"} {
		changed := binding
		switch field {
		case "server":
			changed.ServerID += "-other"
		case "worker":
			changed.WorkerUID += "-other"
		case "execution":
			changed.ExecutionID += "-other"
		case "revision":
			changed.ExecutionRevision += "-other"
		case "grant":
			changed.GrantID += "-other"
		case "type-version":
			changed.JobType.Version += "-other"
		}
		if _, err := c.sealer.Open(t.Context(), changed.encryptionBinding(c.storeID), prepared.Envelope()); err == nil {
			t.Fatalf("ciphertext moved across %s binding", field)
		}
	}
	copy := prepared.Envelope()
	copy.Ciphertext[0] ^= 1
	if _, err := c.sealer.Open(t.Context(), binding.encryptionBinding(c.storeID), prepared.Envelope()); err != nil {
		t.Fatal("caller mutated retained ciphertext")
	}
	after, _, _ := store.JobType(t.Context(), resource.Metadata.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preparation changed durable contract")
	}
	wrong := binding
	wrong.JobType.Revision = "not-the-committed-version"
	if _, err := c.PrepareWorkerOutcome(t.Context(), wrong, input); !errors.Is(err, persistence.ErrCatalogDependency) {
		t.Fatal("wrong pinned revision accepted", err)
	}
	wrongInput := input
	wrongInput.Data = json.RawMessage(`"new-result-contract"`)
	if _, err := c.PrepareWorkerOutcome(t.Context(), binding, wrongInput); !errors.Is(err, ErrJobValueInvalid) {
		t.Fatal("new schema substituted for old", err)
	}
	if !c.Ready() {
		t.Fatal("invalid worker report poisoned catalog")
	}
}

type workerPreparationCancelWrapper struct {
	secureconfig.KeyWrapper
	cancel context.CancelFunc
}

func (w workerPreparationCancelWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	wrapped, err := w.KeyWrapper.Wrap(ctx, key, aad)
	w.cancel()
	return wrapped, err
}

func TestWorkerPreparationCancellationDuringSealing(t *testing.T) {
	for _, operation := range []string{"assignment", "outcome"} {
		t.Run(operation, func(t *testing.T) {
			c, store, intent := workerAssignmentFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			c.sealer, err = secureconfig.NewSealer(workerPreparationCancelWrapper{KeyWrapper: wrapper, cancel: cancel})
			if err != nil {
				t.Fatal(err)
			}
			if operation == "assignment" {
				prepared, err := c.PrepareWorkerExecution(ctx, intent)
				if !errors.Is(err, context.Canceled) || len(prepared.Payload.Ciphertext) != 0 {
					t.Fatal("cancelled assignment returned prepared ciphertext", err)
				}
			} else {
				_, binding, input := workerOutcomeFixture("check", "success")
				binding.JobType = intent.JobType
				input.Data = json.RawMessage(`{}`)
				prepared, err := c.PrepareWorkerOutcome(ctx, binding, input)
				if !errors.Is(err, context.Canceled) || prepared.Digest() != "" || len(prepared.Envelope().Ciphertext) != 0 {
					t.Fatal("cancelled outcome returned preparation", err)
				}
			}
			if _, found, err := store.WorkerExecution(t.Context(), intent.ID); err != nil || found {
				t.Fatal("preparation committed an execution", err)
			}
			if !c.Ready() {
				t.Fatal("cancelled sealing poisoned catalog")
			}
		})
	}
}
