package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type collectionSourceFixture struct {
	catalog *Catalog
	store   *persistence.Store
	head    persistence.CollectionState
	items   []persistence.CollectionItem
	at      time.Time
}

// Fixtures submit real encrypted commands through Store, then use only protected
// read APIs. The producer owns one plaintext resource at a time, even for a large
// inventory. Mutators model invalid clients and committed corrupted envelopes.
func stagedSourceFixture(t *testing.T, count int, input func(int) (persistence.CatalogKey, []byte), changeHeader func(*persistence.CollectionState), changeItem func(int, *persistence.CollectionItem), rewriteSecret ...func(*Catalog, *persistence.CollectionState)) collectionSourceFixture {
	t.Helper()
	c, store := testCatalog(t)
	return stagedSourceFixtureStore(t, c, store, count, input, changeHeader, changeItem, rewriteSecret...)
}

func stagedSourceFixtureStore(t *testing.T, c *Catalog, store *persistence.Store, count int, input func(int) (persistence.CatalogKey, []byte), changeHeader func(*persistence.CollectionState), changeItem func(int, *persistence.CollectionItem), rewriteSecret ...func(*Catalog, *persistence.CollectionState)) collectionSourceFixture {
	t.Helper()
	at := time.Now().UTC()
	key := bytes.Repeat([]byte{41}, commitment.KeyBytes)
	defer clear(key)
	fingerprint := [commitment.MACBytes]byte{73, 11}
	acc, err := commitment.NewAccumulator(key, uint64(count), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "test/operator", IdentityFormat: commitment.Format,
		ItemCount: uint64(count), MaxEncodedBytes: 512 << 20, ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading",
		CreatedAt: at, ActivityAt: at, ExpiresAt: at.Add(persistence.CollectionInactivityLifetime)}
	items := make([]persistence.CollectionItem, count)
	for i := range items {
		identity, raw := input(i)
		item := persistence.CollectionItem{Ordinal: uint64(i + 1), Key: identity, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: uint64(i + 1)}
		mac, err := commitment.ItemMAC(key, itemPosition(item), raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := acc.Add(itemPosition(item), mac); err != nil {
			t.Fatal(err)
		}
		item.ContentDigest = hex.EncodeToString(mac[:])
		item.Payload, err = c.sealer.Seal(context.Background(), item.Binding(c.storeID, head.UploadID), raw)
		clear(raw)
		if err != nil {
			t.Fatal(err)
		}
		if changeItem != nil {
			changeItem(i, &item)
		}
		items[i] = item
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	head.ContentDigest = hex.EncodeToString(digest[:])
	if changeHeader != nil {
		changeHeader(&head)
	}
	head.Secret, err = sealCollectionIdentity(context.Background(), c.sealer, head, c.storeID, key, fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	for _, rewrite := range rewriteSecret {
		rewrite(c, &head)
	}
	head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}, at)
	for i := range items {
		at = at.Add(time.Millisecond)
		head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &items[i]}, at)
	}
	return collectionSourceFixture{catalog: c, store: store, head: head, items: items, at: at}
}

func submitStagedSource(t *testing.T, store *persistence.Store, command persistence.CollectionCommand, at time.Time) persistence.CollectionState {
	t.Helper()
	results, err := store.Submit(context.Background(), []persistence.Command{{Kind: "collection", At: at, Collection: &command}})
	if err != nil || len(results) != 1 {
		t.Fatal("staging command failed", err)
	}
	if results[0].Err != nil || results[0].Collection == nil {
		t.Fatal("staging rejected", results[0].Err)
	}
	return results[0].Collection.Clone()
}

func sourceCredentials(size int) func(int) (persistence.CatalogKey, []byte) {
	return func(i int) (persistence.CatalogKey, []byte) {
		id := fmt.Sprintf("credential-%04d", i)
		value := "private-value-" + strings.Repeat("x", size)
		r := resource("Credential", id, api.CredentialSpec{Value: &value})
		raw, _ := json.Marshal(r)
		return persistence.CatalogKey{Kind: r.Kind, ID: id}, raw
	}
}

