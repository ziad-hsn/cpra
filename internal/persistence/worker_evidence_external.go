//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const MaxWorkerEvidencePerExecution = 8
const MaxWorkerEvidenceBytes = 64 << 20
const maxWorkerEvidenceRecordBytes = 256 << 10

type WorkerEvidenceInput struct {
	ServerID          string                `json:"server_id"`
	ExecutionID       string                `json:"execution_id"`
	ExecutionRevision string                `json:"execution_revision"`
	GrantID           string                `json:"grant_id"`
	OriginalReceiptID string                `json:"original_receipt_id"`
	EvidenceID        string                `json:"evidence_id"`
	Digest            string                `json:"digest"`
	Payload           secureconfig.Envelope `json:"payload"`
}

type WorkerEvidenceRecord struct {
	Input          WorkerEvidenceInput `json:"input"`
	Receipt        WorkerReceipt       `json:"receipt"`
	RecordedAt     time.Time           `json:"recorded_at"`
	CommittedIndex uint64              `json:"committed_index"`
}

func (r WorkerEvidenceRecord) Clone() WorkerEvidenceRecord {
	r.Input.Payload = r.Input.Payload.Clone()
	return r
}

type WorkerEvidenceCommand struct {
	NodeID     string              `json:"node_id"`
	OwnerEpoch string              `json:"owner_epoch"`
	Authority  WorkerAuthority     `json:"authority"`
	Input      WorkerEvidenceInput `json:"input"`
}

func (in WorkerEvidenceInput) validate() error {
	for _, id := range []string{in.ServerID, in.ExecutionID, in.ExecutionRevision, in.GrantID, in.OriginalReceiptID, in.EvidenceID} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if !bootstrapHash(in.Digest) || in.Payload.Validate() != nil || len(in.Payload.Ciphertext) > MaxWorkerExecutionPayload+16 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func (c WorkerEvidenceCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.NodeID, 256) || !catalogIdentifier(c.OwnerEpoch, 256) || !c.Authority.sessionShapeValid() ||
		c.Input.validate() != nil || c.Input.ServerID != workerProtocolServerID(c.NodeID, c.Authority.Epoch) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	raw, err := json.Marshal(c.Input)
	if err != nil || len(raw) > maxWorkerEvidenceRecordBytes-(8<<10) {
		return ErrWorkerExecutionQuota
	}
	return nil
}

func workerEvidenceReceiptID(original, evidenceID string, index uint64) string {
	return identity(fmt.Sprintf("cpra/worker/evidence-receipt/v1/%s/%s/%d", original, evidenceID, index))
}

