//go:build externaljobs

package management

import (
	"context"
	"encoding/json"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const maxWorkerAssignmentResponseBytes = 4 << 20

// WorkerAssignments opens only the exact offers in a committed poll receipt.
// It rechecks authority after decryption and never returns a partial page.
// Assignment data is not permission to invoke a handler.
func (c *Catalog) WorkerAssignments(ctx context.Context, authority persistence.WorkerAuthority, response persistence.WorkerPollResponse) (api.Assignments, error) {
	var empty api.Assignments
	if err := c.readyContext(ctx); err != nil {
		return empty, err
	}
	if err := c.store.VerifyWorkerPollResponse(ctx, authority, response); err != nil {
		return empty, err
	}
	out := api.Assignments{ServerID: response.ServerID, WorkerUID: response.WorkerUID,
		ClientSessionID: response.ClientSessionID, SessionID: response.SessionID,
		PollSequence: int64(response.PollSequence), SessionExpiresAt: response.SessionExpiresAt,
		Items: make([]api.Assignment, 0, len(response.Offers))}
	succeeded := false
	defer func() {
		if !succeeded {
			for i := range out.Items {
				clear(out.Items[i].Parameters)
			}
		}
	}()
	for _, offer := range response.Offers {
		record, ok, err := c.store.WorkerExecution(ctx, offer.ExecutionID)
		if err != nil {
			return empty, workerPreparationError(ctx, err)
		}
		if !ok || record.Intent.Revision != offer.ExecutionRevision || !record.Intent.Deadline.Equal(offer.Deadline) {
			return empty, persistence.ErrWorkerExecutionConflict
		}
		descriptor, err := c.OpenWorkerExecution(ctx, record)
		if err != nil {
			return empty, err
		}
		in := record.Intent
		out.Items = append(out.Items, api.Assignment{ServerID: response.ServerID, WorkerUID: response.WorkerUID,
			SessionID: response.SessionID, ExecutionID: in.ID, ExecutionRevision: in.Revision,
			LeaseID: offer.LeaseID, Deadline: in.Deadline, MonitorID: in.MonitorID, IncarnationUID: in.MonitorUID,
			JobTypeID: in.JobType.JobTypeID, JobTypeUID: in.JobType.JobTypeUID, JobTypeVersion: in.JobType.Version,
			Kind: in.Category, Parameters: descriptor.Parameters(), CredentialProfile: descriptor.CredentialProfile()})
	}
	encoded, err := json.Marshal(out)
	encodedBytes := len(encoded)
	clear(encoded)
	if err != nil || encodedBytes > maxWorkerAssignmentResponseBytes {
		return empty, persistence.ErrWorkerExecutionQuota
	}
	// Crypto and schema work runs outside the FSM lock. A revocation, expired
	// offer or changed target while it runs must discard the complete page.
	if err := c.store.VerifyWorkerPollResponse(ctx, authority, response); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	succeeded = true
	return out, nil
}
