package management

import (
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

// A fixed allowance for simultaneous borrowed input and conservative decode /
// encode reservations. It permits a 1 MiB resource plus typed validation copies;
// sequential resources release their allowance regardless of total upload size.
const collectionSourcePlaintextBudget = 64 << 20
const collectionSecretVersion = 1

// The private header plaintext has a fixed binary format. It is never part of a
// resource response, event, graph index or formatter. Secret envelopes are bound
// to the upload identity and inventory digest by persistence.CollectionState.Binding.
func sealCollectionIdentity(ctx context.Context, sealer *secureconfig.Sealer, state persistence.CollectionState, storeID string, key, fingerprint []byte) (secureconfig.Envelope, error) {
	if len(key) != commitment.KeyBytes || len(fingerprint) != commitment.MACBytes || sealer == nil {
		return secureconfig.Envelope{}, ErrValidation
	}
	plain := make([]byte, 1+commitment.KeyBytes+commitment.MACBytes)
	defer clear(plain)
	plain[0] = collectionSecretVersion
	copy(plain[1:], key)
	copy(plain[1+commitment.KeyBytes:], fingerprint)
	return sealer.Seal(ctx, state.Binding(storeID), plain)
}

type stagedItemRef struct {
	Ordinal        uint64
	Key            persistence.CatalogKey
	Source         string
	Document, Item uint64
}

// collectionSourceItemError attributes a failed original input row without
// including its JSON, source filename, or callback diagnostic in formatting.
// A final inventory-commitment failure has no trustworthy per-item attribution.
type collectionSourceItemError struct {
	ref   stagedItemRef
	cause error
}

func (collectionSourceItemError) Error() string                { return "encrypted collection item validation failed" }
func (e collectionSourceItemError) Unwrap() error              { return e.cause }
func (e collectionSourceItemError) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(e.Error())) }

// collectionValidationSource owns one invocation's private key and bounded
// borrowed plaintext. It is single-owner; callbacks must not retain resources.
// This source is a validation building block, not an activation capability.
type collectionValidationSource struct {
	catalog              *Catalog
	view                 *persistence.CollectionValidationView
	head                 persistence.CollectionState
	key                  [commitment.KeyBytes]byte
	fingerprint          [commitment.MACBytes]byte
	canRead              func(persistence.CatalogKey) bool
	now                  func() time.Time
	liveBytes, peakBytes int
	closed               bool
}

func (collectionValidationSource) String() string {
	return "private encrypted collection source (input omitted)"
}
func (s collectionValidationSource) GoString() string           { return s.String() }
func (s collectionValidationSource) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(s.String())) }
func (collectionValidationSource) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private collection source cannot be serialized")
}

// The caller authorizes the collection operation before construction. canRead
// separately authorizes each identity before existence lookup or decryption.
// The clock is evaluated on every read; reading never renews upload inactivity.
func newCollectionValidationSource(ctx context.Context, catalog *Catalog, id string, canRead func(persistence.CatalogKey) bool, now func() time.Time) (*collectionValidationSource, error) {
	return newCollectionValidationSourceForAttempt(ctx, catalog, id, nil, canRead, now)
}

