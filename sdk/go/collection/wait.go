package collection

import (
	"context"
	"errors"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// ExecutionOperations exposes only the read needed to await retained results.
type ExecutionOperations interface {
	ExecutionResult(context.Context, string, cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error)
}

// Wait waits for the original collection's retained execution result. Apply
// itself returns on activation admission. The returned Result keeps the last
// verified observation, including partial counts, when waiting fails or stops.
// A ready partial or failed result is returned without a transport error; its
// immutable summary describes the execution outcome. Only one first page is
// returned. The Frozen can be closed before Wait, and context cancellation never
// cancels or resubmits server work.
func Wait(ctx context.Context, operations ExecutionOperations, result Result) (Result, error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.Noop {
		return result, nil
	}
	original := result.Operation
	if operations == nil || result.OperationID == "" || original.ID != result.OperationID || original.IdentityFormat != commitment.Format || original.ContentDigest == "" || original.ItemCount == nil || *original.ItemCount < 1 {
		return result, errors.New("original collection operation identity is required")
	}
	count := *original.ItemCount
	for {
		r, err := operations.ExecutionResult(ctx, result.OperationID, cpra.ExecutionResultPageOptions{})
		if err != nil {
			return result, err
		}
		if r == nil || r.Data.ID != result.OperationID || r.OperationID != "" && r.OperationID != result.OperationID || r.Data.IdentityFormat != original.IdentityFormat || r.Data.NormalizationProfile != original.NormalizationProfile || r.Data.ContentDigest != original.ContentDigest || r.Data.ItemCount == nil || *r.Data.ItemCount != count {
			return result, errors.New("execution result collection identity mismatch")
		}
		if err := api.ValidateExecutionResult(r.Data); err != nil {
			return result, err
		}
		if len(r.Data.Items) > 100 || len(r.Data.Items) > 0 && (r.Data.Items[0].InputOrdinal == nil || *r.Data.Items[0].InputOrdinal != 1) || r.Data.RetryAfterSeconds < 0 || r.Data.RetryAfterSeconds > 86400 {
			return result, errors.New("invalid execution first page")
		}
		result.Operation = r.Data
		if r.Data.ExecutionResult == nil {
			return result, cpra.ErrExecutionResultUnsupported
		}
		switch r.Data.ExecutionResult.State {
		case "ready":
			return result, nil
		case "expired":
			return result, cpra.ErrExecutionResultExpired
		case "pending":
		default:
			return result, cpra.ErrExecutionResultUnsupported
		}
		delay := max(5*time.Second, r.RetryAfter, time.Duration(r.Data.RetryAfterSeconds)*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}
