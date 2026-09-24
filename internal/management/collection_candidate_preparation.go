package management

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

var errCollectionPreparationNotRequired = errors.New("unchanged collection item requires no encrypted candidate")

// collectionCandidatePreparation owns a bounded detached original input holder.
// It retains no database transaction. No method submits,
// refreshes graph guards, constructs a provider or allocates a child operation.
type collectionCandidatePreparation struct {
	catalog   *Catalog
	view      *persistence.CollectionExecutionPreparationView
	head      persistence.CollectionState
	now       func() time.Time
	canWrite  func(persistence.CatalogKey) bool
	profile   string
	key       [commitment.KeyBytes]byte
	keyLoaded bool
	cached    *persistence.CollectionPreparedItem
	closed    bool
}

func (collectionCandidatePreparation) String() string {
	return "private collection candidate preparation (contents omitted)"
}
func (p collectionCandidatePreparation) GoString() string { return p.String() }
func (p collectionCandidatePreparation) Format(w fmt.State, _ rune) {
	_, _ = w.Write([]byte(p.String()))
}
func (collectionCandidatePreparation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private candidate preparation cannot be serialized")
}

func newCollectionCandidatePreparation(ctx context.Context, c *Catalog, id, actor string, canWrite func(persistence.CatalogKey) bool, now func() time.Time) (*collectionCandidatePreparation, error) {
	if ctx == nil || c == nil || now == nil || canWrite == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.verified.Load() || c.failed.Load() {
		return nil, ErrUnavailable
	}
	_, profile, err := collectionValidationProfile()
	if err != nil {
		return nil, ErrUnavailable
	}
	authority, err := c.store.ObserveOperatorAuthority(ctx, actor, now())
	if err != nil {
		return nil, err
	}
	view, err := c.store.CollectionExecutionPreparationView(ctx, id, authority, profile, now())
	if err != nil {
		return nil, err
	}
	p := &collectionCandidatePreparation{catalog: c, view: view, head: view.Header(), now: now, canWrite: canWrite, profile: profile}
	if err = p.check(ctx); err != nil {
		_ = p.close()
		return nil, err
	}
	return p, nil
}
func (p *collectionCandidatePreparation) close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	clear(p.key[:])
	p.keyLoaded = false
	if p.cached != nil {
		clearStagedRecord(&p.cached.Record)
	}
	p.cached = nil
	p.head = persistence.CollectionState{}
	err := p.view.Close()
	p.view = nil
	p.catalog = nil
	p.now = nil
	p.canWrite = nil
	return err
}
func (p *collectionCandidatePreparation) check(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.closed || p.catalog == nil || !p.catalog.verified.Load() || p.catalog.failed.Load() {
		return ErrUnavailable
	}
	_, profile, err := collectionValidationProfile()
	if err != nil || profile != p.profile {
		return persistence.ErrCollectionConflict
	}
	return p.view.Check(ctx, p.now())
}
func (p *collectionCandidatePreparation) loadKey(ctx context.Context) error {
	if p.keyLoaded {
		return nil
	}
	plain, err := p.catalog.sealer.Open(ctx, p.head.Binding(p.catalog.storeID), p.head.Secret)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.catalog.fail()
		return ErrUnavailable
	}
	defer clear(plain)
	if len(plain) != 1+commitment.KeyBytes+commitment.MACBytes || plain[0] != collectionSecretVersion {
		p.catalog.fail()
		return ErrUnavailable
	}
	copy(p.key[:], plain[1:1+commitment.KeyBytes])
	p.keyLoaded = true
	return p.check(ctx)
}

