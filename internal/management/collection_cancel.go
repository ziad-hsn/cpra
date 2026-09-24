package management

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// CancelCollection retires input owned by actor, including validating uploads,
// validated/rejected results and privately admitted activation with no executed
// items. The original plan and verdict remain observations; cancellation never
// authorizes their execution. Partial item execution needs its own later
// cancellation contract and is not implemented by the admission foundation.
// A canceled receipt remains the original result after encrypted rows/header cleanup;
// retries never renew it, submit another command, or create another operation.
// Callers must authorize the request and recheck admission through admit.
func (c *Catalog) CancelCollection(ctx context.Context, id, actor string, now func() time.Time, admit CollectionCommit) (api.Operation, error) {
	if ctx == nil || now == nil || admit == nil || actor == "" {
		return api.Operation{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	// The protected read below checks storage health with the caller's context.
	// Ready also takes owner locks and would make this preliminary check
	// uncancelable while storage is busy.
	if !c.verified.Load() || c.failed.Load() {
		return api.Operation{}, ErrUnavailable
	}
	// Serialize same-owner HTTP cancellation before obtaining an authorization
	// grant. Waiting never holds the policy lock or performs crypto/provider I/O.
	select {
	case c.collectionCancel <- struct{}{}:
		defer func() { <-c.collectionCancel }()
	case <-ctx.Done():
		return api.Operation{}, ctx.Err()
	}
	read := func() (persistence.CollectionReceipt, error) {
		r, err := c.store.CollectionOperationAs(ctx, id, actor, now())
		if err != nil {
			return persistence.CollectionReceipt{}, err
		}
		if r.Actor != actor {
			return persistence.CollectionReceipt{}, persistence.ErrOperationNotFound
		}
		switch r.Phase {
		case "uploading", "validating", "validated", "rejected", "applying", "canceled":
			return r, nil
		case "expired", "invalidated":
			return persistence.CollectionReceipt{}, persistence.ErrOperationExpired
		default:
			return persistence.CollectionReceipt{}, persistence.ErrCollectionConflict
		}
	}
	receipt, err := read()
	if err != nil {
		return api.Operation{}, err
	}
	if receipt.Phase == "canceled" {
		return collectionOperationView(receipt), nil
	}
	var results []persistence.Result
	err = admit(func() error {
		var err error
		receipt, err = read()
		if err != nil || receipt.Phase == "canceled" {
			return err
		}
		at := now().UTC()
		command := persistence.CollectionCommand{
			Action: "cancel", OperationID: id, UploadID: receipt.UploadID,
			Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: actor, At: at}}
		if receipt.Activation != nil {
			// A retained admission is not execution authority. Observe current
			// durable policy and let Apply compare it again with the owner and
			// original activation identity at this actual write boundary.
			authority, authorityErr := c.store.ObserveOperatorAuthority(ctx, actor, at)
			if authorityErr != nil {
				return authorityErr
			}
			command.ActivationAuthority = &authority
			command.ActivationFence = &persistence.CollectionActivationFence{ID: receipt.Activation.ID,
				ResultID: receipt.Activation.ResultID, PlanID: receipt.Activation.PlanID}
		}
		at = now().UTC()
		command.Cancel.At = at
		results, err = c.store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at, Collection: &command}})
		if err != nil {
			return errors.Join(ErrOutcomeUnconfirmed, err)
		}
		return nil
	})
	if err != nil {
		return api.Operation{ID: id}, err
	}
	if results != nil {
		if len(results) != 1 {
			return api.Operation{ID: id}, ErrOutcomeUnconfirmed
		}
		if resultErr := results[0].Err; resultErr != nil {
			// Another Catalog owner can win after our observation. Reconcile that
			// authoritative receipt; never resubmit with a replacement identity.
			if !errors.Is(resultErr, persistence.ErrCollectionConflict) && !errors.Is(resultErr, persistence.ErrOperationExpired) {
				return api.Operation{ID: id}, resultErr
			}
			winner, readErr := read()
			if readErr == nil && winner.Phase == "canceled" {
				return collectionOperationView(winner), nil
			}
			if readErr != nil {
				return api.Operation{ID: id}, readErr
			}
			return api.Operation{ID: id}, resultErr
		}
	}
	receipt, err = read()
	if err != nil {
		return api.Operation{ID: id}, err
	}
	if receipt.Phase != "canceled" {
		return api.Operation{ID: id}, ErrOutcomeUnconfirmed
	}
	return collectionOperationView(receipt), nil
}
