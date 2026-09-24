package management

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ActivateCollection admits the original successful collection validation for
// background execution. It never decrypts input, recompiles a plan, refreshes a
// resource version guard or applies a resource itself. The authenticated caller
// authorizes the complete resource-kind permission set before this lookup and
// rechecks its principal and process admission through admit at the write.
func (c *Catalog) ActivateCollection(ctx context.Context, id, actor string, now func() time.Time, admit CollectionCommit) (api.Operation, error) {
	if ctx == nil || now == nil || admit == nil || actor == "" {
		return api.Operation{}, ErrValidation
	}
	if err := c.readyContext(ctx); err != nil {
		return api.Operation{}, err
	}
	if c.validationStopping.Load() {
		return api.Operation{}, ErrUnavailable
	}
	read := func() (persistence.CollectionReceipt, error) {
		return c.store.CollectionOperationAs(ctx, id, actor, now())
	}
	authorized := false
	observeAuthority := func() (persistence.OperatorAuthority, error) {
		if err := c.readyContext(ctx); err != nil {
			return persistence.OperatorAuthority{}, err
		}
		if c.validationStopping.Load() {
			return persistence.OperatorAuthority{}, ErrUnavailable
		}
		return c.store.ObserveOperatorAuthority(ctx, actor, now())
	}
	observed := func(r persistence.CollectionReceipt) (api.Operation, error) {
		if !authorized {
			// Even a read-only retry crosses the current principal/shutdown
			// gate. The protected history read already completed outside it.
			if err := admit(func() error {
				_, err := observeAuthority()
				return err
			}); err != nil {
				return api.Operation{ID: id}, err
			}
			// Waiting for admission can cross retention or recovery. Re-read
			// outside the caller's policy lock so the returned observation is
			// fenced by the current owner, epoch, health and history cutoff.
			var err error
			r, err = read()
			if err != nil {
				return api.Operation{ID: id}, err
			}
			if !collectionActivationObserved(r) {
				return api.Operation{ID: id}, ErrOutcomeUnconfirmed
			}
		}
		return collectionOperationView(r), nil
	}
	receipt, err := read()
	if err != nil {
		return api.Operation{ID: id}, err
	}
	if collectionActivationObserved(receipt) {
		return observed(receipt)
	}
	if receipt.Phase == "expired" || receipt.Phase == "invalidated" {
		return api.Operation{ID: id}, persistence.ErrOperationExpired
	}
	if receipt.Phase != "validated" || receipt.Validation == nil || !receipt.Validation.Header.Valid || receipt.Validation.Header.SummaryOnly {
		return api.Operation{ID: id}, persistence.ErrCollectionConflict
	}
	_, profile, err := collectionValidationProfile()
	if err != nil {
		return api.Operation{ID: id}, err
	}
	if profile != receipt.Validation.Header.CapabilitiesDigest {
		return api.Operation{ID: id}, persistence.ErrCollectionConflict
	}
	// A competing admission can end inactive artifact access while verification
	// runs. Reconcile its original durable identity; never retry a new command.
	reconcile := func(original error) (api.Operation, error) {
		winner, readErr := read()
		if readErr == nil && collectionActivationObserved(winner) {
			return observed(winner)
		}
		return api.Operation{ID: id}, original
	}
	proof, err := c.store.VerifyCollectionPlan(ctx, id, now())
	if err != nil {
		return reconcile(err)
	}
	v := receipt.Validation
	if proof.OperationID != id || proof.UploadID != receipt.UploadID || proof.InputCount != receipt.ItemCount ||
		proof.InputProgressDigest != v.Header.InputProgressDigest || proof.PlanID != v.Header.PlanID || proof.Descriptor.Digest != v.Header.PlanDigest {
		return api.Operation{ID: id}, persistence.ErrCollectionConflict
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), InputProgressDigest: proof.InputProgressDigest,
		ItemCount: proof.InputCount, ResultID: v.Header.ResultID, ResultDescriptor: v.Descriptor,
		PlanID: proof.PlanID, PlanDescriptor: proof.Descriptor, CapabilitiesDigest: profile,
		ValidationRequest: persistence.CollectionValidationRequestFenceFor(persistence.CollectionState{ValidationRequest: receipt.ValidationRequest})}
	var results []persistence.Result
	attempted := false
	err = admit(func() error {
		authority, err := observeAuthority()
		if err != nil {
			return err
		}
		authorized = true
		at := now().UTC()
		activation.Authority, activation.At = authority, at
		command := persistence.CollectionCommand{Action: "activation_admit", OperationID: id, UploadID: receipt.UploadID,
			Activation: &activation, ActivationAuthority: &authority}
		attempted = true
		results, err = c.store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at, Collection: &command}})
		if err != nil {
			return errors.Join(ErrOutcomeUnconfirmed, err)
		}
		return nil
	})
	if err != nil {
		if attempted {
			return reconcile(errors.Join(ErrOutcomeUnconfirmed, err))
		}
		return api.Operation{ID: id}, err
	}
	if len(results) != 1 || !results[0].Allowed && results[0].Err == nil {
		return reconcile(ErrOutcomeUnconfirmed)
	}
	if resultErr := results[0].Err; resultErr != nil {
		if errors.Is(resultErr, persistence.ErrCollectionConflict) || errors.Is(resultErr, persistence.ErrOperationExpired) {
			return reconcile(resultErr)
		}
		return api.Operation{ID: id}, resultErr
	}
	// Read the original execution observation, including any progress already
	// committed by the coordinator. A raw admission receipt has no item counts.
	receipt, err = read()
	if err != nil {
		return api.Operation{ID: id}, errors.Join(ErrOutcomeUnconfirmed, err)
	}
	if !collectionActivationObserved(receipt) {
		return api.Operation{ID: id}, ErrOutcomeUnconfirmed
	}
	return collectionOperationView(receipt), nil
}

func collectionActivationObserved(r persistence.CollectionReceipt) bool {
	return r.Activation != nil || r.Execution != nil || r.ExecutionObservation != nil && r.ExecutionObservation.Summary != nil
}