// prepare returns an existing immutable candidate without rewrapping, or creates
// one from authenticated original input and its exact validation-time target.
// The bool means that the candidate was already committed. Cached uncommitted
// retries retain their IDs/ciphertext but do not claim committed status.
func (p *collectionCandidatePreparation) prepare(ctx context.Context, ordinal uint64) (persistence.CollectionPreparedItem, bool, error) {
	if err := p.check(ctx); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	row, digest, err := p.view.Row(ctx, ordinal, p.now())
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	if !p.canWrite(row.Key) {
		return persistence.CollectionPreparedItem{}, false, errCollectionReadDenied
	}
	old, exists, err := p.view.Target(ctx, ordinal, p.now())
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	defer clearStagedRecord(&old)
	if row.Change == "unchanged" {
		if err := p.validateFileProfile(ctx, ordinal); err != nil {
			return persistence.CollectionPreparedItem{}, false, err
		}
		return persistence.CollectionPreparedItem{}, false, errCollectionPreparationNotRequired
	}
	if row.Change != "create" && row.Change != "update" || row.Change == "create" && exists || row.Change == "update" && !exists {
		return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
	}
	if original, ok, err := p.view.ExistingPrepared(ctx, p.now()); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	} else if ok {
		if err := p.validateFileProfile(ctx, ordinal); err != nil {
			clearStagedRecord(&original.Record)
			return persistence.CollectionPreparedItem{}, false, err
		}
		if original.Ordinal != ordinal || original.InputOrdinal != row.InputOrdinal || original.RowDigest != digest || original.Record.Key != row.Key {
			return persistence.CollectionPreparedItem{}, false, ErrUnavailable
		}
		if err := p.checkTarget(ctx, ordinal, row.Key); err != nil {
			clearStagedRecord(&original.Record)
			return persistence.CollectionPreparedItem{}, false, err
		}
		return original, true, nil
	}
	if p.cached != nil {
		if err := p.validateFileProfile(ctx, ordinal); err != nil {
			return persistence.CollectionPreparedItem{}, false, err
		}
		if p.cached.Ordinal != ordinal {
			return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
		}
		if err := p.checkTarget(ctx, ordinal, row.Key); err != nil {
			return persistence.CollectionPreparedItem{}, false, err
		}
		return p.cached.Clone(), false, nil
	}
	input, err := p.view.Input(ctx, ordinal, p.now())
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	defer func() { clear(input.Payload.Ciphertext); clear(input.Payload.WrappedKey); clear(input.Payload.Nonce) }()
	// Account simultaneously borrowed decoding/normalization representations. No
	// accumulated fleet plaintext or driver-library construction is retained.
	if 16*(len(input.Payload.Ciphertext)+len(old.Payload.Ciphertext))+16*1024 > collectionSourcePlaintextBudget {
		return persistence.CollectionPreparedItem{}, false, ErrGraphLimit
	}
	if err = p.loadKey(ctx); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	if err = p.checkTarget(ctx, ordinal, row.Key); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	plain, err := p.catalog.sealer.Open(ctx, input.Binding(p.catalog.storeID, p.head.UploadID), input.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return persistence.CollectionPreparedItem{}, false, ctx.Err()
		}
		p.catalog.fail()
		return persistence.CollectionPreparedItem{}, false, ErrUnavailable
	}
	defer clear(plain)
	mac, err := hex.DecodeString(input.ContentDigest)
	if err != nil || commitment.VerifyItem(p.key[:], itemPosition(input), plain, mac) != nil {
		p.catalog.fail()
		return persistence.CollectionPreparedItem{}, false, ErrUnavailable
	}
	resource, err := api.DecodeResource(plain)
	defer func() { clear(resource.Spec); clear(resource.Status) }()
	if err != nil || resource.Kind != row.Key.Kind || resource.Metadata.ID != row.Key.ID || validateMetadata(resource.Metadata) != nil {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	if p.head.NormalizationProfile != "" && collection.ValidateFileProfileResource(p.head.NormalizationProfile, resource) != nil {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	if err = p.checkTarget(ctx, ordinal, row.Key); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	clear(resource.Status)
	resource.Status = nil
	var previous api.Resource
	if exists {
		if resource.Metadata.UID != "" && resource.Metadata.UID != old.UID || resource.Metadata.ResourceVersion != "" && resource.Metadata.ResourceVersion != old.Revision || resource.Metadata.Generation != 0 && resource.Metadata.Generation != int64(old.Generation) {
			return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
		}
		previous, err = p.catalog.open(ctx, old)
		if err != nil {
			return persistence.CollectionPreparedItem{}, false, err
		}
		defer func() { clear(previous.Spec); clear(previous.Status) }()
		if err = p.checkTarget(ctx, ordinal, row.Key); err != nil {
			return persistence.CollectionPreparedItem{}, false, err
		}
		if resource.Kind == "Credential" {
			original := resource.Spec
			err = preserveCredential(&resource, previous)
			clear(original)
			if err != nil {
				return persistence.CollectionPreparedItem{}, false, ErrValidation
			}
		}
	} else if resource.Metadata.UID != "" || resource.Metadata.ResourceVersion != "" || resource.Metadata.Generation != 0 {
		return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
	}
	originalSpec := resource.Spec
	err = validateDesired(&resource)
	if resource.Kind == "Monitor" || resource.Kind == "NotificationEndpoint" {
		clear(originalSpec)
	}
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	var original *api.Resource
	if exists {
		original = &previous
	}
	shape, err := collectionCandidateEncoding(resource, original)
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	if !shape.Changed {
		return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
	}
	refs, err := directReferences(resource)
	if err != nil {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	at := p.now().UTC()
	if at.Before(p.head.Activation.At) || exists && at.Before(old.UpdatedAt) {
		return persistence.CollectionPreparedItem{}, false, persistence.ErrCollectionConflict
	}
	record := persistence.CatalogRecord{Key: row.Key, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: uint64(shape.Generation), Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at, References: refs}
	if exists {
		record.UID, record.CreatedAt = old.UID, old.CreatedAt
	}
	resource.Metadata.UID, resource.Metadata.ResourceVersion, resource.Metadata.Generation = record.UID, record.Revision, int64(record.Generation)
	if err := p.catalog.prepareJobTypeReferences(ctx, resource, &record); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	encoded, err := json.Marshal(resource)
	defer clear(encoded)
	if err != nil || len(encoded) > api.MaxResourceBytes || len(encoded) != shape.EncodedBytes {
		return persistence.CollectionPreparedItem{}, false, ErrValidation
	}
	if err = p.checkTarget(ctx, ordinal, row.Key); err != nil {
		return persistence.CollectionPreparedItem{}, false, err
	}
	record.Payload, err = p.catalog.sealer.Seal(ctx, record.Binding(p.catalog.storeID), encoded)
	if err != nil {
		if ctx.Err() != nil {
			return persistence.CollectionPreparedItem{}, false, ctx.Err()
		}
		return persistence.CollectionPreparedItem{}, false, ErrUnavailable
	}
	if err = p.checkTarget(ctx, ordinal, row.Key); err != nil {
		clearStagedRecord(&record)
		return persistence.CollectionPreparedItem{}, false, err
	}
	candidate := persistence.CollectionPreparedItem{Binding: persistence.CollectionExecutionBinding{OperationID: p.head.ID, UploadID: p.head.UploadID, ActivationID: p.head.Activation.ID, PlanID: p.head.Plan.Header.PlanID, PlanDigest: p.head.Plan.Descriptor.Digest}, ID: uuid.NewString(), Ordinal: ordinal, InputOrdinal: row.InputOrdinal, RowDigest: digest, At: at, Record: record}
	p.cached = &candidate
	return candidate.Clone(), false, nil
}

// validateFileProfile rechecks original staged bytes before using old sealed
// plans or prepared candidates. Earlier versions could accept a caller's base
// profile claim without checking tagged resource variants. Unprofiled work keeps
// its existing contract, including committed retries without key access.
func (p *collectionCandidatePreparation) validateFileProfile(ctx context.Context, ordinal uint64) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	if p.head.NormalizationProfile == "" {
		return nil
	}
	input, err := p.view.Input(ctx, ordinal, p.now())
	if err != nil {
		return err
	}
	defer func() { clear(input.Payload.Ciphertext); clear(input.Payload.WrappedKey); clear(input.Payload.Nonce) }()
	if !p.canWrite(input.Key) {
		return errCollectionReadDenied
	}
	if input.Payload.Validate() != nil || 3*len(input.Payload.Ciphertext)+1024 > collectionSourcePlaintextBudget {
		return ErrValidation
	}
	if err := p.loadKey(ctx); err != nil {
		return err
	}
	plain, err := p.catalog.sealer.Open(ctx, input.Binding(p.catalog.storeID, p.head.UploadID), input.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer clear(plain)
	mac, err := hex.DecodeString(input.ContentDigest)
	if err != nil || commitment.VerifyItem(p.key[:], itemPosition(input), plain, mac) != nil {
		return ErrValidation
	}
	resource, err := api.DecodeResource(plain)
	defer func() { clear(resource.Spec); clear(resource.Status) }()
	if err != nil || resource.Kind != input.Key.Kind || resource.Metadata.ID != input.Key.ID ||
		collection.ValidateFileProfileResource(p.head.NormalizationProfile, resource) != nil {
		return ErrValidation
	}
	return p.check(ctx)
}

func (p *collectionCandidatePreparation) checkTarget(ctx context.Context, ordinal uint64, key persistence.CatalogKey) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	if !p.canWrite(key) {
		return errCollectionReadDenied
	}
	target, _, err := p.view.Target(ctx, ordinal, p.now())
	clearStagedRecord(&target)
	return err
}
