//go:build externaljobs

package management

import (
	"context"
	"errors"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func workerOutcomeBinding(record persistence.WorkerExecutionRecord) WorkerOutcomeBinding {
	return WorkerOutcomeBinding{ServerID: record.Lifecycle.ServerID, WorkerUID: record.Lifecycle.WorkerUID,
		ExecutionID: record.Intent.ID, ExecutionRevision: record.Intent.Revision, GrantID: record.Lifecycle.GrantID, JobType: record.Intent.JobType}
}

// CommitWorkerOutcome derives the immutable contract from authenticated retained
// state, never from the caller's category, monitor or current configuration.
func (c *Catalog) CommitWorkerOutcome(ctx context.Context, authority persistence.WorkerAuthority, input api.Outcome) (persistence.WorkerReceipt, error) {
	if ctx == nil || input.WorkerUID == "" || !workerOutcomeText(input.WorkerUID, 256) {
		return persistence.WorkerReceipt{}, ErrWorkerOutcomeInvalid
	}
	if input.WorkerUID != authority.WorkerUID {
		return persistence.WorkerReceipt{}, persistence.ErrWorkerAuthorityDenied
	}
	if err := c.readyContext(ctx); err != nil {
		return persistence.WorkerReceipt{}, err
	}
	record, err := c.store.WorkerResultExecution(ctx, authority, input.ServerID, input.ExecutionID, input.GrantID)
	if err != nil {
		if errors.Is(err, persistence.ErrWorkerCheckExpired) && input.Kind != "check" {
			return persistence.WorkerReceipt{}, ErrWorkerOutcomeInvalid
		}
		return persistence.WorkerReceipt{}, err
	}
	prepared, err := c.PrepareWorkerOutcome(ctx, workerOutcomeBinding(record), input)
	if err != nil {
		return persistence.WorkerReceipt{}, err
	}
	return c.store.CommitWorkerResult(ctx, authority, persistence.WorkerResultInput{ServerID: record.Lifecycle.ServerID,
		ExecutionID: record.Intent.ID, ExecutionRevision: record.Intent.Revision, GrantID: record.Lifecycle.GrantID,
		Status: prepared.disposition.Status, Retryable: prepared.disposition.Retryable, Digest: prepared.digest, Payload: prepared.payload}, c.EvaluateWorkerMaintenance)
}

// verifyWorkerOutcome authenticates retained ciphertext even when its source or
// grant is no longer active. Replay and startup perform no provider operation.
func (c *Catalog) verifyWorkerOutcome(ctx context.Context, record persistence.WorkerExecutionRecord) error {
	if record.Result == nil {
		return nil
	}
	if record.Lifecycle == nil {
		return persistence.ErrWorkerExecutionInvalid
	}
	binding := workerOutcomeBinding(record)
	if err := binding.validate(); err != nil {
		return err
	}
	plain, err := c.sealer.Open(ctx, binding.encryptionBinding(c.storeID), record.Result.Input.Payload)
	if err != nil {
		return err
	}
	defer clear(plain)
	var input api.Outcome
	if len(plain) > workerOutcomeBytes || api.StrictDecode(plain, &input) != nil {
		return ErrWorkerOutcomeInvalid
	}
	defer clear(input.Data)
	contract, err := c.workerOutcomeContract(ctx, binding)
	if err != nil {
		return err
	}
	disposition, encoded, err := validateWorkerOutcome(ctx, contract, binding, input)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if workerOutcomeDigest(binding, encoded) != record.Result.Input.Digest || disposition.Status != record.Result.Input.Status || disposition.Retryable != record.Result.Input.Retryable {
		return ErrWorkerOutcomeInvalid
	}
	return ctx.Err()
}
