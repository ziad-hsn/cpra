package management

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

var errReselectionInput = errors.New("selected sources do not match the original collection")

type reselectionSuffixRow struct {
	ref    stagedItemRef
	digest string
	record reselectionSpoolRecord
}

// A proof owns only encrypted suffix references and the captured prefix. The
// caller owns the spool and must discard it on failure. No durable mutation or
// provider operation occurs here. All methods require single-owner access.
type collectionReselectionProof struct {
	catalog   *Catalog
	view      *persistence.CollectionUploadView
	head      persistence.CollectionState
	authority persistence.OperatorAuthority
	spool     *reselectionSpool
	canWrite  func(persistence.CatalogKey) bool
	now       func() time.Time
	suffix    []reselectionSuffixRow
	verified  bool
	closed    bool
}

func (collectionReselectionProof) String() string               { return "private collection reselection proof" }
func (p collectionReselectionProof) GoString() string           { return p.String() }
func (p collectionReselectionProof) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(p.String())) }
func (collectionReselectionProof) MarshalJSON() ([]byte, error) { return nil, errReselectionInput }

// The caller must authenticate the named actor before construction. Committed
// authority, ownership, profile and prefix are checked before decrypting the
// identity, and again after every external key operation. Only the explicit
// base file profile is supported; unprofiled uploads remain on their old path.
func proveCollectionReselection(ctx context.Context, catalog *Catalog, id, actor string, sources *reselectionSources,
	spool *reselectionSpool, canWrite func(persistence.CatalogKey) bool, now func() time.Time) (*collectionReselectionProof, error) {
	if ctx == nil || catalog == nil || sources == nil || spool == nil || sources.spool != spool || canWrite == nil || now == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := catalog.readyContext(ctx); err != nil {
		return nil, err
	}
	progress, err := sources.progress()
	if err != nil {
		return nil, err
	}
	if !progress.Complete {
		return nil, ErrValidation
	}
	authority, err := catalog.store.ObserveOperatorAuthority(ctx, actor, now())
	if err != nil {
		return nil, err
	}
	if _, err := catalog.store.CollectionOperationAs(ctx, id, actor, now()); err != nil {
		return nil, err
	}
	view, head, err := catalog.store.CollectionUploadView(ctx, id, now())
	if err != nil {
		return nil, err
	}
	if head.Actor != actor || head.Owner == nil || head.Owner.Actor != actor || head.Owner.Epoch != authority.Epoch {
		return nil, persistence.ErrOperationNotFound
	}
	if head.NormalizationProfile != collection.FileNormalizationProfile {
		return nil, collection.ErrUnsupportedNormalization
	}
	if head.ItemCount > 10_000 {
		return nil, errReselectionSpoolQuota
	}
	p := &collectionReselectionProof{catalog: catalog, view: view, head: head, authority: authority, spool: spool, canWrite: canWrite, now: now}
	ok := false
	defer func() {
		if !ok {
			p.close()
		}
	}()
	if err := p.check(ctx); err != nil {
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
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	key := plain[1 : 1+commitment.KeyBytes]
	var fingerprint [commitment.MACBytes]byte
	copy(fingerprint[:], plain[1+commitment.KeyBytes:])
	defer clear(fingerprint[:])
	if err := p.verifySources(ctx, sources, progress.SourceCount, key, fingerprint); err != nil {
		return nil, err
	}
	if err := p.verifyInventory(ctx, sources, progress.SourceCount, key, fingerprint); err != nil {
		return nil, err
	}
	if err := p.view.VerifyPrefix(ctx, p.now()); err != nil {
		return nil, err
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	p.verified, ok = true, true
	return p, nil
}

func (p *collectionReselectionProof) check(ctx context.Context) error {
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
	current, err := p.catalog.store.ObserveOperatorAuthority(ctx, p.authority.Actor, p.now())
	if err != nil {
		return err
	}
	if current != p.authority {
		return persistence.ErrAuthenticationConflict
	}
	if err := p.view.Check(ctx, p.now()); err != nil {
		return err
	}
	_, err = p.spool.accounting()
	return err
}

func (p *collectionReselectionProof) close() {
	if p == nil || p.closed {
		return
	}
	p.closed, p.verified = true, false
	p.head.Secret = secureconfig.Envelope{}
	p.suffix = nil
	p.catalog, p.view, p.spool, p.canWrite, p.now = nil, nil, nil, nil, nil
}

func (p *collectionReselectionProof) verifySources(ctx context.Context, sources *reselectionSources, count int, key []byte, expected [commitment.MACBytes]byte) error {
	acc, err := commitment.NewSourceAccumulator(key, uint64(count), 64<<20)
	if err != nil {
		return errReselectionInput
	}
	defer acc.Close()
	scratch := make([]byte, 32<<10)
	defer clear(scratch)
	for n := 1; n <= count; n++ {
		if err := p.check(ctx); err != nil {
			return err
		}
		token, err := commitment.SourceToken(uint64(n))
		if err != nil || acc.Begin(token) != nil {
			return errReselectionInput
		}
		r, err := sources.reader(ctx, uint64(n))
		if err != nil {
			return err
		}
		defer r.Close()
		_, copyErr := io.CopyBuffer(acc, r, scratch)
		closeErr := r.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := acc.End(); err != nil {
			return errReselectionInput
		}
	}
	actual, err := acc.Finish()
	defer clear(actual[:])
	if err != nil || !hmac.Equal(actual[:], expected[:]) {
		return errReselectionInput
	}
	return p.check(ctx)
}

func (p *collectionReselectionProof) verifyInventory(ctx context.Context, sources *reselectionSources, count int, key []byte, fingerprint [commitment.MACBytes]byte) error {
	acc, err := commitment.NewAccumulator(key, p.head.ItemCount, fingerprint)
	if err != nil {
		return errReselectionInput
	}
	defer acc.Close()
	seen := make(map[persistence.CatalogKey]struct{})
	var ordinal uint64
	var encoded int64
	for source := 1; source <= count; source++ {
		if err := p.check(ctx); err != nil {
			return err
		}
		token, _ := commitment.SourceToken(uint64(source))
		r, err := sources.reader(ctx, uint64(source))
		if err != nil {
			return err
		}
		defer r.Close()
		var visitErr error
		parseErr := collection.NormalizeFile(ctx, r, p.head.NormalizationProfile, collection.DecodeOptions{SourceName: token}, func(item collection.NormalizedItem) error {
			defer clear(item.JSON)
			visitErr = func() error {
				if err := p.check(ctx); err != nil {
					return err
				}
				kind, id, found := strings.Cut(item.ID, "/")
				if !found || !supportedKind(kind) || !validID(id) {
					return errReselectionInput
				}
				resourceKey := persistence.CatalogKey{Kind: kind, ID: id}
				if !p.canWrite(resourceKey) {
					return errCollectionReadDenied
				}
				if _, duplicate := seen[resourceKey]; duplicate {
					return errReselectionInput
				}
				if ordinal >= p.head.ItemCount || ordinal >= 10_000 || int64(len(item.JSON)) > (512<<20)-encoded {
					return errReselectionInput
				}
				seen[resourceKey] = struct{}{}
				ordinal++
				encoded += int64(len(item.JSON))
				ref := stagedItemRef{Ordinal: ordinal, Key: resourceKey, Source: token, Document: uint64(item.Location.Document), Item: uint64(item.Location.Item)}
				position := commitment.Position{Ordinal: ordinal, ID: item.ID, Source: commitment.SourcePosition{Token: token, Document: ref.Document, Item: ref.Item}}
				mac, err := commitment.ItemMAC(key, position, item.JSON)
				if err != nil || acc.Add(position, mac) != nil {
					return errReselectionInput
				}
				digest := hex.EncodeToString(mac[:])
				if ordinal <= p.head.Uploaded {
					if err := p.verifyPrefix(ctx, ref, digest, item.JSON); err != nil {
						return err
					}
				} else {
					record, err := p.spool.appendSuffix(ctx, ordinal, item.JSON)
					if err != nil {
						return err
					}
					p.suffix = append(p.suffix, reselectionSuffixRow{ref: ref, digest: digest, record: record})
				}
				return p.check(ctx)
			}()
			return visitErr
		})
		closeErr := r.Close()
		if visitErr != nil {
			return visitErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if parseErr != nil {
			return errReselectionInput
		}
		if closeErr != nil {
			return closeErr
		}
	}
	expected, err := hex.DecodeString(p.head.ContentDigest)
	if err != nil || ordinal != p.head.ItemCount || acc.Verify(expected) != nil {
		return errReselectionInput
	}
	return nil
}

func (p *collectionReselectionProof) verifyPrefix(ctx context.Context, ref stagedItemRef, digest string, raw []byte) error {
	stored, err := p.view.Item(ctx, ref.Ordinal, p.now())
	if err != nil {
		return err
	}
	if stagedRef(stored) != ref || stored.ContentDigest != digest {
		return errReselectionInput
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	if !p.canWrite(ref.Key) {
		return errCollectionReadDenied
	}
	plain, err := p.catalog.sealer.Open(ctx, stored.Binding(p.catalog.storeID, p.head.UploadID), stored.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.catalog.fail()
		return ErrUnavailable
	}
	defer clear(plain)
	if !bytes.Equal(plain, raw) {
		return errReselectionInput
	}
	if !p.canWrite(ref.Key) {
		return errCollectionReadDenied
	}
	return p.check(ctx)
}

// withSuffix borrows one verified original resource outside spool locks. The
// callback must not retain plaintext, submit commands or perform external work.
// It may only prepare a detached conditional upload. A proof remains tied to its captured
// prefix; a transfer coordinator must use durable upload fences separately.
func (p *collectionReselectionProof) withSuffix(ctx context.Context, index int, visit func(CollectionUploadItem) error) error {
	if p == nil || !p.verified || visit == nil || index < 0 || index >= len(p.suffix) {
		return ErrValidation
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	row := p.suffix[index]
	if !p.canWrite(row.ref.Key) {
		return errCollectionReadDenied
	}
	var raw []byte
	err := p.spool.withRecord(ctx, row.record, reselectionSuffix, func(frame []byte) error {
		raw = bytes.Clone(frame)
		return nil
	})
	defer clear(raw)
	if err != nil {
		return err
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	if !p.canWrite(row.ref.Key) {
		return errCollectionReadDenied
	}
	if err := visit(CollectionUploadItem{Ordinal: row.ref.Ordinal, Key: row.ref.Key, Source: row.ref.Source,
		SourceDocument: row.ref.Document, SourceItem: row.ref.Item, ContentDigest: row.digest, Resource: raw}); err != nil {
		return err
	}
	return p.check(ctx)
}
