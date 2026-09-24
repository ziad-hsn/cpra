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
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func batchUploadFixture(t *testing.T, sizes []int, batch int, quota int64) *reselectionProofFixture {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	if batch > 0 {
		cfg.Storage.BatchSize = batch
	}
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	wrapper, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	sealer, _ := secureconfig.NewSealer(wrapper)
	c, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := &reselectionProofFixture{catalog: c, store: store, at: time.Now().UTC()}
	f.policy, err = store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(f.at.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{49}, commitment.KeyBytes)
	defer clear(key)
	fingerprint := [commitment.MACBytes]byte{11, 19}
	acc, err := commitment.NewAccumulator(key, uint64(len(sizes)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	for n, size := range sizes {
		raw, err := json.Marshal(resource("Credential", fmt.Sprintf("secret-%03d", n), api.CredentialSpec{Value: api.Pointer(strings.Repeat("v", size))}))
		if err != nil {
			t.Fatal(err)
		}
		row := CollectionUploadItem{Ordinal: uint64(n + 1), Key: persistence.CatalogKey{Kind: "Credential", ID: fmt.Sprintf("secret-%03d", n)}, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: uint64(n + 1), Resource: raw}
		position := commitment.Position{Ordinal: row.Ordinal, ID: row.Key.Kind + "/" + row.Key.ID, Source: commitment.SourcePosition{Token: row.Source, Document: row.SourceDocument, Item: row.SourceItem}}
		mac, err := commitment.ItemMAC(key, position, raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = acc.Add(position, mac); err != nil {
			t.Fatal(err)
		}
		row.ContentDigest = hex.EncodeToString(mac[:])
		f.items = append(f.items, row)
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.ObserveCollectionOwner(t.Context(), "team/operator", f.at)
	if err != nil {
		t.Fatal(err)
	}
	if quota == 0 {
		quota = 16 << 20
	}
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "team/operator", Owner: owner, IdentityFormat: commitment.Format, ContentDigest: hex.EncodeToString(digest[:]), ItemCount: uint64(len(sizes)), MaxEncodedBytes: quota, ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading", CreatedAt: f.at, ActivityAt: f.at, ExpiresAt: f.at.Add(persistence.CollectionInactivityLifetime)}
	head.Secret, err = sealCollectionIdentity(t.Context(), c.sealer, head, c.storeID, key, fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	f.head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}, f.at)
	t.Cleanup(func() {
		for _, item := range f.items {
			clear(item.Resource)
		}
	})
	return f
}
func batchUpload(t *testing.T, f *reselectionProofFixture, items []CollectionUploadItem, admit CollectionCommit) (api.Operation, error) {
	t.Helper()
	return f.catalog.UploadCollection(t.Context(), f.head.ID, "team/operator", items, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at }, admit)
}
func batchHead(t *testing.T, f *reselectionProofFixture) persistence.CollectionState {
	t.Helper()
	h, ok, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	return h
}
func TestCollectionUploadBatchBounds(t *testing.T) {
	for _, tc := range []struct {
		name           string
		sizes          []int
		limit, batches int
	}{
		{"max-chunk", make([]int, 256), 0, 1},
		{"configured-count", []int{10, 10, 10, 10, 10}, 2, 3},
		{"encoded-bytes", []int{900000, 900000, 900000, 900000}, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := batchUploadFixture(t, tc.sizes, tc.limit, 0)
			before := f.store.Status().CommittedIndex
			calls := 0
			op, err := batchUpload(t, f, f.items, func(commit func() error) error { calls++; return commit() })
			if err != nil || op.Uploaded == nil || *op.Uploaded != int64(len(tc.sizes)) || calls != tc.batches || f.store.Status().CommittedIndex-before != uint64(calls) {
				t.Fatalf("batch admission mismatch err=%v calls=%d index_delta=%d", err, calls, f.store.Status().CommittedIndex-before)
			}
			if snapshot, err := f.store.CatalogSnapshot(); err != nil || snapshot.Len() != 0 {
				t.Fatal("upload activated configuration")
			}
		})
	}
}
func TestCollectionUploadBatchMixedRetryPreservesCiphertext(t *testing.T) {
	f := batchUploadFixture(t, []int{8, 8, 8}, 0, 0)
	if _, err := batchUpload(t, f, f.items[:1], allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.CollectionPage(f.head.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	fresh, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{24}, 32))
	f.catalog.sealer, _ = secureconfig.NewSealer(fresh, old)
	calls := 0
	if _, err := batchUpload(t, f, f.items, func(commit func() error) error { calls++; return commit() }); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.CollectionPage(f.head.ID, 0, 3)
	if err != nil || calls != 1 || len(rows) != 3 || !reflect.DeepEqual(rows[0], before[0]) || rows[1].Payload.KeyID != fresh.ID() {
		t.Fatal("retry changed ciphertext or split unexpectedly", err, calls)
	}
	beforeIndex := f.store.Status().CommittedIndex
	if _, err := batchUpload(t, f, f.items, allowCollectionCommit); err != nil || f.store.Status().CommittedIndex != beforeIndex+1 {
		t.Fatal("retry-only complete prefix failed", err)
	}
}
func TestCollectionUploadBatchRejectsPendingDuplicateBeforeAdmission(t *testing.T) {
	f := batchUploadFixture(t, []int{8, 8}, 0, 0)
	f.items[1].Key = f.items[0].Key
	before := f.store.Status().CommittedIndex
	calls := 0
	_, err := batchUpload(t, f, f.items, func(commit func() error) error { calls++; return commit() })
	if !errors.Is(err, persistence.ErrCollectionConflict) || calls != 0 || f.store.Status().CommittedIndex != before {
		t.Fatal("duplicate admitted", err, calls)
	}
}

func TestCollectionUploadBatchCorruptLaterInputDoesNotAdmitPreparedRows(t *testing.T) {
	f := batchUploadFixture(t, []int{8, 8}, 0, 0)
	f.items[1].Resource = append(f.items[1].Resource, ' ')
	before := f.store.Status().CommittedIndex
	calls := 0
	_, err := batchUpload(t, f, f.items, func(commit func() error) error { calls++; return commit() })
	if !errors.Is(err, ErrValidation) || calls != 0 || f.store.Status().CommittedIndex != before || batchHead(t, f).Uploaded != 0 {
		t.Fatal("corrupt later input admitted a prepared row", err, calls)
	}
}
func TestCollectionUploadBatchPartialQuotaAndLostReply(t *testing.T) {
	t.Run("later-quota", func(t *testing.T) {
		f := batchUploadFixture(t, []int{8, 4000, 8}, 0, 1500)
		calls := 0
		op, err := batchUpload(t, f, f.items, func(commit func() error) error { calls++; return commit() })
		if !errors.Is(err, persistence.ErrCollectionQuota) || op.ID != f.head.ID || calls != 1 || batchHead(t, f).Uploaded != 1 {
			t.Fatal("partial outcome hidden or later row crossed rejected prefix", err, calls, batchHead(t, f).Uploaded)
		}
	})
	t.Run("lost-reply", func(t *testing.T) {
		f := batchUploadFixture(t, []int{8, 8, 8}, 0, 0)
		calls := 0
		op, err := batchUpload(t, f, f.items, func(commit func() error) error {
			calls++
			if err := commit(); err != nil {
				return err
			}
			return errors.New("reply lost")
		})
		if !errors.Is(err, ErrOutcomeUnconfirmed) || op.ID != f.head.ID || calls != 1 || batchHead(t, f).Uploaded != 3 {
			t.Fatal("lost reply silently retried or lost handle", err, calls)
		}
	})
}
func TestCollectionUploadBatchAdmissionFences(t *testing.T) {
	for _, mode := range []string{"context", "authority-expiry", "upload-expiry", "cancel", "permission", "no-callback"} {
		t.Run(mode, func(t *testing.T) {
			f := batchUploadFixture(t, []int{8, 8}, 0, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			allowed := true
			_, err := f.catalog.UploadCollection(ctx, f.head.ID, "team/operator", f.items, func(persistence.CatalogKey) bool { return allowed }, func() time.Time { return f.at }, func(commit func() error) error {
				switch mode {
				case "context":
					cancel()
				case "authority-expiry":
					f.at = f.policy.Principals[0].ExpiresAt
				case "upload-expiry":
					f.at = f.head.ExpiresAt
				case "permission":
					allowed = false
				case "no-callback":
					return nil
				case "cancel":
					if _, err := f.catalog.CancelCollection(t.Context(), f.head.ID, "team/operator", func() time.Time { return f.at }, allowCollectionCommit); err != nil {
						t.Fatal(err)
					}
				}
				return commit()
			})
			if err == nil || batchHead(t, f).Uploaded != 0 {
				t.Fatal("stale admission advanced prefix", err)
			}
		})
	}
}
func TestCollectionUploadBatchFSMRejectsConcurrentDifferentCiphertext(t *testing.T) {
	f := batchUploadFixture(t, []int{8, 8, 8}, 0, 0)
	inside := false
	injected := false
	_, err := f.catalog.UploadCollection(t.Context(), f.head.ID, "team/operator", f.items, func(persistence.CatalogKey) bool {
		if inside && !injected {
			injected = true
			if _, err := batchUpload(t, f, f.items[:1], allowCollectionCommit); err != nil {
				t.Fatal(err)
			}
		}
		return true
	}, func() time.Time { return f.at }, func(commit func() error) error { inside = true; return commit() })
	if !errors.Is(err, persistence.ErrCollectionConflict) || !injected || batchHead(t, f).Uploaded != 1 {
		t.Fatal("dependent suffix appended after changed ciphertext", err, batchHead(t, f).Uploaded)
	}
}
func TestCollectionUploadBatchLegacyOwnerlessRemainsSerial(t *testing.T) {
	f := uploadFixture(t, 3, 0)
	items := make([]CollectionUploadItem, 0, 3)
	for _, in := range f.input {
		items = append(items, CollectionUploadItem{Ordinal: in.Ref.Ordinal, Key: in.Ref.Key, Source: in.Ref.Source, SourceDocument: in.Ref.Document, SourceItem: in.Ref.Item, ContentDigest: in.ContentDigest, Resource: in.Resource})
	}
	calls := 0
	_, err := f.catalog.UploadCollection(t.Context(), f.head.ID, f.head.Actor, items, func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at }, func(commit func() error) error { calls++; return commit() })
	if err != nil || calls != 3 {
		t.Fatal("historical ownerless admission changed", err, calls)
	}
}
