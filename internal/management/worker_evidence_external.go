//go:build externaljobs

package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var ErrWorkerEvidenceInvalid = errors.New("invalid worker evidence")

func workerEvidenceBinding(storeID string, r persistence.WorkerExecutionRecord, evidenceID string) secureconfig.Binding {
	receipt, _ := r.TerminalReceipt()
	raw, _ := json.Marshal(struct {
		Execution  WorkerOutcomeBinding `json:"execution"`
		ReceiptID  string               `json:"receipt_id"`
		EvidenceID string               `json:"evidence_id"`
	}{workerOutcomeBinding(r), receipt.ReceiptID, evidenceID})
	digest := sha256.Sum256(append([]byte("cpra/worker/evidence-binding/v1\x00"), raw...))
	return secureconfig.Binding{StoreID: storeID, Kind: "WorkerExecution", ID: r.Intent.ID,
		UID: r.Lifecycle.WorkerUID, Revision: hex.EncodeToString(digest[:]), Purpose: "worker-evidence-v1"}
}

func workerEvidenceDigest(binding secureconfig.Binding, raw []byte) string {
	id, _ := json.Marshal(binding)
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/worker/evidence/v1\x00"))
	_, _ = h.Write(id)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func validateWorkerEvidence(ctx context.Context, contract api.JobType, r persistence.WorkerExecutionRecord, input api.LateEvidenceRequest) ([]byte, error) {
	receipt, terminal := r.TerminalReceipt()
	if r.Lifecycle == nil || !terminal || input.ServerID != r.Lifecycle.ServerID || input.WorkerUID != r.Lifecycle.WorkerUID ||
		input.ExecutionID != r.Intent.ID || input.OriginalReceiptID != receipt.ReceiptID || input.EvidenceID == "" ||
		!workerOutcomeText(input.EvidenceID, 256) || strings.TrimSpace(input.EvidenceID) != input.EvidenceID {
		return nil, ErrWorkerEvidenceInvalid
	}
	// Reuse the original supplemental-result schema and limits. An opaque data
	// document cannot change the original classification or prove handler exit.
	status := "unknown"
	if r.Intent.Category == "check" {
		status = "noData"
	}
	_, checked, err := validateWorkerOutcome(ctx, contract, workerOutcomeBinding(r), api.Outcome{
		ServerID: input.ServerID, WorkerUID: input.WorkerUID, ExecutionID: input.ExecutionID, GrantID: r.Lifecycle.GrantID,
		Kind: r.Intent.Category, Status: status, Data: input.Data, Diagnostic: input.Diagnostic, Evidence: input.Evidence})
	clear(checked)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > workerOutcomeBytes {
		clear(raw)
		return nil, ErrWorkerEvidenceInvalid
	}
	if err := ctx.Err(); err != nil {
		clear(raw)
		return nil, err
	}
	return raw, nil
}

// CommitWorkerEvidence retains the first report for each evidence ID. It does
// not resolve an action, replace a review, or accept a new health observation.
func (c *Catalog) CommitWorkerEvidence(ctx context.Context, authority persistence.WorkerAuthority, input api.LateEvidenceRequest) (persistence.WorkerReceipt, error) {
	if ctx == nil || input.WorkerUID == "" || !workerOutcomeText(input.WorkerUID, 256) {
		return persistence.WorkerReceipt{}, ErrWorkerEvidenceInvalid
	}
	if input.WorkerUID != authority.WorkerUID {
		return persistence.WorkerReceipt{}, persistence.ErrWorkerAuthorityDenied
	}
	if err := c.readyContext(ctx); err != nil {
		return persistence.WorkerReceipt{}, err
	}
	r, err := c.store.WorkerEvidenceExecution(ctx, authority, input.ServerID, input.ExecutionID, input.OriginalReceiptID)
	if err != nil {
		return persistence.WorkerReceipt{}, err
	}
	if len(input.Data) > workerOutcomeBytes || len(input.Evidence) > 64 {
		return persistence.WorkerReceipt{}, ErrWorkerEvidenceInvalid
	}
	input.Data = slices.Clone(input.Data)
	defer clear(input.Data)
	input.Evidence = slices.Clone(input.Evidence)
	contract, err := c.workerOutcomeContract(ctx, workerOutcomeBinding(r))
	if err != nil {
		return persistence.WorkerReceipt{}, err
	}
	raw, err := validateWorkerEvidence(ctx, contract, r, input)
	if err != nil {
		return persistence.WorkerReceipt{}, err
	}
	defer clear(raw)
	binding := workerEvidenceBinding(c.storeID, r, input.EvidenceID)
	payload, err := c.sealer.Seal(ctx, binding, raw)
	if err != nil {
		if ctx.Err() != nil {
			return persistence.WorkerReceipt{}, ctx.Err()
		}
		return persistence.WorkerReceipt{}, ErrUnavailable
	}
	return c.store.CommitWorkerEvidence(ctx, authority, persistence.WorkerEvidenceInput{
		ServerID: r.Lifecycle.ServerID, ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision,
		GrantID: r.Lifecycle.GrantID, OriginalReceiptID: input.OriginalReceiptID, EvidenceID: input.EvidenceID,
		Digest: workerEvidenceDigest(binding, raw), Payload: payload})
}

func (c *Catalog) verifyWorkerEvidence(ctx context.Context, record persistence.WorkerExecutionRecord) error {
	if len(record.Evidence) == 0 {
		return nil
	}
	if _, terminal := record.TerminalReceipt(); record.Lifecycle == nil || !terminal {
		return persistence.ErrWorkerExecutionInvalid
	}
	contract, err := c.workerOutcomeContract(ctx, workerOutcomeBinding(record))
	if err != nil {
		return err
	}
	for _, evidence := range record.Evidence {
		if err := c.verifyWorkerEvidenceRecord(ctx, contract, record, evidence); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *Catalog) verifyWorkerEvidenceRecord(ctx context.Context, contract api.JobType, record persistence.WorkerExecutionRecord, evidence persistence.WorkerEvidenceRecord) error {
	binding := workerEvidenceBinding(c.storeID, record, evidence.Input.EvidenceID)
	plain, err := c.sealer.Open(ctx, binding, evidence.Input.Payload)
	if err != nil {
		return err
	}
	defer clear(plain)
	var input api.LateEvidenceRequest
	if len(plain) > workerOutcomeBytes || api.StrictDecode(plain, &input) != nil || input.EvidenceID != evidence.Input.EvidenceID {
		return ErrWorkerEvidenceInvalid
	}
	defer clear(input.Data)
	raw, err := validateWorkerEvidence(ctx, contract, record, input)
	if err != nil {
		return err
	}
	defer clear(raw)
	if workerEvidenceDigest(binding, raw) != evidence.Input.Digest {
		return ErrWorkerEvidenceInvalid
	}
	return ctx.Err()
}
