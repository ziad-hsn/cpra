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
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type uploadPreparationFixture struct {
	collectionSourceFixture
	input []collectionUploadInput
}

func uploadFixture(t *testing.T, count, uploaded int) *uploadPreparationFixture {
	t.Helper()
	c, store := testCatalog(t)
	at := time.Now().UTC()
	key := bytes.Repeat([]byte{41}, commitment.KeyBytes)
	defer clear(key)
	fingerprint := [commitment.MACBytes]byte{73, 11}
	acc, err := commitment.NewAccumulator(key, uint64(count), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	f := &uploadPreparationFixture{collectionSourceFixture: collectionSourceFixture{catalog: c, store: store, at: at}, input: make([]collectionUploadInput, count)}
	for i := range f.input {
		identity, raw := sourceCredentials(40)(i)
		row := persistence.CollectionItem{Ordinal: uint64(i + 1), Key: identity, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: uint64(i + 1)}
		mac, err := commitment.ItemMAC(key, itemPosition(row), raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := acc.Add(itemPosition(row), mac); err != nil {
			t.Fatal(err)
		}
		f.input[i] = collectionUploadInput{Ref: stagedRef(row), ContentDigest: hex.EncodeToString(mac[:]), Resource: raw}
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "test/operator", IdentityFormat: commitment.Format,
		ContentDigest: hex.EncodeToString(digest[:]), ItemCount: uint64(count), MaxEncodedBytes: 16 << 20, ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading", CreatedAt: at, ActivityAt: at, ExpiresAt: at.Add(persistence.CollectionInactivityLifetime)}
	head.Secret, err = sealCollectionIdentity(context.Background(), c.sealer, head, c.storeID, key, fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	f.head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}, at)
	for i := 0; i < uploaded; i++ {
		input := f.input[i]
		row := persistence.CollectionItem{Ordinal: input.Ref.Ordinal, Key: input.Ref.Key, Source: input.Ref.Source, SourceDocument: input.Ref.Document, SourceItem: input.Ref.Item, ContentDigest: input.ContentDigest}
		row.Payload, err = c.sealer.Seal(context.Background(), row.Binding(c.storeID, head.UploadID), input.Resource)
		if err != nil {
			t.Fatal(err)
		}
		f.at = f.at.Add(time.Millisecond)
		f.head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &row}, f.at)
	}
	t.Cleanup(func() {
		for _, input := range f.input {
			clear(input.Resource)
		}
	})
	return f
}