func (f *collectionSourceFixture) open(t *testing.T, canRead func(persistence.CatalogKey) bool) *collectionValidationSource {
	t.Helper()
	if canRead == nil {
		canRead = func(persistence.CatalogKey) bool { return true }
	}
	s, err := newCollectionValidationSource(context.Background(), f.catalog, f.head.ID, canRead, func() time.Time { return f.at })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func TestCollectionSourceWalkLookupAndNoEffects(t *testing.T) {
	f := stagedSourceFixture(t, 3, sourceCredentials(10), nil, nil)
	s := f.open(t, nil)
	before, err := f.store.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var positions []stagedItemRef
	var borrowed []byte
	err = s.walk(context.Background(), func(ref stagedItemRef, r *api.Resource) error {
		positions = append(positions, ref)
		if r.Kind != "Credential" || !bytes.Contains(r.Spec, []byte("private-value-")) {
			t.Fatal("wrong borrowed resource")
		}
		borrowed = r.Spec
		return nil
	})
	if err != nil || len(positions) != 3 {
		t.Fatal("inventory failed", err)
	}
	if !bytes.Equal(borrowed, make([]byte, len(borrowed))) || s.liveBytes != 0 {
		t.Fatal("borrowed bytes/reservation survived callback")
	}
	for i, ref := range positions {
		if ref.Ordinal != uint64(i+1) || ref.Source != f.items[i].Source || ref.Item != uint64(i+1) {
			t.Fatal("source coordinate changed")
		}
		found, err := s.withResource(context.Background(), ref.Key, func(r *api.Resource) error {
			if !bytes.Contains(r.Spec, []byte("private-value-")) {
				t.Fatal("previous clearing modified persisted resource")
			}
			return nil
		})
		if err != nil || !found {
			t.Fatal("lookup failed", err)
		}
	}
	called := false
	if found, err := s.withResource(context.Background(), persistence.CatalogKey{Kind: "Credential", ID: "absent"}, func(*api.Resource) error { called = true; return nil }); err != nil || found || called {
		t.Fatal("absence was not ordinary", err)
	}
	after, err := f.store.CatalogSnapshot()
	if err != nil || before.Index != after.Index || after.Len() != 0 {
		t.Fatal("validation mutated active catalog", err)
	}
	head, found, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !found || !reflect.DeepEqual(head, f.head) {
		t.Fatal("read renewed or changed upload", err)
	}
	s.close()
	if s.key != [commitment.KeyBytes]byte{} || s.fingerprint != [commitment.MACBytes]byte{} || s.catalog != nil {
		t.Fatal("close retained explicit key material")
	}
	if err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal("closed source remained usable", err)
	}
}

func TestCollectionSourceRejectsLateMalformedInputAndFinalMAC(t *testing.T) {
	for _, name := range []string{"malformed-last", "inventory-mac", "resource-identity", "item-mac"} {
		t.Run(name, func(t *testing.T) {
			input := sourceCredentials(10)
			makeInput := func(i int) (persistence.CatalogKey, []byte) {
				key, raw := input(i)
				if i == 2 && name == "malformed-last" {
					clear(raw)
					raw = []byte(`{"kind":`)
				}
				if i == 2 && name == "resource-identity" {
					key.ID = "mismatched"
				}
				return key, raw
			}
			f := stagedSourceFixture(t, 3, makeInput, func(h *persistence.CollectionState) {
				if name == "inventory-mac" {
					h.ContentDigest = strings.Repeat("a", 64)
				}
			}, nil)
			s := f.open(t, nil)
			if name == "item-mac" {
				s.key[0] ^= 1
			}
			seen := 0
			err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { seen++; return nil })
			if !errors.Is(err, ErrValidation) {
				t.Fatal("invalid collection accepted", err)
			}
			var positioned *collectionSourceItemError
			if name == "inventory-mac" {
				if errors.As(err, &positioned) {
					t.Fatal("whole inventory failure blamed one input item")
				}
			} else {
				want := uint64(3)
				if name == "item-mac" {
					want = 1
				}
				if !errors.As(err, &positioned) || positioned.ref.Ordinal != want {
					t.Fatal("incorrect source attribution")
				}
			}
			if name == "malformed-last" && seen != 2 || name == "inventory-mac" && seen != 3 || name == "item-mac" && seen != 0 {
				t.Fatal("unexpected validation boundary", seen)
			}
			view, _ := f.store.CatalogSnapshot()
			if view.Len() != 0 || s.liveBytes != 0 || !f.catalog.Ready() {
				t.Fatal("invalid client input changed active state or leaked a reservation")
			}
		})
	}
}

