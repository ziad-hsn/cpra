package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type reselectionProofFixture struct {
	catalog *Catalog
	store   *persistence.Store
	head    persistence.CollectionState
	policy  persistence.AuthenticationState
	at      time.Time
	raw     [][]byte
	items   []CollectionUploadItem
}

func reselectionProofFiles() [][]byte {
	return [][]byte{
		[]byte("# PRIVATE-SOURCE-COMMENT\napiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: first\nspec:\n  value: PRIVATE-SOURCE-VALUE\n"),
		[]byte("apiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: second\nspec:\n  value: ANOTHER-SOURCE-VALUE\n"),
		{},
	}
}

func newReselectionProofFixture(t *testing.T, uploaded int, alter func(*api.CollectionPrepareRequest), rawOverride [][]byte) *reselectionProofFixture {
	t.Helper()
	c, store := testCatalog(t)
	return newReselectionProofFixtureStore(t, c, store, uploaded, alter, rawOverride)
}

func newReselectionProofFixtureStore(t *testing.T, c *Catalog, store *persistence.Store, uploaded int, alter func(*api.CollectionPrepareRequest), rawOverride [][]byte) *reselectionProofFixture {
	t.Helper()
	f := &reselectionProofFixture{catalog: c, store: store, at: time.Now().UTC(), raw: reselectionProofFiles()}
	bootstrap := collectionOwnerBootstrap(f.at.Add(-time.Second))
	bootstrap.Principals = append(bootstrap.Principals, persistence.AuthenticationPrincipal{ID: "other/operator", Role: "operator", TokenSHA256: strings.Repeat("34", 32)})
	var err error
	f.policy, err = store.CommitAuthentication(t.Context(), bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{49}, commitment.KeyBytes)
	defer clear(key)
	for i, raw := range f.raw {
		token, _ := commitment.SourceToken(uint64(i + 1))
		err := collection.NormalizeFile(t.Context(), bytes.NewReader(raw), collection.FileNormalizationProfile, collection.DecodeOptions{}, func(item collection.NormalizedItem) error {
			kind, id, _ := strings.Cut(item.ID, "/")
			row := CollectionUploadItem{Ordinal: uint64(len(f.items) + 1), Key: persistence.CatalogKey{Kind: kind, ID: id}, Source: token,
				SourceDocument: uint64(item.Location.Document), SourceItem: uint64(item.Location.Item), Resource: item.JSON}
			pos := commitment.Position{Ordinal: row.Ordinal, ID: item.ID, Source: commitment.SourcePosition{Token: token, Document: row.SourceDocument, Item: row.SourceItem}}
			mac, err := commitment.ItemMAC(key, pos, item.JSON)
			if err != nil {
				return err
			}
			row.ContentDigest = hex.EncodeToString(mac[:])
			f.items = append(f.items, row)
			return nil
		})
		if err != nil {
			t.Fatal("normalize fixture", err)
		}
	}
	if rawOverride != nil {
		f.raw = rawOverride
	}
	sources, err := commitment.NewSourceAccumulator(key, uint64(len(f.raw)), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	for n, raw := range f.raw {
		token, _ := commitment.SourceToken(uint64(n + 1))
		if err := sources.Begin(token); err != nil {
			t.Fatal(err)
		}
		if _, err := sources.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := sources.End(); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := sources.Finish()
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, uint64(len(f.items)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	for _, row := range f.items {
		pos := commitment.Position{Ordinal: row.Ordinal, ID: row.Key.Kind + "/" + row.Key.ID, Source: commitment.SourcePosition{Token: row.Source, Document: row.SourceDocument, Item: row.SourceItem}}
		mac, err := commitment.ItemMAC(key, pos, row.Resource)
		if err != nil {
			t.Fatal(err)
		}
		if err := acc.Add(pos, mac); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	request := api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(commitment.Format), IdentityKey: api.Pointer(hex.EncodeToString(key)),
		SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: int64(len(f.items)), NormalizationProfile: collection.FileNormalizationProfile}
	if alter != nil {
		alter(&request)
	}
	ticket, err := c.PrepareCollection(t.Context(), request, "team/operator", func() time.Time { return f.at }, allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.CreateCollection(t.Context(), collectionCreateRequest(request, ticket.Ticket), "team/operator", func() time.Time { return f.at }, allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded > 0 {
		_, err = c.UploadCollection(t.Context(), op.ID, "team/operator", f.items[:uploaded], func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at }, allowCollectionCommit)
		if err != nil {
			t.Fatal(err)
		}
	}
	f.head, _, err = store.CollectionGet(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, row := range f.items {
			clear(row.Resource)
		}
	})
	return f
}

func (f *reselectionProofFixture) stage(t *testing.T, raw [][]byte) (*reselectionSpool, *reselectionSources) {
	t.Helper()
	root, _ := reselectionTestRoot(t)
	spool := reselectionTestSpool(t, root)
	sources, err := newReselectionSources(spool, len(raw), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	for n, data := range raw {
		if _, err := sources.append(t.Context(), uint64(n+1), 0, true, data); err != nil {
			t.Fatal(err)
		}
	}
	return spool, sources
}

func (f *reselectionProofFixture) prove(ctx context.Context, spool *reselectionSpool, sources *reselectionSources, actor string, allow func(persistence.CatalogKey) bool) (*collectionReselectionProof, error) {
	if allow == nil {
		allow = func(persistence.CatalogKey) bool { return true }
	}
	return proveCollectionReselection(ctx, f.catalog, f.head.ID, actor, sources, spool, allow, func() time.Time { return f.at })
}

func TestCollectionReselectionProofPreservesOriginalPrefix(t *testing.T) {
	for _, uploaded := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(uploaded), func(t *testing.T) {
			f := newReselectionProofFixture(t, uploaded, nil, nil)
			spool, sources := f.stage(t, f.raw)
			before, err := f.store.CollectionPage(f.head.ID, 0, 256)
			if err != nil {
				t.Fatal(err)
			}
			index := f.store.Status().CommittedIndex
			p, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			if !p.verified || len(p.suffix) != len(f.items)-uploaded {
				t.Fatal("incorrect suffix")
			}
			for n := range p.suffix {
				var borrowed []byte
				if err := p.withSuffix(t.Context(), n, func(row CollectionUploadItem) error {
					if !reflect.DeepEqual(row, f.items[n+uploaded]) {
						t.Error("suffix changed original item")
					}
					borrowed = row.Resource
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
					t.Fatal("borrowed resource retained plaintext")
				}
			}
			after, err := f.store.CollectionPage(f.head.ID, 0, 256)
			if err != nil || !reflect.DeepEqual(before, after) || f.store.Status().CommittedIndex != index {
				t.Fatal("proof mutated original upload", err)
			}
			catalog, err := f.store.CatalogSnapshot()
			if err != nil || catalog.Len() != 0 {
				t.Fatal("proof activated configuration", err)
			}
			for _, v := range []any{p, *p} {
				if _, err := json.Marshal(v); err == nil {
					t.Fatal("private proof serialized")
				}
				if text := fmt.Sprintf("%+v %#v", v, v); strings.Contains(text, "PRIVATE") || strings.Contains(text, "team/operator") {
					t.Fatal("private proof formatted")
				}
			}
			p.close()
			if p.suffix != nil || p.head.Secret.Ciphertext != nil || p.check(t.Context()) == nil {
				t.Fatal("closed proof remained usable")
			}
		})
	}
}

func TestCollectionReselectionProofRequiresBothCommitments(t *testing.T) {
	for _, change := range []string{"comment", "order", "boundary", "inventory", "malformed-final", "duplicate-final"} {
		t.Run(change, func(t *testing.T) {
			var alter func(*api.CollectionPrepareRequest)
			var override [][]byte
			if change == "inventory" {
				alter = func(r *api.CollectionPrepareRequest) { r.ContentDigest = strings.Repeat("ab", 32) }
			}
			if change == "malformed-final" {
				override = reselectionProofFiles()
				override[2] = []byte("kind: [PRIVATE-BROKEN")
			}
			if change == "duplicate-final" {
				override = reselectionProofFiles()
				override[2] = bytes.Clone(override[0])
			}
			f := newReselectionProofFixture(t, 1, alter, override)
			raw := append([][]byte(nil), f.raw...)
			switch change {
			case "comment":
				raw[0] = append([]byte("# changed\n"), raw[0]...)
			case "order":
				raw[0], raw[1] = raw[1], raw[0]
			case "boundary":
				raw = append(raw, []byte{})
			}
			spool, sources := f.stage(t, raw)
			index := f.store.Status().CommittedIndex
			p, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if p != nil || !errors.Is(err, errReselectionInput) {
				t.Fatal("changed original input accepted", err)
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "PRIVATE") {
				t.Fatal("parser input leaked")
			}
			current, _, readErr := f.store.CollectionGet(f.head.ID)
			if readErr != nil || !reflect.DeepEqual(current, f.head) || f.store.Status().CommittedIndex != index {
				t.Fatal("failed proof changed original upload", readErr)
			}
		})
	}
}

func TestCollectionReselectionProofAuthorizesBeforeUnwrap(t *testing.T) {
	for _, change := range []string{"other-owner", "no-profile", "expired", "credential-expired", "canceled"} {
		t.Run(change, func(t *testing.T) {
			var alter func(*api.CollectionPrepareRequest)
			if change == "no-profile" {
				alter = func(r *api.CollectionPrepareRequest) { r.NormalizationProfile = "" }
			}
			f := newReselectionProofFixture(t, 1, alter, nil)
			spool, sources := f.stage(t, f.raw)
			actor := "team/operator"
			switch change {
			case "other-owner":
				actor = "other/operator"
			case "expired":
				f.at = f.head.ExpiresAt
			case "credential-expired":
				f.at = f.policy.Principals[0].ExpiresAt
			case "canceled":
				if _, err := f.catalog.CancelCollection(t.Context(), f.head.ID, actor, func() time.Time { return f.at }, allowCollectionCommit); err != nil {
					t.Fatal(err)
				}
			}
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			observed := &uploadObservedWrapper{KeyWrapper: base}
			f.catalog.sealer, _ = secureconfig.NewSealer(observed)
			p, err := f.prove(t.Context(), spool, sources, actor, nil)
			if p != nil || err == nil || observed.unwraps != 0 {
				t.Fatal("denied proof opened encrypted identity", err, observed.unwraps)
			}
		})
	}
}

func TestCollectionReselectionProofRechecksAcrossKeyUnwrap(t *testing.T) {
	for _, uploaded := range []int{0, 1} {
		for _, change := range []string{"context", "authority", "expiry", "prefix"} {
			t.Run(fmt.Sprintf("uploaded-%d/%s", uploaded, change), func(t *testing.T) {
				f := newReselectionProofFixture(t, uploaded, nil, nil)
				spool, sources := f.stage(t, f.raw)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				input := f.items[uploaded]
				row := persistence.CollectionItem{Ordinal: input.Ordinal, Key: input.Key, Source: input.Source, SourceDocument: input.SourceDocument, SourceItem: input.SourceItem, ContentDigest: input.ContentDigest}
				var err error
				row.Payload, err = f.catalog.sealer.Seal(ctx, row.Binding(f.catalog.storeID, f.head.UploadID), input.Resource)
				if err != nil {
					t.Fatal(err)
				}
				base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
				opens := 0
				f.catalog.sealer, _ = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: base, before: func(context.Context) {
					opens++
					if opens != uploaded+1 {
						return
					}
					switch change {
					case "context":
						cancel()
					case "authority":
						f.at = f.policy.Principals[0].ExpiresAt
					case "expiry":
						f.at = f.head.ExpiresAt
					case "prefix":
						f.head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &row}, f.at)
					}
				}})
				p, err := f.prove(ctx, spool, sources, "team/operator", nil)
				if p != nil || err == nil {
					t.Fatal("stale proof returned", err)
				}
				if !f.catalog.Ready() {
					t.Fatal("ordinary fence conflict poisoned catalog")
				}
			})
		}
	}

}
func TestCollectionReselectionProofResourcePermissionAndLaterFence(t *testing.T) {
	f := newReselectionProofFixture(t, 1, nil, nil)
	spool, sources := f.stage(t, f.raw)
	if p, err := f.prove(t.Context(), spool, sources, "team/operator", func(persistence.CatalogKey) bool { return false }); p != nil || !errors.Is(err, errCollectionReadDenied) {
		t.Fatal("resource denial ignored", err)
	}
	spool, sources = f.stage(t, f.raw)
	p, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	f.at = f.policy.Principals[0].ExpiresAt
	called := false
	err = p.withSuffix(t.Context(), 0, func(CollectionUploadItem) error { called = true; return nil })
	if called || !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("expired authority released resource", err)
	}
}

