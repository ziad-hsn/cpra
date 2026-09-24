package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func operationView(receipt persistence.OperationReceipt) api.Operation {
	// This is a resource-receipt identity digest, not a digest of collection
	// input or plaintext configuration. It cannot be used as a secret oracle.
	identity := []string{"cpra-resource-receipt-v1", receipt.ID, receipt.Key.Kind, receipt.Key.ID, receipt.UID, receipt.NewVersion}
	if receipt.Subject != "" {
		identity = append(identity, receipt.Subject, receipt.IncidentID)
		if receipt.ActionID != "" {
			identity = append(identity, receipt.ActionID)
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(identity, "\x00")))
	applied := receipt.State == "completed"
	committed := receipt.CommittedIndex != 0
	item := api.ApplyResult{ID: receipt.Key.ID, OldVersion: receipt.OldVersion, NewVersion: receipt.NewVersion,
		Committed: api.Pointer(committed), Applied: api.Pointer(applied), Outcome: receipt.Outcome}
	operation := api.Operation{ID: receipt.ID, ContentDigest: hex.EncodeToString(digest[:]), State: receipt.State, Validated: api.Pointer(committed), Committed: api.Pointer(int64(0)), Applied: api.Pointer(int64(0)), Items: []api.ApplyResult{item}}
	if committed {
		*operation.Committed = 1
	}
	if applied {
		*operation.Applied = 1
	}
	if receipt.State == "committed" || receipt.State == "reserved" {
		operation.RetryAfterSeconds = 5
	}
	return operation
}

// Operation exposes shared ordinary resource receipts only. Collection reads
// require OperationAs with the authenticated actor and current read permission.
func (c *Catalog) Operation(ctx context.Context, id string) (api.Operation, error) {
	if ctx == nil {
		return api.Operation{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	if !c.Ready() {
		return api.Operation{}, ErrUnavailable
	}
	if !validID(id) {
		return api.Operation{}, persistence.ErrOperationNotFound
	}
	receipt, err := c.store.OperationContext(ctx, id, time.Now().UTC())
	if err != nil {
		return api.Operation{}, err
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	return operationView(receipt), nil
}

// CompleteOperation records an owner-loop reconciliation outcome. The caller
// must have installed or removed the exact committed incarnation and revision.
// This is an internal completion channel, never a public management operation.
func (c *Catalog) CompleteOperation(ctx context.Context, id string, applied bool) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt, err := c.store.OperationContext(ctx, id, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if receipt.State != "committed" {
		if (applied && receipt.State == "completed") || (!applied && receipt.State == "failed") {
			return nil
		}
		return persistence.ErrCatalogConflict
	}
	result, err := c.store.Submit(ctx, []persistence.Command{{Kind: "operation", At: time.Now().UTC(), Operation: &persistence.OperationUpdate{
		ID: id, Key: receipt.Key, UID: receipt.UID, Revision: receipt.NewVersion, Subject: receipt.Subject, IncidentID: receipt.IncidentID, ActionID: receipt.ActionID, Applied: applied}}})
	if err != nil {
		return err
	}
	if len(result) != 1 {
		return ErrOutcomeUnconfirmed
	}
	if errors.Is(result[0].Err, persistence.ErrOperationNotFound) {
		current, err := c.store.OperationContext(ctx, id, time.Now().UTC())
		if err != nil {
			return err
		}
		if (applied && current.State == "completed") || (!applied && current.State == "failed") {
			return nil
		}
	}
	return result[0].Err
}
