package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func collectionOwnerInput(t *testing.T) (api.CollectionPrepareRequest, CollectionUploadItem) {
	t.Helper()
	key := bytes.Repeat([]byte{0xab}, commitment.KeyBytes)
	defer clear(key)
	var fingerprint [commitment.MACBytes]byte
	copy(fingerprint[:], bytes.Repeat([]byte{0xcd}, commitment.MACBytes))
	raw, err := json.Marshal(resource("Credential", "oncall-secret", api.CredentialSpec{Value: api.Pointer("PRIVATE-OWNER-CANARY")}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(raw) })
	item := CollectionUploadItem{Ordinal: 1, Key: persistence.CatalogKey{Kind: "Credential", ID: "oncall-secret"},
		Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: 1, Resource: raw}
	position := commitment.Position{Ordinal: item.Ordinal, ID: item.Key.Kind + "/" + item.Key.ID,
		Source: commitment.SourcePosition{Token: item.Source, Document: item.SourceDocument, Item: item.SourceItem}}
	mac, err := commitment.ItemMAC(key, position, raw)
	if err != nil {
		t.Fatal(err)
	}
	item.ContentDigest = hex.EncodeToString(mac[:])
	acc, err := commitment.NewAccumulator(key, 1, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if err := acc.Add(position, mac); err != nil {
		t.Fatal(err)
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(commitment.Format),
		IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])),
		ContentDigest: hex.EncodeToString(digest[:]), ItemCount: 1}, item
}

func collectionOwnerBootstrap(at time.Time) persistence.AuthenticationCommand {
	return persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(),
		Actor: "local-administrator", At: at, Principals: []persistence.AuthenticationPrincipal{
			{ID: "team/operator", Role: "operator", TokenSHA256: strings.Repeat("12", 32), ExpiresAt: at.Add(time.Hour)},
		}}
}

func TestCollectionOwnerManagementAdmissionPreservesNamedOrLegacyIdentity(t *testing.T) {
	for _, policy := range []string{"absent", "historical", "named-operator"} {
		t.Run(policy, func(t *testing.T) {
			catalog, store := testCatalog(t)
			ctx := context.Background()
			at := time.Now().UTC()
			command := collectionOwnerBootstrap(at)
			var authority persistence.AuthenticationState
			switch policy {
			case "named-operator":
				// This memory fixture has no running application. Production
				// stopped administration is exercised by the process-crash suite.
				var err error
				authority, err = store.CommitAuthentication(ctx, command)
				if err != nil || authority.Version != persistence.AuthenticationLifecycleFormatVersion {
					t.Fatal("provision current named policy", err)
				}
			case "historical":
				// Preserve an actual version-zero historical command's meaning.
				// New CommitAuthentication calls select version two and cannot
				// be used to manufacture this legacy fixture.
				results, err := store.Submit(ctx, []persistence.Command{{Kind: "authentication", At: at, Authentication: &command}})
				if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Authentication == nil ||
					results[0].Authentication.Version != persistence.AuthenticationFormatVersion {
					t.Fatal("provision historical policy fixture", err)
				}
			}
			request, item := collectionOwnerInput(t)
			admission, err := catalog.PrepareCollection(ctx, request, "team/operator", collectionClock(at.Add(time.Second)), allowCollectionCommit)
			if err != nil {
				t.Fatal(err)
			}
			wire := collectionCreateRequest(request, admission.Ticket)
			operation, err := catalog.CreateCollection(ctx, wire, "team/operator", collectionClock(at.Add(2*time.Second)), allowCollectionCommit)
			if err != nil || operation.ID == "" {
				t.Fatal("management creation failed", err)
			}
			head, exists, err := store.CollectionGet(operation.ID)
			if err != nil || !exists {
				t.Fatal(err)
			}
			if policy == "named-operator" {
				want := persistence.OperatorAuthority{Epoch: authority.Epoch, Revision: authority.Revision, Actor: "team/operator"}
				if head.Owner == nil || *head.Owner != want {
					t.Fatal("create did not capture the committed named identity")
				}
			} else if head.Owner != nil {
				t.Fatal("absent/historical authority was promoted to background ownership")
			}
			if policy == "absent" {
				// A newly provisioned textual identity must not retroactively
				// acquire the already-created, ownerless upload on lost-reply retry.
				command.At = at.Add(3 * time.Second)
				if _, err := store.CommitAuthentication(ctx, command); err != nil {
					t.Fatal(err)
				}
			}
			retry, err := catalog.CreateCollection(ctx, wire, "team/operator", collectionClock(at.Add(4*time.Second)), allowCollectionCommit)
			afterRetry, _, readErr := store.CollectionGet(operation.ID)
			if err != nil || readErr != nil || retry.ID != operation.ID || !reflect.DeepEqual(afterRetry, head) {
				t.Fatal("creation reconciliation replaced ownership or input identity", err, readErr)
			}
			uploaded, err := catalog.UploadCollection(ctx, operation.ID, "team/operator", []CollectionUploadItem{item},
				func(key persistence.CatalogKey) bool { return key == item.Key }, collectionClock(at.Add(5*time.Second)), allowCollectionCommit)
			if err != nil || uploaded.Uploaded == nil || *uploaded.Uploaded != 1 {
				t.Fatal("inactive upload write path stopped working", err)
			}
			stored, exists, err := store.CollectionGet(operation.ID)
			if err != nil || !exists || !reflect.DeepEqual(stored.Owner, head.Owner) || stored.Uploaded != 1 {
				t.Fatal("upload changed original ownership", err)
			}
			receipt, err := store.CollectionReceipt(ctx, operation.ID, at.Add(6*time.Second))
			if err != nil || receipt.ID != operation.ID || receipt.Uploaded != 1 || receipt.Phase != "uploading" {
				t.Fatal("inactive operation observation became unavailable", err)
			}
			source, err := newCollectionValidationSource(ctx, catalog, operation.ID, func(key persistence.CatalogKey) bool { return key == item.Key }, collectionClock(at.Add(6*time.Second)))
			if err != nil {
				t.Fatal("read-only staged source became unavailable", err)
			}
			source.close()
			view, err := store.CatalogSnapshot()
			if err != nil || view.Len() != 0 || !catalog.Ready() {
				t.Fatal("ownership capture activated input or stopped the catalog", err)
			}
			if policy == "named-operator" {
				denied, err := catalog.PrepareCollection(ctx, request, "missing-principal", collectionClock(at.Add(7*time.Second)), allowCollectionCommit)
				if err != nil {
					t.Fatal(err)
				}
				got, err := catalog.CreateCollection(ctx, collectionCreateRequest(request, denied.Ticket), "missing-principal",
					collectionClock(at.Add(8*time.Second)), allowCollectionCommit)
				if !errors.Is(err, persistence.ErrOperatorAuthorityDenied) || got.ID != "" {
					t.Fatal("missing named principal acquired an upload", err)
				}
			}
		})
	}
}