func TestCollectionReselectionProofFailureAfterSuffixStaging(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	root, _ := reselectionTestRoot(t)
	spool, err := newReselectionSpool(t.Context(), root, reselectionSpoolOptions{AttemptID: uuid.NewString(), MaxPlaintextBytes: 4 << 20, MaxEncodedBytes: 8 << 20, MaxRecords: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	sources, err := newReselectionSources(spool, len(f.raw), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	for n, raw := range f.raw {
		if _, err := sources.append(t.Context(), uint64(n+1), 0, true, raw); err != nil {
			t.Fatal(err)
		}
	}
	index := f.store.Status().CommittedIndex
	p, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
	if p != nil || !errors.Is(err, errReselectionSpoolQuota) {
		t.Fatal("suffix quota was not enforced", err)
	}
	counts, err := spool.accounting()
	if err != nil || counts.Records != 4 {
		t.Fatal("failure did not follow one staged suffix", err)
	}
	after, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !reflect.DeepEqual(after, f.head) || index != f.store.Status().CommittedIndex {
		t.Fatal("failed proof changed durable input", err)
	}
}

func TestCollectionReselectionProofSuffixPanicClearsAndReleasesLock(t *testing.T) {
	f := newReselectionProofFixture(t, 1, nil, nil)
	spool, sources := f.stage(t, f.raw)
	p, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	var borrowed []byte
	func() {
		defer func() {
			if recover() != "fixture-panic" {
				t.Error("panic not propagated")
			}
		}()
		_ = p.withSuffix(t.Context(), 0, func(row CollectionUploadItem) error {
			if _, err := spool.accounting(); err != nil {
				t.Fatal(err)
			}
			borrowed = row.Resource
			panic("fixture-panic")
		})
	}()
	if len(borrowed) == 0 || !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("callback panic retained plaintext")
	}
}

func TestCollectionReselectionProofRechecksPermissionAfterPrefixUnwrap(t *testing.T) {
	f := newReselectionProofFixture(t, 2, nil, nil)
	spool, sources := f.stage(t, f.raw)
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	allowed, opens := true, 0
	f.catalog.sealer, _ = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: base, before: func(context.Context) {
		opens++
		if opens == 3 {
			allowed = false
		}
	}})
	p, err := f.prove(t.Context(), spool, sources, "team/operator", func(persistence.CatalogKey) bool { return allowed })
	if p != nil || !errors.Is(err, errCollectionReadDenied) || opens != 3 {
		t.Fatal("permission change during final prefix unwrap ignored", err, opens)
	}
}
