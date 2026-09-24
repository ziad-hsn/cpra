package management

import (
	"bytes"
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
)

func reselectionNativeOpen(t *testing.T, config runtimeconfig.Config) (*Catalog, *persistence.Store) {
	t.Helper()
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return candidateExecutionCatalog(t, store), store
}

func reselectionNativePrefix(t *testing.T, store *persistence.Store, id string) []persistence.CollectionItem {
	t.Helper()
	rows, err := store.CollectionPage(id, 0, 256)
	if err != nil || len(rows) != 1 || len(rows[0].Payload.Ciphertext) == 0 {
		t.Fatal("expected one actual encrypted prefix row", err)
	}
	return rows
}

// This closes and reopens real local Raft/bbolt state. It establishes native
// disk replay and snapshot recovery; forced-process termination is exercised by
// the separate spool/storage process tests, not simulated by this test.
func TestCollectionReselectionProofNativeRaftReplay(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", snapshot), func(t *testing.T) {
			config := runtimeconfig.Default()
			config.Storage.Directory = t.TempDir()
			catalog, store := reselectionNativeOpen(t, config)
			f := newReselectionProofFixtureStore(t, catalog, store, 1, nil, nil)
			before := reselectionNativePrefix(t, store, f.head.ID)
			nodeID := store.Status().NodeID
			spool, sources := f.stage(t, f.raw)
			old, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if err != nil {
				t.Fatal("proof before restart", err)
			}
			defer old.close()
			if snapshot {
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			called := false
			if err := old.withSuffix(t.Context(), 0, func(CollectionUploadItem) error { called = true; return nil }); err == nil || called {
				t.Fatal("proof from stopped owner remained usable", err)
			}
			old.close()
			if err := spool.Close(); err != nil {
				t.Fatal(err)
			}
			f.catalog, f.store = reselectionNativeOpen(t, config)
			f.at = time.Now().UTC()
			if f.store.Status().NodeID != nodeID {
				t.Fatal("restart changed durable store identity")
			}
			head, found, err := f.store.CollectionGet(f.head.ID)
			if err != nil || !found || !reflect.DeepEqual(head, f.head) {
				t.Fatal("restart changed original collection header", err)
			}
			if actual := reselectionNativePrefix(t, f.store, f.head.ID); !reflect.DeepEqual(actual, before) {
				t.Fatal("restart changed original prefix ciphertext")
			}
			// Reselection gets a fresh disposable source spool and reconstructs
			// proof against the original operation, never a replacement upload.
			spool, sources = f.stage(t, f.raw)
			index := f.store.Status().CommittedIndex
			proof, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if err != nil {
				t.Fatal("fresh reselection proof after native replay", err)
			}
			defer proof.close()
			if proof.head.ID != head.ID || proof.head.UploadID != head.UploadID || !proof.verified || len(proof.suffix) != 1 {
				t.Fatal("reselection did not preserve original operation")
			}
			if err := proof.withSuffix(t.Context(), 0, func(row CollectionUploadItem) error {
				if !reflect.DeepEqual(row, f.items[1]) {
					t.Error("replayed original suffix changed")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			after, found, err := f.store.CollectionGet(head.ID)
			if err != nil || !found || !reflect.DeepEqual(after, head) || f.store.Status().CommittedIndex != index {
				t.Fatal("proof wrote durable collection state", err)
			}
			if actual := reselectionNativePrefix(t, f.store, head.ID); !reflect.DeepEqual(actual, before) {
				t.Fatal("proof replaced original uploaded ciphertext")
			}
			active, err := f.store.CatalogSnapshot()
			if err != nil || active.Len() != 0 {
				t.Fatal("reselection activated configuration", err)
			}
		})
	}
}

func TestCollectionReselectionProofNativeStoppedPolicyReplacement(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoked=%t", revoke), func(t *testing.T) {
			config := runtimeconfig.Default()
			config.Storage.Directory = t.TempDir()
			catalog, store := reselectionNativeOpen(t, config)
			f := newReselectionProofFixtureStore(t, catalog, store, 1, nil, nil)
			before := reselectionNativePrefix(t, store, f.head.ID)
			spool, sources := f.stage(t, f.raw)
			old, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer old.close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			admin, err := persistence.OpenAdministrative(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = admin.Close() })
			policy, err := admin.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			replace := persistence.AuthenticationCommand{
				Mode: "replace", Epoch: policy.Epoch, ExpectedEpoch: policy.Epoch,
				ExpectedRevision: policy.Revision, Revision: uuid.NewString(),
				Actor: "local-administrator", At: time.Now().UTC(), Principals: policy.Clone().Principals,
			}
			for i := range replace.Principals {
				if replace.Principals[i].ID == "team/operator" {
					replace.Principals[i].Revoked = revoke
					replace.Principals[i].TokenSHA256 = strings.Repeat("56", 32)
				}
			}
			current, err := admin.CommitAuthentication(t.Context(), replace)
			if err != nil || current.Revision == policy.Revision {
				t.Fatal("stopped policy replacement did not commit", err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			f.catalog, f.store = reselectionNativeOpen(t, config)
			f.at = time.Now().UTC()
			called := false
			if err := old.withSuffix(t.Context(), 0, func(CollectionUploadItem) error { called = true; return nil }); err == nil || called {
				t.Fatal("old proof survived stopped authority replacement", err)
			}
			if err := spool.Close(); err != nil {
				t.Fatal(err)
			}
			// Count real local-key unwraps; transport and encryption stay on
			// their production implementations. A denied owner must stop first.
			wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			unwraps := 0
			f.catalog.sealer, err = secureconfig.NewSealer(sourceCountingWrapper{KeyWrapper: wrapper, reads: &unwraps})
			if err != nil {
				t.Fatal(err)
			}
			spool, sources = f.stage(t, f.raw)
			index := f.store.Status().CommittedIndex
			fresh, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
			if revoke {
				if fresh != nil || !errors.Is(err, persistence.ErrOperatorAuthorityDenied) || unwraps != 0 {
					t.Fatal("revoked actor reached unwrap or obtained proof", unwraps, err)
				}
			} else {
				if err != nil {
					t.Fatal("current authorized actor could not reselect original upload", err)
				}
				defer fresh.close()
				if fresh.authority.Revision != current.Revision || fresh.authority.Revision == old.authority.Revision || unwraps == 0 {
					t.Fatal("fresh proof reused stale authority or skipped original identity decryption")
				}
			}
			if f.store.Status().CommittedIndex != index || !reflect.DeepEqual(before, reselectionNativePrefix(t, f.store, f.head.ID)) {
				t.Fatal("authority check modified original collection")
			}
			if !f.catalog.Ready() {
				t.Fatal("ordinary current-authority outcome poisoned catalog")
			}
		})
	}
}
