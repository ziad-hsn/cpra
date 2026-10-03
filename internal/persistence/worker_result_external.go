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

const WorkerResultFormatVersion = 22
const workerResultSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-22\n"

// Every admitted execution reserves one result slot through the execution-count
// limit. Result capacity is separate from the intent/lifecycle byte budget, so
// filling that namespace cannot prevent an already granted outcome from landing.
// The bound is encoded retention, not preallocated memory or a heap estimate.
const MaxWorkerResultRecordBytes = 256 << 10
const MaxWorkerResultStateBytes = MaxWorkerExecutions * MaxWorkerResultRecordBytes

type WorkerReceipt struct {
	ServerID    string `json:"server_id"`
	WorkerUID   string `json:"worker_uid"`
	ExecutionID string `json:"execution_id"`
	ReceiptID   string `json:"receipt_id"`
	Disposition string `json:"disposition"`
}

func workerResultReceiptID(serverID, grantID string, index uint64) string {
	return identity(fmt.Sprintf("cpra/worker/result-receipt/v1/%s/%s/%d", serverID, grantID, index))
}

// WorkerResultInput is prepared against the original immutable JobType. The
// persistence boundary independently verifies the authenticated grant owner.
type WorkerResultInput struct {
	ServerID          string                `json:"server_id"`
	ExecutionID       string                `json:"execution_id"`
	ExecutionRevision string                `json:"execution_revision"`
	GrantID           string                `json:"grant_id"`
	Status            string                `json:"status"`
	Retryable         bool                  `json:"retryable,omitempty"`
	Digest            string                `json:"digest"`
	Payload           secureconfig.Envelope `json:"payload"`
}

type WorkerResultRecord struct {
	Input          WorkerResultInput `json:"input"`
	Receipt        WorkerReceipt     `json:"receipt"`
	RecordedAt     time.Time         `json:"recorded_at"`
	CommittedIndex uint64            `json:"committed_index"`
	Applied        bool              `json:"applied"`
	NoData         bool              `json:"no_data,omitempty"`
}

func (r WorkerResultRecord) Clone() WorkerResultRecord {
	r.Input.Payload = r.Input.Payload.Clone()
	return r
}

type WorkerResultCommand struct {
	NodeID     string            `json:"node_id"`
	OwnerEpoch string            `json:"owner_epoch"`
	Authority  WorkerAuthority   `json:"authority"`
	Input      WorkerResultInput `json:"input"`
}