func (f *uploadPreparationFixture) openUpload(t *testing.T, allow func(persistence.CatalogKey) bool) *collectionUploadPreparation {
	t.Helper()
	if allow == nil {
		allow = func(persistence.CatalogKey) bool { return true }
	}
	p, err := newCollectionUploadPreparation(context.Background(), f.catalog, f.head.ID, allow, func() time.Time { return f.at })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func resignUploadInput(t *testing.T, input *collectionUploadInput) {
	t.Helper()
	key := bytes.Repeat([]byte{41}, commitment.KeyBytes)
	defer clear(key)
	ref := input.Ref
	mac, err := commitment.ItemMAC(key, commitment.Position{Ordinal: ref.Ordinal, ID: ref.Key.Kind + "/" + ref.Key.ID, Source: commitment.SourcePosition{Token: ref.Source, Document: ref.Document, Item: ref.Item}}, input.Resource)
	if err != nil {
		t.Fatal(err)
	}
	input.ContentDigest = hex.EncodeToString(mac[:])
}

type uploadObservedWrapper struct {
	secureconfig.KeyWrapper
	wraps, unwraps int
	beforeWrap     func(context.Context)
}

func (w *uploadObservedWrapper) Wrap(ctx context.Context, payload, aad []byte) ([]byte, error) {
	w.wraps++
	if w.beforeWrap != nil {
		w.beforeWrap(ctx)
	}
	return w.KeyWrapper.Wrap(ctx, payload, aad)
}
func (w *uploadObservedWrapper) Unwrap(ctx context.Context, payload, aad []byte) ([]byte, error) {
	w.unwraps++
	return w.KeyWrapper.Unwrap(ctx, payload, aad)
}

func TestCollectionUploadPreparationExactBytesAndRetry(t *testing.T) {
	f := uploadFixture(t, 2, 0)
	p := f.openUpload(t, nil)
	before, _ := f.store.CatalogSnapshot()
	input := f.input[0]
	// A different byte spelling must be committed verbatim, not decoded and
	// re-encoded. The whole-inventory MAC is deliberately not checked here.
	input.Resource = bytes.ReplaceAll(bytes.Clone(input.Resource), []byte(`"value"`), []byte(`"v\u0061lue"`))
	input.Resource = append([]byte(" \n"), input.Resource...)
	resignUploadInput(t, &input)
	defer clear(input.Resource)
	row, retry, err := p.prepare(context.Background(), input)
	if err != nil || retry {
		t.Fatal("prepare failed", err)
	}
	plain, err := f.catalog.sealer.Open(context.Background(), row.Binding(f.catalog.storeID, f.head.UploadID), row.Payload)
	if err != nil || !bytes.Equal(plain, input.Resource) {
		t.Fatal("exact source bytes changed", err)
	}
	clear(plain)
	raw, _ := json.Marshal(row)
	if bytes.Contains(raw, []byte("private-value-")) {
		t.Fatal("plaintext in durable row")
	}
	after, _ := f.store.CatalogSnapshot()
	head, _, _ := f.store.CollectionGet(f.head.ID)
	if after.Index != before.Index || after.Len() != 0 || !reflect.DeepEqual(head, f.head) {
		t.Fatal("preparation committed state")
	}
	f.at = f.at.Add(time.Millisecond)
	f.head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &row}, f.at)
	if _, _, err := p.prepare(context.Background(), input); !errors.Is(err, persistence.ErrCollectionConflict) {
		t.Fatal("stale captured prefix accepted", err)
	}
	p.close()
	p = f.openUpload(t, nil)
	old, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	fresh, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{24}, 32))
	oldObserved, newObserved := &uploadObservedWrapper{KeyWrapper: old}, &uploadObservedWrapper{KeyWrapper: fresh}
	f.catalog.sealer, _ = secureconfig.NewSealer(newObserved, oldObserved)
	got, retry, err := p.prepare(context.Background(), input)
	if err != nil || !retry || !reflect.DeepEqual(got, row) || oldObserved.wraps != 0 || newObserved.wraps != 0 || oldObserved.unwraps != 1 {
		t.Fatal("retry re-sealed or changed ciphertext", err)
	}
	newRow, retry, err := p.prepare(context.Background(), f.input[1])
	if err != nil || retry || newObserved.wraps != 1 || newRow.Payload.KeyID != fresh.ID() {
		t.Fatal("new row did not use active key", err)
	}
	head, _, _ = f.store.CollectionGet(f.head.ID)
	if !reflect.DeepEqual(head, f.head) {
		t.Fatal("read/preparation renewed upload")
	}
}

func TestCollectionUploadPreparationRejectsInputBeforeSealing(t *testing.T) {
	for _, name := range []string{"bytes", "mac-case", "schema", "identity", "duplicate", "skip", "oversize", "source"} {
		t.Run(name, func(t *testing.T) {
			f := uploadFixture(t, 3, 1)
			p := f.openUpload(t, nil)
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			observed := &uploadObservedWrapper{KeyWrapper: base}
			f.catalog.sealer, _ = secureconfig.NewSealer(observed)
			input := f.input[1]
			input.Resource = bytes.Clone(input.Resource)
			defer clear(input.Resource)
			switch name {
			case "bytes":
				input.Resource = append(input.Resource, ' ')
			case "mac-case":
				input.ContentDigest = strings.ToUpper(input.ContentDigest)
			case "schema":
				input.Resource = []byte(`{"kind":"Credential"}`)
				resignUploadInput(t, &input)
			case "identity":
				input.Ref.Key.ID = "different"
				resignUploadInput(t, &input)
			case "duplicate":
				input.Ref.Key = f.input[0].Ref.Key
				input.Resource = bytes.Clone(f.input[0].Resource)
				resignUploadInput(t, &input)
			case "skip":
				input = f.input[2]
			case "oversize":
				input.Resource = bytes.Repeat([]byte{'x'}, commitment.MaxResourceBytes+1)
			case "source":
				input.Ref.Source = "file-secret.yaml"
			}
			row, retry, err := p.prepare(context.Background(), input)
			if err == nil || retry || row.Ordinal != 0 || observed.wraps != 0 || observed.unwraps != 0 {
				t.Fatal("invalid input prepared or read stored secrets", err)
			}
			if strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "file-secret") {
				t.Fatal("unsafe diagnostic")
			}
		})
	}
}