type sourceCountingWrapper struct {
	secureconfig.KeyWrapper
	reads *int
}

type sourceBeforeUnwrap struct {
	secureconfig.KeyWrapper
	before func(context.Context)
}

func (w sourceBeforeUnwrap) Unwrap(ctx context.Context, payload, aad []byte) ([]byte, error) {
	w.before(ctx)
	return w.KeyWrapper.Unwrap(ctx, payload, aad)
}

func (w sourceCountingWrapper) Unwrap(ctx context.Context, payload, aad []byte) ([]byte, error) {
	*w.reads++
	return w.KeyWrapper.Unwrap(ctx, payload, aad)
}

func TestCollectionSourceAuthorizationBeforeDecryptOrExistence(t *testing.T) {
	f := stagedSourceFixture(t, 1, sourceCredentials(10), nil, nil)
	s := f.open(t, func(persistence.CatalogKey) bool { return false })
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	reads := 0
	f.catalog.sealer, _ = secureconfig.NewSealer(sourceCountingWrapper{KeyWrapper: base, reads: &reads})
	for _, key := range []persistence.CatalogKey{f.items[0].Key, {Kind: "Credential", ID: "absent"}} {
		if found, err := s.withResource(context.Background(), key, func(*api.Resource) error { t.Fatal("denied callback"); return nil }); found || !errors.Is(err, errCollectionReadDenied) {
			t.Fatal("denial disclosed lookup distinction", found, err)
		}
	}
	if err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { t.Fatal("denied walk callback"); return nil }); !errors.Is(err, errCollectionReadDenied) {
		t.Fatal("walk denial", err)
	}
	if reads != 0 {
		t.Fatal("denied input was decrypted", reads)
	}
}

func TestCollectionSourceCorruptionAndKeyFailureCloseAdmission(t *testing.T) {
	for _, name := range []string{"payload", "missing-key", "binding"} {
		t.Run(name, func(t *testing.T) {
			f := stagedSourceFixture(t, 1, sourceCredentials(10), nil, func(_ int, item *persistence.CollectionItem) {
				if name == "payload" {
					item.Payload.Ciphertext[0] ^= 1
				}
			})
			s := f.open(t, nil)
			if name == "missing-key" {
				wrong, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{98}, 32))
				f.catalog.sealer, _ = secureconfig.NewSealer(wrong)
			}
			if name == "binding" {
				s.head.UploadID = uuid.NewString()
			}
			err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { t.Fatal("unauthenticated input used"); return nil })
			if !errors.Is(err, ErrUnavailable) || f.catalog.Ready() || s.liveBytes != 0 {
				t.Fatal("crypto failure did not fail closed", err)
			}
		})
	}
}

func TestCollectionSourceCancellationExpiryAndReservations(t *testing.T) {
	f := stagedSourceFixture(t, 2, sourceCredentials(10), nil, nil)
	s := f.open(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	err := s.walk(ctx, func(stagedItemRef, *api.Resource) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || s.liveBytes != 0 || !f.catalog.Ready() {
		t.Fatal("cancellation was not clean", err)
	}
	marker := errors.New("callback error")
	if err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { return marker }); !errors.Is(err, marker) || s.liveBytes != 0 {
		t.Fatal("callback error lost", err)
	}
	release, err := s.reserve(collectionSourcePlaintextBudget)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { t.Fatal("over-budget callback"); return nil }); !errors.Is(err, ErrGraphLimit) {
		t.Fatal("budget not enforced", err)
	}
	release()
	release()
	if s.liveBytes != 0 {
		t.Fatal("release not idempotent")
	}
	err = s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { f.at = f.head.ExpiresAt; return nil })
	if !errors.Is(err, persistence.ErrOperationExpired) || s.liveBytes != 0 {
		t.Fatal("expiry after callback went unnoticed", err)
	}
}

