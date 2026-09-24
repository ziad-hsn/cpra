package management

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// RequestCollectionValidation durably admits one validation request for the
// original input. The caller authenticates the owner and rechecks HTTP admission
// through admit; the background runner retains neither callback nor credentials.
// Acceptance promises queued work, not the catalog observation at this instant.
func (c *Catalog) RequestCollectionValidation(ctx context.Context, id, actor string, now func() time.Time, admit CollectionCommit) (api.Operation, error) {
	if ctx == nil || now == nil || admit == nil || actor == "" {
		return api.Operation{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	if !c.Ready() || c.validationStopping.Load() {
		return api.Operation{}, ErrUnavailable
	}
	var operation api.Operation
	err := admit(func() error {
		if c.validationStopping.Load() {
			return ErrUnavailable
		}
		reconcile := func(original error) error {
			// A competing request or an uncertain submit can already have fixed
			// the one original request. Observe it without issuing another write.
			receipt, err := c.store.CollectionReceipt(ctx, id, now())
			if err == nil && receipt.Actor == actor && receipt.ValidationRequest != nil {
				operation = collectionOperationView(receipt)
				return nil
			}
			return original
		}
		receipt, err := c.store.CollectionReceipt(ctx, id, now())
		if err != nil {
			return err
		}
		if receipt.Actor != actor {
			return persistence.ErrOperationNotFound
		}
		if receipt.ValidationRequest != nil {
			operation = collectionOperationView(receipt)
			return nil // Reconcile the original request; never renew or recompile.
		}
		if receipt.Phase != "uploading" {
			return persistence.ErrCollectionConflict
		}
		_, head, err := c.store.CollectionValidationView(ctx, id, now())
		if err != nil {
			return reconcile(err)
		}
		authority, err := c.store.ObserveOperatorAuthority(ctx, actor, now())
		if err != nil {
			return err
		}
		_, profile, err := collectionValidationProfile()
		if err != nil {
			return err
		}
		at := now().UTC()
		request := persistence.CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest,
			ItemCount: head.ItemCount, Authority: authority, CapabilitiesDigest: profile, RequestedAt: at}
		_, err = c.submitCollectionValidation(ctx, persistence.CollectionCommand{Action: "validation_request", OperationID: id,
			UploadID: head.UploadID, ValidationRequest: &request}, at)
		if err != nil {
			return reconcile(err)
		}
		receipt, err = c.store.CollectionReceipt(ctx, id, now())
		if err != nil {
			return err
		}
		operation = collectionOperationView(receipt)
		return nil
	})
	if err != nil {
		return api.Operation{ID: id}, err
	}
	select {
	case c.validationWake <- struct{}{}:
	default:
	}
	return operation, nil
}

func (c *Catalog) submitCollectionValidation(ctx context.Context, command persistence.CollectionCommand, at time.Time) (persistence.CollectionState, error) {
	if err := ctx.Err(); err != nil {
		return persistence.CollectionState{}, err
	}
	if err := c.collectionValidationHealth(ctx, at); err != nil {
		return persistence.CollectionState{}, err
	}
	results, err := c.store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at.UTC(), Collection: &command}})
	if err != nil {
		return persistence.CollectionState{}, errors.Join(ErrOutcomeUnconfirmed, err)
	}
	if len(results) != 1 {
		return persistence.CollectionState{}, ErrOutcomeUnconfirmed
	}
	if results[0].Err != nil {
		return persistence.CollectionState{}, results[0].Err
	}
	if !results[0].Allowed || results[0].Collection == nil {
		return persistence.CollectionState{}, ErrOutcomeUnconfirmed
	}
	return results[0].Collection.Clone(), nil
}

