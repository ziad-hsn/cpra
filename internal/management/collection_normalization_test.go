package management

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestCollectionAdmissionLegacyIdentityVector(t *testing.T) {
	key, source, digest, err := collectionRequestIdentity(collectionAdmissionRequest(), "fixture-store", "operator")
	defer clear(key[:])
	defer clear(source[:])
	// Fixed HMAC vector from the pre-profile, length-framed admission contract.
	if err != nil || digest != "a0ee89c720fdf32b2f7a771de2dd276d59c3e92c409671975a72fa3e20e2287c" {
		t.Fatal("legacy ticket identity changed", err)
	}
}

func TestCollectionAdmissionNormalizationSurvivesReconciliationAndRetirement(t *testing.T) {
	c, store := testCatalog(t)
	ctx, at := context.Background(), time.Now().UTC()
	p := collectionAdmissionRequest()
	p.NormalizationProfile = collection.FileNormalizationProfile
	ticket, err := c.PrepareCollection(ctx, p, "operator", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	wire := collectionCreateRequest(p, ticket.Ticket)
	changed := wire
	changed.NormalizationProfile = ""
	if _, err := c.CreateCollection(ctx, changed, "operator", collectionClock(at), allowCollectionCommit); !errors.Is(err, ErrValidation) {
		t.Fatal("profile removal consumed the original ticket", err)
	}
	original, err := c.CreateCollection(ctx, wire, "operator", collectionClock(at), allowCollectionCommit)
	if err != nil || original.NormalizationProfile != p.NormalizationProfile {
		t.Fatal("profile not recorded in operation", err)
	}
	head, exists, err := store.CollectionGet(original.ID)
	if err != nil || !exists || head.NormalizationProfile != p.NormalizationProfile {
		t.Fatal("profile not persisted", err)
	}
	modified := head
	modified.NormalizationProfile = ""
	if plaintext, err := c.sealer.Open(ctx, modified.Binding(c.storeID), head.Secret); err == nil {
		clear(plaintext)
		t.Fatal("profile substitution opened the private collection identity")
	}
	reopened, err := NewCatalog(store, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	reconciled, err := reopened.CreateCollection(ctx, wire, "operator", collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || reconciled.ID != original.ID || reconciled.NormalizationProfile != p.NormalizationProfile {
		t.Fatal("catalog restart lost original normalization identity", err)
	}
	cancelAt := at.Add(time.Minute)
	commands := []persistence.Command{
		{Kind: "collection", At: cancelAt, Collection: &persistence.CollectionCommand{Action: "cancel", OperationID: original.ID, UploadID: head.UploadID,
			Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: "operator", At: cancelAt}}},
		{Kind: "collection", At: cancelAt, Collection: &persistence.CollectionCommand{Action: "cleanup", OperationID: original.ID, UploadID: head.UploadID,
			Cleanup: &persistence.CollectionCleanup{ActivityAt: head.ActivityAt}}},
	}
	results, err := store.Submit(ctx, commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	retired, err := reopened.CreateCollection(ctx, wire, "operator", collectionClock(cancelAt.Add(time.Second)), allowCollectionCommit)
	if err != nil || retired.ID != original.ID || retired.NormalizationProfile != p.NormalizationProfile || retired.State != "canceled" {
		t.Fatal("retained admission lost profile or allocated replacement", err)
	}
}
