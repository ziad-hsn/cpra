package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// collectionUploadInput borrows one exact frozen resource span. It is an
// internal preparation argument, never a diagnostic or HTTP response. The
// caller must not mutate Resource during prepare; prepare never changes it.
type collectionUploadInput struct {
	Ref           stagedItemRef
	ContentDigest string
	Resource      []byte
}

func (collectionUploadInput) String() string {
	return "private collection upload input (payload omitted)"
}
func (i collectionUploadInput) GoString() string           { return i.String() }
func (i collectionUploadInput) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(i.String())) }
func (collectionUploadInput) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private collection upload input cannot be serialized")
}

// collectionUploadPreparation owns a captured inactive prefix and its inventory
// key. It is single-owner and does not commit or activate anything. The caller
// must authorize the operation before construction; canWrite additionally checks
// each resource identity before existence lookup, schema decoding or unwrapping.
// A prepared row must still pass the Store's original upload identity and next
// ordinal guards at submission. Refresh this object after prefix advancement.
type collectionUploadPreparation struct {
	catalog   *Catalog
	view      *persistence.CollectionUploadView
	head      persistence.CollectionState
	key       [commitment.KeyBytes]byte
	canWrite  func(persistence.CatalogKey) bool
	now       func() time.Time
	authority *persistence.OperatorAuthority
	closed    bool
}

func (collectionUploadPreparation) String() string {
	return "private encrypted upload preparation (input omitted)"
}
func (p collectionUploadPreparation) GoString() string           { return p.String() }
func (p collectionUploadPreparation) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(p.String())) }
func (collectionUploadPreparation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private upload preparation cannot be serialized")
}

func newCollectionUploadPreparation(ctx context.Context, catalog *Catalog, id string, canWrite func(persistence.CatalogKey) bool, now func() time.Time) (*collectionUploadPreparation, error) {
	return newCollectionUploadPreparationAs(ctx, catalog, id, "", canWrite, now)
}

func newCollectionUploadPreparationAs(ctx context.Context, catalog *Catalog, id, actor string, canWrite func(persistence.CatalogKey) bool, now func() time.Time) (*collectionUploadPreparation, error) {
	if ctx == nil || catalog == nil || canWrite == nil || now == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := catalog.readyContext(ctx); err != nil {
		return nil, err
	}
	view, head, err := catalog.store.CollectionUploadView(ctx, id, now())
	if err != nil {
		return nil, err
	}
	var authority *persistence.OperatorAuthority
	if actor != "" {
		if head.Actor != actor {
			return nil, persistence.ErrOperationNotFound
		}
		if head.Owner != nil {
			current, err := catalog.store.ObserveOperatorAuthority(ctx, actor, now())
			if err != nil {
				return nil, err
			}
			if head.Owner.Epoch != current.Epoch {
				return nil, persistence.ErrOperatorAuthorityDenied
			}
			authority = &current
		}
	}
	plain, err := catalog.sealer.Open(ctx, head.Binding(catalog.storeID), head.Secret)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		catalog.fail()
		return nil, ErrUnavailable
	}
	defer clear(plain)
	if len(plain) != 1+commitment.KeyBytes+commitment.MACBytes || plain[0] != collectionSecretVersion {
		catalog.fail()
		return nil, ErrUnavailable
	}
	p := &collectionUploadPreparation{catalog: catalog, view: view, head: head, canWrite: canWrite, now: now, authority: authority}
	copy(p.key[:], plain[1:1+commitment.KeyBytes])
	if err := p.check(ctx); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *collectionUploadPreparation) close() {
	if p == nil || p.closed {
		return
	}
	p.closed = true
	clear(p.key[:])
	p.head.Secret = secureconfig.Envelope{}
	p.catalog, p.view, p.canWrite, p.now = nil, nil, nil, nil
	p.authority = nil
}

func (p *collectionUploadPreparation) check(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.closed || p.catalog == nil {
		return ErrUnavailable
	}
	if err := p.catalog.readyContext(ctx); err != nil {
		return err
	}
	if p.authority != nil {
		current, err := p.catalog.store.ObserveOperatorAuthority(ctx, p.authority.Actor, p.now())
		if err != nil {
			return err
		}
		if current != *p.authority {
			return persistence.ErrAuthenticationConflict
		}
	}
	return p.view.Check(ctx, p.now())
}

// prepare authenticates the original bytes before decoding them. The bool
// reports an already committed identical row: its original ciphertext is reused
// even after wrapping-key rotation. It does not renew inactivity. Pending rows
// are sealed but remain inactive until a caller explicitly submits them.
// Complete inventory MAC verification, cross-resource validation and activation
// are separate steps; a single valid item does not authorize active changes.
func (p *collectionUploadPreparation) prepare(ctx context.Context, input collectionUploadInput) (persistence.CollectionItem, bool, error) {
	return p.prepareNext(ctx, input, 0)
}