func workerEvidenceRecordCost(r WorkerEvidenceRecord) (int64, error) {
	if _, err := encodedBound(r); err != nil {
		return 0, ErrWorkerExecutionQuota
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > maxWorkerEvidenceRecordBytes {
		return 0, ErrWorkerExecutionQuota
	}
	// Reserve separators as well as encoded record bytes.
	return int64(len(raw) + 2), nil
}

func workerEvidenceEligible(r WorkerExecutionRecord) bool {
	receipt, terminal := r.TerminalReceipt()
	return terminal && r.Lifecycle != nil && r.Lifecycle.GrantID != "" &&
		(receipt.Disposition == "unknown" || r.Intent.Category == "check" && receipt.Disposition == "finalized")
}

func (f *machine) workerEvidenceExecution(a WorkerAuthority, serverID, executionID, originalReceiptID string, at time.Time) (WorkerExecutionRecord, error) {
	if err := f.checkWorkerCredential(a, at); err != nil {
		return WorkerExecutionRecord{}, err
	}
	if f.image.WorkerExecutions == nil {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	r, ok := f.image.WorkerExecutions.Records[executionID]
	l := r.Lifecycle
	receipt, _ := r.TerminalReceipt()
	if !ok || l == nil || l.ServerID != serverID || l.WorkerID != a.WorkerID || l.WorkerUID != a.WorkerUID ||
		!workerEvidenceEligible(r) || receipt.ReceiptID != originalReceiptID {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	if r.Intent.Category != "check" {
		action, ok, err := f.actionForMonitor(f.image.Monitors[r.Intent.MonitorID], r.Intent.ActionID)
		if err != nil {
			return WorkerExecutionRecord{}, err
		}
		if !ok || action.CatalogUID != r.Intent.MonitorUID || action.Revision != r.Intent.MonitorRevision || action.ExecutorKind != "worker" || action.ExecutorSession != l.GrantID {
			return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
		}
	}
	return r, nil
}

// WorkerEvidenceExecution authenticates the original receipt before reading its
// contract or decrypting anything. Current new-work grants and sessions may end
// without discarding delayed evidence from the same authorized worker identity.
func (s *Store) WorkerEvidenceExecution(ctx context.Context, a WorkerAuthority, serverID, executionID, originalReceiptID string) (WorkerExecutionRecord, error) {
	for _, id := range []string{serverID, executionID, originalReceiptID} {
		if !catalogIdentifier(id, 256) {
			return WorkerExecutionRecord{}, ErrWorkerExecutionInvalid
		}
	}
	if serverID != workerProtocolServerID(s.nodeID, a.Epoch) {
		return WorkerExecutionRecord{}, ErrWorkerSessionIdentity
	}
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return WorkerExecutionRecord{}, err
	}
	defer unlock()
	r, err := s.fsm.workerEvidenceExecution(a, serverID, executionID, originalReceiptID, time.Now().UTC())
	if err != nil {
		return WorkerExecutionRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return WorkerExecutionRecord{}, err
	}
	return r.Clone(), nil
}

func (f *machine) applyWorkerEvidence(c WorkerEvidenceCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	old, err := f.workerEvidenceExecution(c.Authority, c.Input.ServerID, c.Input.ExecutionID, c.Input.OriginalReceiptID, at)
	if err != nil {
		return Result{Err: err}
	}
	if old.Intent.Revision != c.Input.ExecutionRevision || old.Lifecycle.GrantID != c.Input.GrantID {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	for _, prior := range old.Evidence {
		if prior.Input.EvidenceID == c.Input.EvidenceID {
			if prior.Input.Digest != c.Input.Digest {
				return Result{Err: ErrWorkerExecutionConflict}
			}
			receipt := prior.Receipt
			return Result{Allowed: true, resultExtensions: resultExtensions{WorkerEvidence: &receipt}}
		}
	}
	terminalAt, _ := old.terminalPosition()
	if at.Before(terminalAt) || len(old.Evidence) > 0 && at.Before(old.Evidence[len(old.Evidence)-1].RecordedAt) {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if len(old.Evidence) >= MaxWorkerEvidencePerExecution {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	receipt := WorkerReceipt{ServerID: c.Input.ServerID, WorkerUID: c.Authority.WorkerUID, ExecutionID: c.Input.ExecutionID,
		ReceiptID: workerEvidenceReceiptID(c.Input.OriginalReceiptID, c.Input.EvidenceID, index), Disposition: "accepted"}
	evidence := WorkerEvidenceRecord{Input: c.Input, Receipt: receipt, RecordedAt: at, CommittedIndex: index}
	evidence.Input.Payload = c.Input.Payload.Clone()
	cost, err := workerEvidenceRecordCost(evidence)
	state := f.image.WorkerExecutions
	if err != nil || cost > MaxWorkerEvidenceBytes-state.EvidenceBytes {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	r := old.Clone()
	r.Evidence = append(r.Evidence, evidence)
	state.Records[r.Intent.ID] = r
	state.EvidenceBytes += cost
	f.image.Version = max(f.image.Version, WorkerLifecycleFormatVersion)
	var events []Event
	if r.Intent.Category != "check" {
		m := f.image.Monitors[r.Intent.MonitorID]
		a, _, err := f.actionForMonitor(m, r.Intent.ActionID)
		if err != nil {
			return Result{Err: err}
		}
		events = []Event{{MonitorID: r.Intent.MonitorID, CatalogUID: r.Intent.MonitorUID, Revision: r.Intent.MonitorRevision, At: at,
			Type: "worker_late_evidence", IncidentID: a.IncidentID, ActionID: r.Intent.ActionID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint,
			Actor: c.Authority.WorkerID, Outcome: "recorded", EvidenceRefs: []string{receipt.ReceiptID}}}
	}
	// Evidence is supplemental, not a replacement outcome or permission to retry.
	// Never infer a disposition or executor fencing from opaque handler data.
	return Result{Allowed: true, Events: events, resultExtensions: resultExtensions{WorkerEvidence: &receipt}}
}

func (s *Store) CommitWorkerEvidence(ctx context.Context, a WorkerAuthority, input WorkerEvidenceInput) (WorkerReceipt, error) {
	if ctx == nil {
		return WorkerReceipt{}, ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return WorkerReceipt{}, err
	}
	if s.administrative {
		return WorkerReceipt{}, ErrWorkerSessionUnavailable
	}
	input.Payload = input.Payload.Clone()
	c := WorkerEvidenceCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: input}
	at := time.Now().UTC()
	if err := c.validate(at); err != nil {
		return WorkerReceipt{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_evidence", At: at, commandExtensions: commandExtensions{WorkerEvidence: &c}}})
	if err != nil {
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 || !results[0].Allowed || results[0].WorkerEvidence == nil {
		if len(results) == 1 && results[0].Err != nil {
			return WorkerReceipt{}, results[0].Err
		}
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	if _, err := s.WorkerEvidenceExecution(ctx, a, input.ServerID, input.ExecutionID, input.OriginalReceiptID); err != nil {
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	return *results[0].WorkerEvidence, nil
}

func validateWorkerEvidence(i image, r WorkerExecutionRecord) (int64, error) {
	if len(r.Evidence) == 0 {
		return 0, nil
	}
	if i.Version < WorkerLifecycleFormatVersion || r.Lifecycle == nil || !workerEvidenceEligible(r) || len(r.Evidence) > MaxWorkerEvidencePerExecution {
		return 0, ErrWorkerExecutionInvalid
	}
	seen := make(map[string]bool, len(r.Evidence))
	at, index := r.terminalPosition()
	receipt, _ := r.TerminalReceipt()
	var used int64
	for _, v := range r.Evidence {
		in := v.Input
		if in.validate() != nil || in.ServerID != r.Lifecycle.ServerID || in.ExecutionID != r.Intent.ID || in.ExecutionRevision != r.Intent.Revision ||
			in.GrantID != r.Lifecycle.GrantID || in.OriginalReceiptID != receipt.ReceiptID || seen[in.EvidenceID] ||
			v.RecordedAt.Before(at) || v.RecordedAt.Year() > 9998 || v.CommittedIndex < index || v.CommittedIndex > i.Index ||
			v.Receipt != (WorkerReceipt{ServerID: in.ServerID, WorkerUID: r.Lifecycle.WorkerUID, ExecutionID: in.ExecutionID,
				ReceiptID: workerEvidenceReceiptID(in.OriginalReceiptID, in.EvidenceID, v.CommittedIndex), Disposition: "accepted"}) {
			return 0, ErrWorkerExecutionInvalid
		}
		cost, err := workerEvidenceRecordCost(v)
		if err != nil {
			return 0, err
		}
		used += cost
		seen[in.EvidenceID] = true
		at, index = v.RecordedAt, v.CommittedIndex
	}
	return used, nil
}