func newCollectionValidationSourceForAttempt(ctx context.Context, catalog *Catalog, id string, attempt *persistence.CollectionValidationRequestFence, canRead func(persistence.CatalogKey) bool, now func() time.Time) (*collectionValidationSource, error) {
	if ctx == nil || catalog == nil || canRead == nil || now == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := catalog.collectionValidationHealth(ctx, now()); err != nil {
		return nil, err
	}
	var view *persistence.CollectionValidationView
	var head persistence.CollectionState
	var err error
	if attempt == nil {
		view, head, err = catalog.store.CollectionValidationView(ctx, id, now())
	} else {
		view, head, err = catalog.store.CollectionValidationAttemptView(ctx, id, *attempt, now())
	}
	if err != nil {
		return nil, err
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
	s := &collectionValidationSource{catalog: catalog, view: view, head: head, canRead: canRead, now: now}
	copy(s.key[:], plain[1:1+commitment.KeyBytes])
	copy(s.fingerprint[:], plain[1+commitment.KeyBytes:])
	if err := s.check(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *collectionValidationSource) currentHeader(ctx context.Context) (persistence.CollectionState, error) {
	var head persistence.CollectionState
	var err error
	if attempt := persistence.CollectionValidationRequestFenceFor(s.head); attempt != nil {
		_, head, err = s.catalog.store.CollectionValidationAttemptView(ctx, s.head.ID, *attempt, s.now())
	} else {
		_, head, err = s.catalog.store.CollectionValidationView(ctx, s.head.ID, s.now())
	}
	return head, err
}

func (s *collectionValidationSource) close() {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	clear(s.key[:])
	clear(s.fingerprint[:])
	s.head.Secret = secureconfig.Envelope{}
	s.view, s.catalog, s.canRead, s.now = nil, nil, nil, nil
}

func (s *collectionValidationSource) check(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.closed {
		return ErrUnavailable
	}
	if err := s.catalog.collectionValidationHealth(ctx, s.now()); err != nil {
		return err
	}
	return s.view.Check(ctx, s.now())
}

// reserve accounts simultaneously retained plaintext/scratch representations,
// not total Go heap or RSS. Released sequential resources do not accumulate.
func (s *collectionValidationSource) reserve(bytes int) (func(), error) {
	if s == nil || s.closed || bytes < 0 || bytes > collectionSourcePlaintextBudget-s.liveBytes {
		return nil, ErrGraphLimit
	}
	s.liveBytes += bytes
	if s.liveBytes > s.peakBytes {
		s.peakBytes = s.liveBytes
	}
	released := false
	return func() {
		if !released {
			s.liveBytes -= bytes
			released = true
		}
	}, nil
}

func stagedRef(item persistence.CollectionItem) stagedItemRef {
	return stagedItemRef{Ordinal: item.Ordinal, Key: item.Key, Source: item.Source, Document: item.SourceDocument, Item: item.SourceItem}
}

func itemPosition(item persistence.CollectionItem) commitment.Position {
	return commitment.Position{Ordinal: item.Ordinal, ID: item.Key.Kind + "/" + item.Key.ID,
		Source: commitment.SourcePosition{Token: item.Source, Document: item.SourceDocument, Item: item.SourceItem}}
}

func (s *collectionValidationSource) withItem(ctx context.Context, item persistence.CollectionItem, use func(*api.Resource) error) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if use == nil {
		return ErrValidation
	}
	if !s.canRead(item.Key) {
		return errCollectionReadDenied
	}
	// Reserve before unwrapping or decoding. The envelope bound prevents an
	// attacker-controlled ciphertext length from overflowing this calculation.
	if item.Payload.Validate() != nil {
		return ErrUnavailable
	}
	release, err := s.reserve(3*len(item.Payload.Ciphertext) + 1024)
	if err != nil {
		return err
	}
	defer release()
	plain, err := s.catalog.sealer.Open(ctx, item.Binding(s.catalog.storeID, s.head.UploadID), item.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.catalog.fail()
		return ErrUnavailable
	}
	defer clear(plain)
	mac, err := hex.DecodeString(item.ContentDigest)
	if err != nil || commitment.VerifyItem(s.key[:], itemPosition(item), plain, mac) != nil {
		return ErrValidation
	}
	resource, err := api.DecodeResource(plain)
	defer clear(resource.Spec)
	defer clear(resource.Status)
	if err != nil || !supportedKind(resource.Kind) || resource.Kind != item.Key.Kind || resource.Metadata.ID != item.Key.ID {
		return ErrValidation
	}
	if s.head.NormalizationProfile != "" && collection.ValidateFileProfileResource(s.head.NormalizationProfile, resource) != nil {
		return ErrValidation
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := use(&resource); err != nil {
		return err
	}
	return s.check(ctx)
}

func (s *collectionValidationSource) withResource(ctx context.Context, key persistence.CatalogKey, use func(*api.Resource) error) (bool, error) {
	if err := s.check(ctx); err != nil {
		return false, err
	}
	if use == nil {
		return false, ErrValidation
	}
	if !s.canRead(key) {
		return false, errCollectionReadDenied
	}
	item, exists, err := s.view.Find(ctx, key, s.now())
	if err != nil || !exists {
		return false, err
	}
	return true, s.withItem(ctx, item, use)
}

// walk authenticates the complete inventory while borrowing one decoded resource
// at a time. A late error invalidates all earlier metadata observations; callers
// must discard them and perform no active mutation. The final MAC covers the
// original item bytes/positions, never credential-filled validation copies.
func (s *collectionValidationSource) walk(ctx context.Context, visit func(stagedItemRef, *api.Resource) error) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if visit == nil {
		return ErrValidation
	}
	accumulator, err := commitment.NewAccumulator(s.key[:], s.head.ItemCount, s.fingerprint)
	if err != nil {
		return ErrValidation
	}
	defer accumulator.Close()
	var after uint64
	for after < s.head.ItemCount {
		page, err := s.view.Page(ctx, after, 256, s.now())
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return ErrUnavailable
		}
		for _, item := range page {
			if item.Ordinal != after+1 {
				return ErrUnavailable
			}
			var mac [commitment.MACBytes]byte
			decoded, err := hex.DecodeString(item.ContentDigest)
			if err != nil || len(decoded) != len(mac) {
				return &collectionSourceItemError{ref: stagedRef(item), cause: ErrValidation}
			}
			copy(mac[:], decoded)
			if err := accumulator.Add(itemPosition(item), mac); err != nil {
				return &collectionSourceItemError{ref: stagedRef(item), cause: ErrValidation}
			}
			if err := s.withItem(ctx, item, func(resource *api.Resource) error { return visit(stagedRef(item), resource) }); err != nil {
				return &collectionSourceItemError{ref: stagedRef(item), cause: err}
			}
			after = item.Ordinal
		}
	}
	expected, err := hex.DecodeString(s.head.ContentDigest)
	if err != nil || accumulator.Verify(expected) != nil {
		return ErrValidation
	}
	return s.check(ctx)
}