func TestCollectionSourcePrivateFormatting(t *testing.T) {
	f := stagedSourceFixture(t, 1, sourceCredentials(10), nil, nil)
	s := f.open(t, nil)
	for _, value := range []any{s, *s} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(format, value); got != "private encrypted collection source (input omitted)" {
				t.Fatal("unsafe source formatter")
			}
		}
		if raw, err := json.Marshal(value); err == nil || bytes.Contains(raw, s.key[:]) {
			t.Fatal("source was serializable")
		}
	}
	private := errors.New("private-callback-diagnostic")
	positioned := &collectionSourceItemError{ref: stagedItemRef{Ordinal: 1}, cause: private}
	for _, value := range []any{positioned, *positioned} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, value), private.Error()) {
				t.Fatal("callback diagnostic escaped formatter")
			}
		}
	}
	if !errors.Is(positioned, private) {
		t.Fatal("source error lost classified cause")
	}
}

func TestCollectionSourceHeaderAndUnwrapFences(t *testing.T) {
	for _, name := range []string{"version", "short", "missing-key", "header-binding"} {
		t.Run(name, func(t *testing.T) {
			f := stagedSourceFixture(t, 1, sourceCredentials(1), nil, nil, func(c *Catalog, h *persistence.CollectionState) {
				if name == "missing-key" {
					return
				}
				plain := make([]byte, 1+commitment.KeyBytes+commitment.MACBytes)
				plain[0] = collectionSecretVersion
				if name == "version" {
					plain[0]++
				}
				if name == "short" {
					plain = plain[:len(plain)-1]
				}
				binding := h.Binding(c.storeID)
				if name == "header-binding" {
					binding.Revision = strings.Repeat("b", 64)
				}
				var err error
				h.Secret, err = c.sealer.Seal(context.Background(), binding, plain)
				clear(plain)
				if err != nil {
					t.Fatal(err)
				}
			})
			if name == "missing-key" {
				wrong, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{98}, 32))
				f.catalog.sealer, _ = secureconfig.NewSealer(wrong)
			}
			s, err := newCollectionValidationSource(context.Background(), f.catalog, f.head.ID, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at })
			if s != nil || !errors.Is(err, ErrUnavailable) || f.catalog.Ready() {
				t.Fatal("invalid protected header admitted", err)
			}
		})
	}
	for _, name := range []string{"cancel-header", "cancel-item", "expire-header", "expire-item"} {
		t.Run(name, func(t *testing.T) {
			f := stagedSourceFixture(t, 1, sourceCredentials(1), nil, nil)
			var source *collectionValidationSource
			if strings.HasSuffix(name, "item") {
				source = f.open(t, nil)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			f.catalog.sealer, _ = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: base, before: func(context.Context) {
				if strings.HasPrefix(name, "cancel") {
					cancel()
				} else {
					f.at = f.head.ExpiresAt
				}
			}})
			var err error
			if source == nil {
				source, err = newCollectionValidationSource(ctx, f.catalog, f.head.ID, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at })
				if source != nil {
					source.close()
					t.Fatal("expired/canceled constructor returned source")
				}
			} else {
				err = source.walk(ctx, func(stagedItemRef, *api.Resource) error { t.Fatal("late input reached callback"); return nil })
			}
			want := error(context.Canceled)
			if strings.HasPrefix(name, "expire") {
				want = persistence.ErrOperationExpired
			}
			if !errors.Is(err, want) || !f.catalog.Ready() {
				t.Fatal("unwrap fence misclassified or poisoned valid store", err)
			}
			if source != nil && source.liveBytes != 0 {
				t.Fatal("unwrap left reservation")
			}
		})
	}
}