func (in WorkerResultInput) validate() error {
	for _, v := range []string{in.ServerID, in.ExecutionID, in.ExecutionRevision, in.GrantID} {
		if !catalogIdentifier(v, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if !bootstrapHash(in.Digest) || in.Payload.Validate() != nil || len(in.Payload.Ciphertext) > MaxWorkerExecutionPayload+16 ||
		(in.Retryable != (in.Status == "rejected")) || !workerResultStatus(in.Status, "") {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func workerResultStatus(status, category string) bool {
	switch status {
	case "success", "failure", "noData":
		return category == "" || category == "check"
	case "accepted", "rejected", "unknown":
		return category == "" || category == "recovery" || category == "notification"
	case "completed":
		return category == "" || category == "recovery"
	case "delivered":
		return category == "" || category == "notification"
	}
	return false
}

func (c WorkerResultCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.NodeID, 256) || !catalogIdentifier(c.OwnerEpoch, 256) || !c.Authority.sessionShapeValid() ||
		c.Input.validate() != nil || c.Input.ServerID != workerProtocolServerID(c.NodeID, c.Authority.Epoch) ||
		at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	// Leave room for the server-owned receipt in the execution's reserved slot.
	raw, err := json.Marshal(c.Input)
	if err != nil || len(raw) > MaxWorkerResultRecordBytes-(8<<10) {
		return ErrWorkerExecutionQuota
	}
	return nil
}

func workerResultRecordCost(r *WorkerResultRecord) (int64, error) {
	if r == nil {
		return 0, nil
	}
	if _, err := encodedBound(*r); err != nil {
		return 0, ErrWorkerExecutionQuota
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxWorkerResultRecordBytes {
		return 0, ErrWorkerExecutionQuota
	}
	return int64(len(raw)), nil
}

func (f *machine) workerResultExecution(a WorkerAuthority, serverID, executionID, grantID string, at time.Time) (WorkerExecutionRecord, error) {
	if err := f.checkWorkerCredential(a, at); err != nil {
		return WorkerExecutionRecord{}, err
	}
	if f.workerCheckExpired(executionID) {
		return WorkerExecutionRecord{}, ErrWorkerCheckExpired
	}
	if f.image.WorkerExecutions == nil {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	r, ok := f.image.WorkerExecutions.Records[executionID]
	l := r.Lifecycle
	if !ok || l == nil || l.ServerID != serverID || l.WorkerID != a.WorkerID || l.WorkerUID != a.WorkerUID || l.GrantID == "" || l.GrantID != grantID {
		return WorkerExecutionRecord{}, ErrWorkerExecutionConflict
	}
	return r, nil
}

// WorkerResultExecution authenticates ownership before result schema/key access.
// Expired sessions and removed new-work grants do not discard an original result.
func (s *Store) WorkerResultExecution(ctx context.Context, a WorkerAuthority, serverID, executionID, grantID string) (WorkerExecutionRecord, error) {
	for _, v := range []string{serverID, executionID, grantID} {
		if !catalogIdentifier(v, 256) {
			return WorkerExecutionRecord{}, ErrWorkerExecutionInvalid
		}
	}
	if serverID != workerProtocolServerID(s.nodeID, a.Epoch) {
		return WorkerExecutionRecord{}, ErrWorkerSessionIdentity
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return WorkerExecutionRecord{}, err
	}
	defer unlock()
	r, err := s.fsm.workerResultExecution(a, serverID, executionID, grantID, time.Now().UTC())
	if err != nil {
		return WorkerExecutionRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return WorkerExecutionRecord{}, err
	}
	return r.Clone(), nil
}

func (f *machine) applyWorkerResult(c WorkerResultCommand, at time.Time, index uint64, version int) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	old, err := f.workerResultExecution(c.Authority, c.Input.ServerID, c.Input.ExecutionID, c.Input.GrantID, at)
	if err != nil {
		return Result{Err: err}
	}
	if old.Intent.Revision != c.Input.ExecutionRevision || !workerResultStatus(c.Input.Status, old.Intent.Category) {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if err := validateWorkerExecutionLifecycle(f.image, old); err != nil {
		return Result{Err: err}
	}
	if old.Finalization != nil {
		receipt := old.Finalization.Receipt
		return Result{Allowed: true, resultExtensions: resultExtensions{WorkerResult: &receipt}}
	}
	if saved := old.Result; saved != nil {
		if saved.Input.Digest != c.Input.Digest || saved.Input.Status != c.Input.Status || saved.Input.Retryable != c.Input.Retryable {
			return Result{Err: ErrWorkerExecutionConflict}
		}
		receipt := saved.Receipt
		return Result{Allowed: true, resultExtensions: resultExtensions{WorkerResult: &receipt}}
	}
	if at.Before(old.Lifecycle.UpdatedAt) || (old.Lifecycle.Phase != "started" && old.Lifecycle.Phase != "unknown") {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	r := old.Clone()
	receipt := WorkerReceipt{ServerID: c.Input.ServerID, WorkerUID: c.Authority.WorkerUID, ExecutionID: c.Input.ExecutionID,
		ReceiptID: workerResultReceiptID(c.Input.ServerID, c.Input.GrantID, index), Disposition: "accepted"}
	monitor, events, applied, noData, held := f.workerResultTransition(r, c.Input, at, version)
	if noData {
		receipt.Disposition = "finalized"
	}
	if held {
		receipt.Disposition = "unknown"
	}
	r.Result = &WorkerResultRecord{Input: c.Input, Receipt: receipt, RecordedAt: at, CommittedIndex: index, Applied: applied, NoData: noData}
	r.Result.Input.Payload = c.Input.Payload.Clone()
	r.Lifecycle.UpdatedAt = at
	r.Lifecycle.Phase = "finished"
	if held {
		r.Lifecycle.Phase = "unknown"
	}
	resultCost, err := workerResultRecordCost(r.Result)
	if err != nil {
		return Result{Err: err}
	}
	if r.ResultTarget > 0 && resultCost > MaxWorkerResultRecordBytes-maxWorkerObservationBytes {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	oldCost, err := workerExecutionRecordCost(r.Intent.ID, old)
	if err != nil {
		return Result{Err: err}
	}
	newCost, err := workerExecutionRecordCost(r.Intent.ID, r)
	if err != nil {
		return Result{Err: err}
	}
	// Lifecycle accounting was reserved at offer admission; result bytes use the
	// execution's separate bounded slot and cannot consume another grant's slot.
	if newCost > oldCost {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	f.finishWorkerSLO(r, at)
	f.image.WorkerExecutions.replaceRecord(r)
	f.image.WorkerExecutions.EncodedBytes += newCost - oldCost
	f.image.Version = max(f.image.Version, WorkerResultFormatVersion)
	if notificationStorageFormat(version) {
		f.image.Version = joinStorageFormat(f.image.Version, version)
	}
	if monitor != nil {
		previous := f.image.Monitors[monitor.ID]
		events = append(events, f.installMonitor(previous, *monitor, at)...)
		f.publishWorkerMonitor(previous, *monitor)
	}
	return Result{Allowed: true, Monitor: monitor, Events: events, resultExtensions: resultExtensions{WorkerResult: &receipt}}
}

func (s *Store) CommitWorkerResult(ctx context.Context, a WorkerAuthority, input WorkerResultInput) (WorkerReceipt, error) {
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
	c := WorkerResultCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: input}
	at := time.Now().UTC()
	if err := c.validate(at); err != nil {
		return WorkerReceipt{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_result", At: at, commandExtensions: commandExtensions{WorkerResult: &c}}})
	confirmedAt := time.Now().UTC()
	if err != nil {
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 || !results[0].Allowed || results[0].WorkerResult == nil {
		if len(results) == 1 && results[0].Err != nil {
			return WorkerReceipt{}, results[0].Err
		}
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	record, err := s.WorkerResultExecution(ctx, a, input.ServerID, input.ExecutionID, input.GrantID)
	if err != nil {
		return WorkerReceipt{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if results[0].Monitor != nil && record.ResultTarget > 0 && record.Result != nil && record.Result.Applied && !record.Result.NoData {
		observation := WorkerObservationCommand{ExecutionID: input.ExecutionID, ExecutionRevision: input.ExecutionRevision,
			ReceiptID: record.Result.Receipt.ReceiptID, OwnerEpoch: s.executorSession}
		// This is measurement persistence, not another external outcome. A
		// failed write leaves a visible missing measurement. Never revoke the
		// known receipt or manufacture a replacement time on duplicate delivery.
		_, _ = s.Submit(ctx, []Command{{Kind: "worker_observation", At: confirmedAt, commandExtensions: commandExtensions{WorkerObservation: &observation}}})
	}
	return *results[0].WorkerResult, nil
}
