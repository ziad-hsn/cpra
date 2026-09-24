package management

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type reselectionTransferProgress struct {
	Operation  api.Operation
	Complete   bool
	Reconciled bool
}

// The holder owns a successful proof and its spool. step and Close serialize;
// the enclosing attempt must cancel in-flight work before waiting for Close.
// After an uncertain submission, later steps only reconcile: an absent row
// remains unconfirmed and is never automatically resubmitted by this holder.
type collectionReselectionTransfer struct {
	mu        *sync.Mutex
	proof     *collectionReselectionProof
	next      int
	pending   *persistence.Command
	uncertain bool
	closed    bool
	submit    func(context.Context, []persistence.Command) ([]persistence.Result, error)
}

func (collectionReselectionTransfer) String() string {
	return "private collection reselection transfer"
}
func (t collectionReselectionTransfer) GoString() string { return t.String() }
func (t collectionReselectionTransfer) Format(w fmt.State, _ rune) {
	_, _ = w.Write([]byte(t.String()))
}
func (collectionReselectionTransfer) MarshalJSON() ([]byte, error) { return nil, errReselectionInput }

// Success moves ownership and invalidates the caller's proof handle. Failure
// leaves ownership with the caller. No encryption or durable mutation occurs.
func newReselectionTransfer(ctx context.Context, proof *collectionReselectionProof) (*collectionReselectionTransfer, error) {
	if ctx == nil || proof == nil || !proof.verified || proof.closed ||
		uint64(len(proof.suffix)) != proof.head.ItemCount-proof.head.Uploaded {
		return nil, ErrValidation
	}
	if err := proof.check(ctx); err != nil {
		return nil, err
	}
	owned := *proof
	owned.head = proof.head.Clone()
	t := &collectionReselectionTransfer{mu: &sync.Mutex{}, proof: &owned, submit: proof.catalog.store.Submit}
	proof.close()
	return t, nil
}

func (t *collectionReselectionTransfer) authority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.closed || t.proof == nil {
		return ErrUnavailable
	}
	p := t.proof
	if err := p.catalog.readyContext(ctx); err != nil {
		return err
	}
	current, err := p.catalog.store.ObserveOperatorAuthority(ctx, p.authority.Actor, p.now())
	if err != nil {
		return err
	}
	if current != p.authority {
		return persistence.ErrAuthenticationConflict
	}
	_, err = p.catalog.store.CollectionOperationAs(ctx, p.head.ID, p.authority.Actor, p.now())
	return err
}

func (t *collectionReselectionTransfer) observation(ctx context.Context, reconciled bool) (reselectionTransferProgress, error) {
	if err := t.authority(ctx); err != nil {
		return reselectionTransferProgress{}, err
	}
	p := t.proof
	receipt, err := p.catalog.store.CollectionOperationAs(ctx, p.head.ID, p.authority.Actor, p.now())
	if err != nil {
		return reselectionTransferProgress{}, err
	}
	return reselectionTransferProgress{Operation: collectionOperationView(receipt), Complete: t.next == len(p.suffix), Reconciled: reconciled}, nil
}

// reconcile accepts only an unchanged captured prefix or the exact one-row
// extension prepared by this holder. A concurrently uploaded equivalent
// plaintext in another envelope is a conflict, as is any additional row.
func (t *collectionReselectionTransfer) reconcile(ctx context.Context) (bool, error) {
	if err := t.authority(ctx); err != nil {
		return false, err
	}
	p := t.proof
	if t.pending != nil && !p.canWrite(t.pending.Collection.Item.Key) {
		return false, errCollectionReadDenied
	}
	view, head, err := p.catalog.store.CollectionUploadView(ctx, p.head.ID, p.now())
	if err != nil {
		return false, err
	}
	if head.Uploaded == p.head.Uploaded {
		return false, p.check(ctx)
	}
	if t.pending == nil || head.Uploaded != p.head.Uploaded+1 {
		return false, persistence.ErrCollectionConflict
	}
	stored, err := view.Item(ctx, head.Uploaded, p.now())
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(stored, *t.pending.Collection.Item) {
		// Another uploader's valid envelope is ordinary contention, not
		// evidence that the committed ledger itself is corrupt.
		return false, persistence.ErrCollectionConflict
	}
	if err := view.VerifyAppend(ctx, p.head, *t.pending.Collection.Item, p.now()); err != nil {
		return false, err
	}
	if err := t.authority(ctx); err != nil {
		return false, err
	}
	if !p.canWrite(t.pending.Collection.Item.Key) {
		return false, errCollectionReadDenied
	}
	p.view, p.head = view, head
	t.next++
	t.pending, t.uncertain = nil, false
	return true, nil
}