func TestCollectionSourceNestedBorrowAndClose(t *testing.T) {
	f := stagedSourceFixture(t, 2, sourceCredentials(512), nil, nil)
	s := f.open(t, nil)
	_, err := s.withResource(context.Background(), f.items[0].Key, func(first *api.Resource) error {
		before := bytes.Clone(first.Spec)
		defer clear(before)
		found, err := s.withResource(context.Background(), f.items[1].Key, func(*api.Resource) error { return nil })
		if !found || err != nil || !bytes.Equal(first.Spec, before) {
			t.Fatal("nested lookup invalidated outer borrow", err)
		}
		s.close()
		return nil
	})
	if !errors.Is(err, ErrUnavailable) || s.liveBytes != 0 {
		t.Fatal("close in callback missed final fence or leaked reservation", err)
	}
}

func TestCollectionSourceLargeInventoryKeepsScopedPlaintext(t *testing.T) {
	// >64 MiB of authenticated plaintext, generated/encrypted one item at a
	// time. Every resource remains below the approved 1 MiB per-item limit.
	const count, size = 72, 950 << 10
	f := stagedSourceFixture(t, count, sourceCredentials(size), nil, nil)
	s := f.open(t, nil)
	seen := 0
	// Diagnostic samples supplement the logical quota. They include the encrypted
	// in-memory fixture, GC timing and the test process; no heap/RSS limit is asserted.
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	baseline, sampledPeak := memory.HeapAlloc, memory.HeapAlloc
	err := s.walk(context.Background(), func(stagedItemRef, *api.Resource) error {
		seen++
		runtime.ReadMemStats(&memory)
		sampledPeak = max(sampledPeak, memory.HeapAlloc)
		return nil
	})
	if err != nil || seen != count || s.liveBytes != 0 || s.peakBytes > 4<<20 {
		t.Fatal("sequential validation retained aggregate plaintext", seen, s.peakBytes, err)
	}
	t.Logf("validated %d resources exceeding %d plaintext bytes; peak raw/decode reservation %d bytes (not RSS)", count, count*size, s.peakBytes)
	t.Logf("noncontinuous HeapAlloc samples: before walk %d bytes, highest callback sample %d bytes; encrypted memory fixture included", baseline, sampledPeak)
}

func TestCollectionSourceRaftSnapshotAndLaterLogRestart(t *testing.T) {
	store, config := bootstrapTestStore(t, "raft", 1000)
	wrapper, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	sealer, _ := secureconfig.NewSealer(wrapper)
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := stagedSourceFixtureStore(t, catalog, store, 3, sourceCredentials(128), nil, nil)
	before := f.open(t, nil)
	if err := before.walk(context.Background(), func(stagedItemRef, *api.Resource) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before.close()
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	// A later exact accepted retry advances activity in the authoritative log,
	// without replacing ciphertext or changing the original private commitment.
	f.head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &f.items[2]}, f.head.ActivityAt.Add(time.Second))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	recovered, err := NewCatalog(reopened, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	source, err := newCollectionValidationSource(context.Background(), recovered, f.head.ID, func(persistence.CatalogKey) bool { return true }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	seen := 0
	if err := source.walk(context.Background(), func(ref stagedItemRef, r *api.Resource) error {
		if ref.Key != f.items[seen].Key || !bytes.Contains(r.Spec, []byte("private-value-")) {
			t.Fatal("restart changed original resource")
		}
		seen++
		return nil
	}); err != nil || seen != 3 || source.liveBytes != 0 {
		t.Fatal("recovered inventory could not authenticate", seen, err)
	}
	head, found, err := reopened.CollectionGet(f.head.ID)
	if err != nil || !found || !reflect.DeepEqual(head, f.head) {
		t.Fatal("snapshot plus later log changed inventory identity", err)
	}
	items, err := reopened.CollectionPage(f.head.ID, 0, 256)
	if err != nil || !reflect.DeepEqual(items, f.items) {
		t.Fatal("restart re-encrypted or replaced original input", err)
	}
	view, err := reopened.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("restart activated validation input", err)
	}
}