func TestCollectionUploadPreparationDeniedExistingAndAbsent(t *testing.T) {
	f := uploadFixture(t, 2, 1)
	p := f.openUpload(t, func(persistence.CatalogKey) bool { return false })
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	observed := &uploadObservedWrapper{KeyWrapper: base}
	f.catalog.sealer, _ = secureconfig.NewSealer(observed)
	for _, input := range f.input {
		row, retry, err := p.prepare(context.Background(), input)
		if !errors.Is(err, errCollectionReadDenied) || row.Ordinal != 0 || retry {
			t.Fatal("denial differs by existence", err)
		}
	}
	if observed.wraps != 0 || observed.unwraps != 0 {
		t.Fatal("denied resource accessed wrapping service")
	}
}

func TestCollectionUploadPreparationExpiryAndCancellationAfterSeal(t *testing.T) {
	for _, name := range []string{"expiry", "context"} {
		t.Run(name, func(t *testing.T) {
			f := uploadFixture(t, 1, 0)
			p := f.openUpload(t, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			observed := &uploadObservedWrapper{KeyWrapper: base, beforeWrap: func(context.Context) {
				if name == "expiry" {
					f.at = f.head.ExpiresAt
				} else {
					cancel()
				}
			}}
			f.catalog.sealer, _ = secureconfig.NewSealer(observed)
			row, retry, err := p.prepare(ctx, f.input[0])
			if row.Ordinal != 0 || retry || err == nil {
				t.Fatal("stale sealed row returned", err)
			}
			if name == "context" && !errors.Is(err, context.Canceled) {
				t.Fatal("context identity lost", err)
			}
			if name == "expiry" && !errors.Is(err, persistence.ErrOperationExpired) {
				t.Fatal("expiry identity lost", err)
			}
			head, _, _ := f.store.CollectionGet(f.head.ID)
			if head.Uploaded != 0 || !f.catalog.Ready() {
				t.Fatal("stale preparation mutated or poisoned store")
			}
		})
	}
}

func TestCollectionUploadPreparationPrivateFormattingAndClose(t *testing.T) {
	f := uploadFixture(t, 1, 0)
	p := f.openUpload(t, nil)
	for _, value := range []any{p, *p, f.input[0], &f.input[0]} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			out := fmt.Sprintf(format, value)
			if strings.Contains(out, "41 41") || strings.Contains(out, "private-value") || strings.Contains(out, "credential-0000") {
				t.Fatal("private preparation formatted", out)
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private preparation serialized")
		}
	}
	p.close()
	p.close()
	if p.key != [commitment.KeyBytes]byte{} || p.catalog != nil || p.view != nil || p.now != nil || p.canWrite != nil {
		t.Fatal("close retained private state")
	}
	if row, retry, err := p.prepare(context.Background(), f.input[0]); !errors.Is(err, ErrUnavailable) || row.Ordinal != 0 || retry {
		t.Fatal("closed preparation accepted", err)
	}
}

func TestCollectionUploadPreparationConcurrentAppendDuringSeal(t *testing.T) {
	f := uploadFixture(t, 2, 0)
	p := f.openUpload(t, nil)
	input := f.input[0]
	committed := persistence.CollectionItem{Ordinal: input.Ref.Ordinal, Key: input.Ref.Key, Source: input.Ref.Source,
		SourceDocument: input.Ref.Document, SourceItem: input.Ref.Item, ContentDigest: input.ContentDigest}
	var err error
	committed.Payload, err = f.catalog.sealer.Seal(context.Background(), committed.Binding(f.catalog.storeID, f.head.UploadID), input.Resource)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	observed := &uploadObservedWrapper{KeyWrapper: base, beforeWrap: func(context.Context) {
		f.at = f.at.Add(time.Millisecond)
		f.head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &committed}, f.at)
	}}
	f.catalog.sealer, _ = secureconfig.NewSealer(observed)
	row, retry, err := p.prepare(context.Background(), input)
	if !errors.Is(err, persistence.ErrCollectionConflict) || retry || row.Ordinal != 0 {
		t.Fatal("concurrent prefix advance returned a stale prepared row", err)
	}
	page, err := f.store.CollectionPage(f.head.ID, 0, 1)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], committed) {
		t.Fatal("concurrent committed ciphertext changed", err)
	}
}

