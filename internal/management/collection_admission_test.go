package management

import (
	"context"
	"encoding/base64"
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

func collectionAdmissionRequest() api.CollectionPrepareRequest {
	return api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(commitment.Format),
		IdentityKey: api.Pointer(strings.Repeat("ab", 32)), SourceFingerprint: api.Pointer(strings.Repeat("cd", 32)),
		ContentDigest: strings.Repeat("ef", 32), ItemCount: 2}
}

func collectionCreateRequest(p api.CollectionPrepareRequest, ticket string) api.OperationCreateRequest {
	return api.OperationCreateRequest{AdmissionTicket: ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(p.IdentityFormat),
		IdentityKey: p.IdentityKey, SourceFingerprint: p.SourceFingerprint, ContentDigest: p.ContentDigest, ItemCount: p.ItemCount, NormalizationProfile: p.NormalizationProfile}
}

func TestCollectionAdmissionOriginalTicketAndCleanupReconciliation(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	at := time.Now().UTC()
	request := collectionAdmissionRequest()
	admission, err := c.PrepareCollection(ctx, request, "team/operator", collectionClock(at), allowCollectionCommit)
	if err != nil || !admission.ExpiresAt.Equal(at.Add(24*time.Hour)) {
		t.Fatal("prepare", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(admission.Ticket)
	if err != nil || strings.Contains(string(raw), *request.IdentityKey) || strings.Contains(string(raw), *request.SourceFingerprint) || strings.Contains(string(raw), "team/operator") {
		t.Fatal("ticket exposes private claims", err)
	}
	wire := collectionCreateRequest(request, admission.Ticket)
	one, err := c.CreateCollection(ctx, wire, "team/operator", collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || one.ID == "" || one.ItemCount == nil || *one.ItemCount != 2 || one.Uploaded == nil || *one.Uploaded != 0 {
		t.Fatal("create", one.ID, err)
	}
	original, ok, err := store.CollectionGet(one.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	// Simulate a client that received no original reply and a new Catalog owner.
	restarted, err := NewCatalog(store, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	two, err := restarted.CreateCollection(ctx, wire, "team/operator", collectionClock(at.Add(time.Minute)), allowCollectionCommit)
	if err != nil || two.ID != one.ID {
		t.Fatal("retry allocated another operation", err)
	}
	current, _, _ := store.CollectionGet(one.ID)
	if !reflect.DeepEqual(original, current) {
		t.Fatal("retry replaced or renewed header")
	}
	cancelAt := at.Add(2 * time.Minute)
	commands := []persistence.Command{{Kind: "collection", At: cancelAt, Collection: &persistence.CollectionCommand{Action: "cancel", OperationID: one.ID, UploadID: original.UploadID,
		Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: "team/operator", At: cancelAt}}},
		{Kind: "collection", At: cancelAt, Collection: &persistence.CollectionCommand{Action: "cleanup", OperationID: one.ID, UploadID: original.UploadID,
			Cleanup: &persistence.CollectionCleanup{ActivityAt: original.ActivityAt}}}}
	results, err := store.Submit(ctx, commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if _, exists, _ := store.CollectionGet(one.ID); exists {
		t.Fatal("empty canceled upload not cleaned")
	}
	three, err := c.CreateCollection(ctx, wire, "team/operator", collectionClock(at.Add(3*time.Minute)), allowCollectionCommit)
	if err != nil || three.ID != one.ID || three.State != "canceled" {
		t.Fatal("lost reply after cleanup", three.ID, three.State, err)
	}
}

func TestCollectionAdmissionBindsOriginalActorInputAndStore(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	at := time.Now().UTC()
	p := collectionAdmissionRequest()
	admission, err := c.PrepareCollection(ctx, p, "operator", collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"actor", "key", "fingerprint", "digest", "count", "ticket", "store", "profile"} {
		t.Run(field, func(t *testing.T) {
			req := collectionCreateRequest(p, admission.Ticket)
			actor := "operator"
			catalog := c
			switch field {
			case "profile":
				req.NormalizationProfile = "cpra.file.base.v1"
			case "actor":
				actor = "another-operator"
			case "key":
				req.IdentityKey = api.Pointer(strings.Repeat("01", 32))
			case "fingerprint":
				req.SourceFingerprint = api.Pointer(strings.Repeat("01", 32))
			case "digest":
				req.ContentDigest = strings.Repeat("01", 32)
			case "count":
				req.ItemCount++
			case "ticket":
				decoded, _ := base64.RawURLEncoding.DecodeString(req.AdmissionTicket)
				var token collectionTicket
				if err := json.Unmarshal(decoded, &token); err != nil {
					t.Fatal(err)
				}
				token.ID = uuid.NewString()
				decoded, _ = json.Marshal(token)
				req.AdmissionTicket = base64.RawURLEncoding.EncodeToString(decoded)
			case "store":
				catalog, _ = testCatalog(t)
			}
			result, err := catalog.CreateCollection(ctx, req, actor, collectionClock(at.Add(time.Second)), allowCollectionCommit)
			if err == nil || result.ID != "" {
				t.Fatal("changed creation accepted")
			}
			if !catalog.Ready() {
				t.Fatal("untrusted ticket poisoned readiness")
			}
		})
	}
	if _, err := c.CreateCollection(ctx, collectionCreateRequest(p, admission.Ticket), "operator", collectionClock(admission.ExpiresAt), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("expired ticket allocated or wrong error", err)
	}
}

func TestCollectionAdmissionRejectsInvalidInputBeforeEpoch(t *testing.T) {
	c, _ := testCatalog(t)
	at := time.Now().UTC()
	for _, kind := range []string{"empty", "uppercase", "count", "format", "fingerprint", "profile"} {
		p := collectionAdmissionRequest()
		switch kind {
		case "profile":
			p.NormalizationProfile = "cpra.file.future.v2"
		case "empty":
			p.IdentityKey = nil
		case "uppercase":
			p.IdentityKey = api.Pointer(strings.Repeat("AB", 32))
		case "count":
			p.ItemCount = 0
		case "format":
			p.IdentityFormat = "unsupported"
		case "fingerprint":
			p.SourceFingerprint = api.Pointer("private-invalid-fingerprint")
		}
		if _, err := c.PrepareCollection(context.Background(), p, "operator", collectionClock(at), allowCollectionCommit); !errors.Is(err, ErrValidation) {
			t.Fatal(kind, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PrepareCollection(ctx, collectionAdmissionRequest(), "operator", collectionClock(at), allowCollectionCommit); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func collectionClock(at time.Time) func() time.Time   { return func() time.Time { return at } }
func allowCollectionCommit(commit func() error) error { return commit() }