// prepareNext permits one explicitly ordered pending ordinal beyond the captured
// prefix. Only bounded upload batching uses it; the caller must reject pending
// duplicate keys and attach an exact predicted UploadFence before submission.
// A zero next retains prepare's original single-row contract.
func (p *collectionUploadPreparation) prepareNext(ctx context.Context, input collectionUploadInput, next uint64) (persistence.CollectionItem, bool, error) {
	if err := p.check(ctx); err != nil {
		return persistence.CollectionItem{}, false, err
	}
	if !p.canWrite(input.Ref.Key) {
		return persistence.CollectionItem{}, false, errCollectionReadDenied
	}
	row := persistence.CollectionItem{Ordinal: input.Ref.Ordinal, Key: input.Ref.Key,
		Source: input.Ref.Source, SourceDocument: input.Ref.Document, SourceItem: input.Ref.Item, ContentDigest: input.ContentDigest}
	if next == 0 {
		next = p.head.Uploaded + 1
	}
	if next < p.head.Uploaded+1 || next > p.head.Uploaded+256 || row.Ordinal == 0 || row.Ordinal > p.head.ItemCount || row.Ordinal > p.head.Uploaded && row.Ordinal != next ||
		!supportedKind(row.Key.Kind) || !validID(row.Key.ID) || len(input.Resource) == 0 || len(input.Resource) > commitment.MaxResourceBytes {
		return persistence.CollectionItem{}, false, ErrValidation
	}
	var mac [commitment.MACBytes]byte
	if len(input.ContentDigest) != hex.EncodedLen(len(mac)) {
		return persistence.CollectionItem{}, false, ErrValidation
	}
	n, err := hex.Decode(mac[:], []byte(input.ContentDigest))
	if err != nil || n != len(mac) || hex.EncodeToString(mac[:]) != input.ContentDigest ||
		commitment.VerifyItem(p.key[:], itemPosition(row), input.Resource, mac[:]) != nil {
		return persistence.CollectionItem{}, false, ErrValidation
	}
	resource, err := api.DecodeResource(input.Resource)
	defer clear(resource.Spec)
	defer clear(resource.Status)
	if err != nil || resource.Kind != row.Key.Kind || resource.Metadata.ID != row.Key.ID {
		return persistence.CollectionItem{}, false, ErrValidation
	}
	if p.head.NormalizationProfile != "" && collection.ValidateFileProfileResource(p.head.NormalizationProfile, resource) != nil {
		return persistence.CollectionItem{}, false, ErrValidation
	}
	if err := p.check(ctx); err != nil {
		return persistence.CollectionItem{}, false, err
	}
	if row.Ordinal <= p.head.Uploaded {
		stored, err := p.view.Item(ctx, row.Ordinal, p.now())
		if err != nil {
			return persistence.CollectionItem{}, false, err
		}
		if stored.Key != row.Key || stored.Source != row.Source || stored.SourceDocument != row.SourceDocument ||
			stored.SourceItem != row.SourceItem || stored.ContentDigest != row.ContentDigest {
			return persistence.CollectionItem{}, false, persistence.ErrCollectionConflict
		}
		plain, err := p.catalog.sealer.Open(ctx, stored.Binding(p.catalog.storeID, p.head.UploadID), stored.Payload)
		if err != nil {
			if ctx.Err() != nil {
				return persistence.CollectionItem{}, false, ctx.Err()
			}
			p.catalog.fail()
			return persistence.CollectionItem{}, false, ErrUnavailable
		}
		defer clear(plain)
		if !bytes.Equal(plain, input.Resource) {
			return persistence.CollectionItem{}, false, persistence.ErrCollectionConflict
		}
		if err := p.check(ctx); err != nil {
			return persistence.CollectionItem{}, false, err
		}
		return stored, true, nil
	}
	if _, exists, err := p.view.Find(ctx, row.Key, p.now()); err != nil {
		return persistence.CollectionItem{}, false, err
	} else if exists {
		return persistence.CollectionItem{}, false, persistence.ErrCollectionConflict
	}
	row.Payload, err = p.catalog.sealer.Seal(ctx, row.Binding(p.catalog.storeID, p.head.UploadID), input.Resource)
	if err != nil {
		return persistence.CollectionItem{}, false, err
	}
	if err := p.check(ctx); err != nil {
		return persistence.CollectionItem{}, false, err
	}
	return row, false, nil
}