func TestCollectionUploadPreparationHeaderUnwrapFences(t *testing.T) {
	for _, name := range []string{"expiry", "context", "wrong-key"} {
		t.Run(name, func(t *testing.T) {
			f := uploadFixture(t, 1, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			keyByte := byte(23)
			if name == "wrong-key" {
				keyByte = 77
			}
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{keyByte}, 32))
			f.catalog.sealer, _ = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: base, before: func(context.Context) {
				if name == "expiry" {
					f.at = f.head.ExpiresAt
				}
				if name == "context" {
					cancel()
				}
			}})
			p, err := newCollectionUploadPreparation(ctx, f.catalog, f.head.ID, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at })
			if p != nil || err == nil {
				t.Fatal("invalid header preparation accepted", err)
			}
			if name == "context" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation identity lost", err)
			}
			if name == "expiry" && !errors.Is(err, persistence.ErrOperationExpired) {
				t.Fatal("expiry identity lost", err)
			}
			if (name == "wrong-key") == f.catalog.Ready() {
				t.Fatal("wrong-key failure and ordinary interrupted reads were not distinguished")
			}
		})
	}
}

func TestCollectionUploadPreparationCommittedPayloadAuthentication(t *testing.T) {
	f := stagedSourceFixture(t, 1, sourceCredentials(40), nil, func(_ int, item *persistence.CollectionItem) { item.Payload.Ciphertext[0] ^= 1 })
	p, err := newCollectionUploadPreparation(context.Background(), f.catalog, f.head.ID, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at })
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	_, raw := sourceCredentials(40)(0)
	defer clear(raw)
	input := collectionUploadInput{Ref: stagedRef(f.items[0]), ContentDigest: f.items[0].ContentDigest, Resource: raw}
	row, retry, err := p.prepare(context.Background(), input)
	if !errors.Is(err, ErrUnavailable) || row.Ordinal != 0 || retry || f.catalog.Ready() {
		t.Fatal("corrupted committed payload treated as a successful retry", err)
	}
}

func TestCollectionUploadPreparationByteLimit(t *testing.T) {
	f := uploadFixture(t, 1, 0)
	p := f.openUpload(t, nil)
	input := f.input[0]
	// JSON permits surrounding whitespace. This exercises the precise byte
	// ceiling without inventing an unbounded string field in the schema.
	input.Resource = append(bytes.Clone(input.Resource), bytes.Repeat([]byte{' '}, commitment.MaxResourceBytes-len(input.Resource))...)
	defer clear(input.Resource)
	resignUploadInput(t, &input)
	row, retry, err := p.prepare(context.Background(), input)
	if err != nil || retry || len(row.Payload.Ciphertext) != commitment.MaxResourceBytes+16 {
		t.Fatal("maximum legal resource failed", err)
	}
	if len(input.Resource) != commitment.MaxResourceBytes {
		t.Fatal("bad boundary fixture")
	}
}

func TestCollectionUploadPreparationCommittedCancellationDuringSeal(t *testing.T) {
	f := uploadFixture(t, 1, 0)
	p := f.openUpload(t, nil)
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	observed := &uploadObservedWrapper{KeyWrapper: base, beforeWrap: func(context.Context) {
		f.at = f.at.Add(time.Millisecond)
		cancel := persistence.CollectionCancellation{ID: uuid.NewString(), Actor: f.head.Actor, At: f.at}
		f.head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "cancel", OperationID: f.head.ID, UploadID: f.head.UploadID, Cancel: &cancel}, f.at)
	}}
	f.catalog.sealer, _ = secureconfig.NewSealer(observed)
	row, retry, err := p.prepare(context.Background(), f.input[0])
	if !errors.Is(err, persistence.ErrOperationExpired) || retry || row.Ordinal != 0 {
		t.Fatal("canceled upload returned prepared ciphertext", err)
	}
	if f.head.Uploaded != 0 || f.head.Phase != "canceled" || !f.catalog.Ready() {
		t.Fatal("cancellation activated input or poisoned unrelated catalog")
	}
}