// runCollectionValidation is one claimed attempt, owned by the bounded
// lifecycle coordinator. It never takes over another claim or recompiles after
// any claim already exists. Successful return means the immutable verdict was
// committed; retained-history publication may still be pending.
func (c *Catalog) runCollectionValidation(ctx context.Context, id, runID string, now func() time.Time) (persistence.CollectionState, error) {
	if ctx == nil || now == nil {
		return persistence.CollectionState{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return persistence.CollectionState{}, err
	}
	if err := c.collectionValidationHealth(ctx, now()); err != nil {
		return persistence.CollectionState{}, err
	}
	receipt, err := c.store.CollectionReceipt(ctx, id, now())
	if err != nil {
		return persistence.CollectionState{}, err
	}
	request := receipt.ValidationRequest
	if receipt.Phase != "validating" || request == nil || request.Claim != nil || request.Interruption != nil {
		return persistence.CollectionState{}, persistence.ErrCollectionConflict
	}
	_, profile, err := collectionValidationProfile()
	if err != nil {
		return persistence.CollectionState{}, err
	}
	if request.CapabilitiesDigest != profile {
		return persistence.CollectionState{}, persistence.ErrCollectionConflict
	}
	at := now().UTC()
	claim := persistence.CollectionValidationClaim{ID: uuid.NewString(), RunID: runID, At: at}
	state, err := c.submitCollectionValidation(ctx, persistence.CollectionCommand{Action: "validation_claim", OperationID: id,
		UploadID: receipt.UploadID, ValidationFence: &persistence.CollectionValidationRequestFence{RequestID: request.ID}, ValidationClaim: &claim}, at)
	if err != nil {
		return persistence.CollectionState{}, err
	}
	fence := persistence.CollectionValidationRequestFenceFor(state)
	// Each lookup rechecks durable authority. Today's operator role has access to
	// these five resource kinds; this callback adds no unsupported resource kind.
	canRead := func(key persistence.CatalogKey) bool { return supportedKind(key.Kind) }
	source, err := newCollectionValidationSourceForAttempt(ctx, c, id, fence, canRead, now)
	if err != nil {
		return state, err
	}
	defer source.close()
	view, err := c.Snapshot()
	if err != nil {
		return state, err
	}
	validation, plan, validationErr := c.compileStagedCollectionPlan(ctx, view, source, CollectionValidationOptions{CanRead: canRead})
	result, err := prepareCollectionValidationArtifact(ctx, source, validation, validationErr)
	if err != nil {
		return state, err
	}
	defer result.close()
	var artifact *collectionPlanArtifact
	if validation.Valid {
		artifact, err = prepareCollectionPlanArtifact(ctx, plan, uuid.NewString())
		if err != nil {
			return state, err
		}
	}
	// Both descriptors are frozen before plan_begin ends compilation access.
	source.close()
	commit := func(command persistence.CollectionCommand) error {
		command.OperationID, command.UploadID, command.ValidationFence = id, state.UploadID, fence
		next, err := c.submitCollectionValidation(ctx, command, now())
		if err == nil {
			state = next
		}
		return err
	}
	if artifact != nil {
		if err := commit(persistence.CollectionCommand{Action: "plan_begin", PlanBegin: &persistence.CollectionPlanBegin{Header: artifact.header, Descriptor: artifact.descriptor}}); err != nil {
			return state, err
		}
		var ordinal uint64
		if err := visitCollectionPlanArtifact(ctx, artifact, func(fragment persistence.CollectionPlanFragment) error {
			ordinal++
			return commit(persistence.CollectionCommand{Action: "plan_append", PlanID: artifact.header.PlanID,
				PlanFragment: &persistence.CollectionPlanLedgerFragment{Ordinal: ordinal, Fragment: fragment}})
		}); err != nil {
			return state, err
		}
		proof, err := c.store.VerifyCollectionPlan(ctx, id, now())
		if err != nil {
			return state, err
		}
		if err := commit(persistence.CollectionCommand{Action: "plan_finalize", PlanFinalize: &proof}); err != nil {
			return state, err
		}
	}
	header := persistence.CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: id, UploadID: state.UploadID,
		InputProgressDigest: request.InputProgressDigest, ItemCount: request.ItemCount, Authority: request.Authority,
		CapabilitiesDigest: request.CapabilitiesDigest, Valid: result.valid, Issue: result.issue, SummaryOnly: result.summaryOnly}
	if artifact != nil {
		header.PlanID, header.PlanDigest = artifact.header.PlanID, artifact.descriptor.Digest
	}
	if err := commit(persistence.CollectionCommand{Action: "validation_begin", ValidationBegin: &persistence.CollectionValidationBegin{Header: header, Descriptor: result.descriptor}}); err != nil {
		return state, err
	}
	if err := result.emit(ctx, func(items []persistence.CollectionValidationItem) error {
		return commit(persistence.CollectionCommand{Action: "validation_append", ValidationID: header.ResultID, ValidationItems: items})
	}); err != nil {
		return state, err
	}
	if err := commit(persistence.CollectionCommand{Action: "validation_finalize", ValidationID: header.ResultID}); err != nil {
		return state, err
	}
	return state, nil
}

// A pipe connects the bounded codec to the durable fragment visitor without a
// second complete encoded artifact. Closing the reader always releases and joins
// the sole encoder goroutine, including visitor failures and cancellation.
func visitCollectionPlanArtifact(ctx context.Context, artifact *collectionPlanArtifact, visit func(persistence.CollectionPlanFragment) error) error {
	reader, writer := io.Pipe()
	encoded := make(chan error, 1)
	go func() {
		_, err := artifact.writeTo(ctx, writer)
		_ = writer.CloseWithError(err)
		encoded <- err
	}()
	descriptor, err := persistence.DecodeCollectionPlan(ctx, reader, visit)
	_ = reader.CloseWithError(err)
	writeErr := <-encoded
	if err != nil || writeErr != nil {
		return errors.Join(err, writeErr)
	}
	if descriptor != artifact.descriptor {
		return persistence.ErrCollectionConflict
	}
	return nil
}