func (t *collectionReselectionTransfer) prepare(ctx context.Context) error {
	p := t.proof
	var row persistence.CollectionItem
	err := p.withSuffix(ctx, t.next, func(input CollectionUploadItem) error {
		row = persistence.CollectionItem{Ordinal: input.Ordinal, Key: input.Key, Source: input.Source,
			SourceDocument: input.SourceDocument, SourceItem: input.SourceItem, ContentDigest: input.ContentDigest}
		var err error
		row.Payload, err = p.catalog.sealer.Seal(ctx, row.Binding(p.catalog.storeID, p.head.UploadID), input.Resource)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return err
	}
	if row.Ordinal != p.head.Uploaded+1 {
		return persistence.ErrCollectionConflict
	}
	t.pending = &persistence.Command{Kind: "collection", Collection: &persistence.CollectionCommand{
		Action: "upload", OperationID: p.head.ID, UploadID: p.head.UploadID, Item: &row,
		UploadFence: &persistence.CollectionUploadFence{Uploaded: p.head.Uploaded, EncodedBytes: p.head.EncodedBytes,
			ProgressDigest: p.head.ProgressDigest, Authority: p.authority},
	}}
	return nil
}

// step commits at most one original inactive row. Preparation is outside admit;
// the FSM checks current operator authority and the exact prefix atomically.
// A later step may resolve an uncertain outcome by protected reads alone,
// without renewing inactivity. No step validates or activates configuration,
// and no step invokes jobs.
func (t *collectionReselectionTransfer) step(ctx context.Context, admit CollectionCommit) (reselectionTransferProgress, error) {
	if t == nil || t.mu == nil || ctx == nil || admit == nil {
		return reselectionTransferProgress{}, ErrValidation
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.authority(ctx); err != nil {
		return reselectionTransferProgress{}, err
	}
	if t.next == len(t.proof.suffix) {
		return t.observation(ctx, false)
	}
	reconciled, err := t.reconcile(ctx)
	if err != nil {
		return reselectionTransferProgress{}, err
	}
	if reconciled {
		return t.observation(ctx, true)
	}
	if t.uncertain {
		return reselectionTransferProgress{}, ErrOutcomeUnconfirmed
	}
	if t.pending == nil {
		if err := t.prepare(ctx); err != nil {
			return reselectionTransferProgress{}, err
		}
	}
	var results []persistence.Result
	invoked, submitted := false, false
	err = admit(func() error {
		if invoked {
			return ErrValidation
		}
		invoked = true
		if err := t.authority(ctx); err != nil {
			return err
		}
		if err := t.proof.check(ctx); err != nil {
			return err
		}
		if !t.proof.canWrite(t.pending.Collection.Item.Key) {
			return errCollectionReadDenied
		}
		// Before any attempt at submission the observation time is fresh.
		// If its response is lost, the entire command remains frozen.
		t.pending.At = t.proof.now().UTC()
		submitted = true
		var submitErr error
		results, submitErr = t.submit(ctx, []persistence.Command{*t.pending})
		if submitErr != nil {
			return errors.Join(ErrOutcomeUnconfirmed, submitErr)
		}
		return nil
	})
	if err != nil {
		if submitted {
			t.uncertain = true
		}
		return reselectionTransferProgress{}, err
	}
	if !submitted {
		return reselectionTransferProgress{}, ErrValidation
	}
	if len(results) != 1 || results[0].Err == nil && (!results[0].Allowed || results[0].Collection == nil) {
		t.uncertain = true
		return reselectionTransferProgress{}, ErrOutcomeUnconfirmed
	}
	if results[0].Err != nil {
		return reselectionTransferProgress{}, results[0].Err
	}
	// Even a successful response is not used as an unchecked new prefix:
	// reconcile the actual protected original ciphertext and digest chain.
	t.uncertain = true
	reconciled, err = t.reconcile(ctx)
	if err != nil {
		return reselectionTransferProgress{}, err
	}
	if !reconciled {
		return reselectionTransferProgress{}, ErrOutcomeUnconfirmed
	}
	return t.observation(ctx, false)
}

func (t *collectionReselectionTransfer) Close() error {
	if t == nil || t.mu == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	spool := t.proof.spool
	t.proof.close()
	t.proof, t.pending, t.submit = nil, nil, nil
	return spool.Close()
}
