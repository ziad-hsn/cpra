package management

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestOperationAsOrdinaryReceiptsRemainShared(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	private := "PRIVATE-ORDINARY-OPERATION-CANARY"
	prepared, err := c.Prepare(ctx, resource("Credential", "shared", api.CredentialSpec{Value: &private}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := c.CommitAs(ctx, prepared, "original-owner")
	if err != nil {
		t.Fatal(err)
	}
	c.sealer = nil
	for _, terminal := range []bool{false, true} {
		if terminal {
			if err := c.CompleteOperation(ctx, committed.Operation.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		want, err := c.Operation(ctx, committed.Operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		before := store.Status().CommittedIndex
		for _, allow := range []bool{false, true} {
			for _, actor := range []string{"original-owner", "reader", "foreign-operator", ""} {
				got, err := c.OperationAs(ctx, committed.Operation.ID, actor, time.Now().UTC(), allow)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("ordinary receipt was owner-restricted", actor, allow, err)
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), private) {
					t.Fatal("receipt leaked input")
				}
			}
		}
		if store.Status().CommittedIndex != before {
			t.Fatal("read committed a mutation")
		}
	}
}

func TestOperationAsCollectionReadFloorOwnerAndElapsedStaging(t *testing.T) {
	c, store, op, at := newCancelCollection(t)
	ctx := context.Background()
	c.sealer = nil
	before, _, _ := store.CollectionGet(op.ID)
	index := store.Status().CommittedIndex
	for _, tc := range []struct {
		actor string
		allow bool
		want  error
	}{
		{"owner", true, nil}, {"owner", false, persistence.ErrOperationNotFound},
		{"reader", false, persistence.ErrOperationNotFound}, {"foreign", true, persistence.ErrOperationNotFound}, {"", true, persistence.ErrOperationNotFound},
	} {
		got, err := c.OperationAs(ctx, op.ID, tc.actor, at, tc.allow)
		if !errors.Is(err, tc.want) {
			t.Fatal("owner/read floor", tc.actor, tc.allow, err)
		}
		if tc.want != nil && got.ID != "" {
			t.Fatal("denied response exposed identity")
		}
		if tc.want == nil && (got.ID != op.ID || got.State != "uploading" || got.Validated != nil) {
			t.Fatal("wrong upload observation")
		}
	}
	got, err := c.OperationAs(ctx, op.ID, "owner", before.ExpiresAt, true)
	if err != nil || got.State != "expired" || got.ContentDigest != before.ContentDigest || got.ItemCount == nil || uint64(*got.ItemCount) != before.ItemCount || got.Validated != nil {
		t.Fatal("elapsed staging lost original metadata", got, err)
	}
	// The legacy internal method retains its previous expired-error contract.
	if _, err := store.CollectionReceipt(ctx, op.ID, before.ExpiresAt); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("legacy receipt behavior changed", err)
	}
	after, _, _ := store.CollectionGet(op.ID)
	if !reflect.DeepEqual(before, after) || store.Status().CommittedIndex != index {
		t.Fatal("observation renewed staging or committed expiration")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.OperationAs(canceled, op.ID, "owner", at, true); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
	if _, err := c.OperationAs(ctx, op.ID, "owner", time.Time{}, true); !errors.Is(err, persistence.ErrOperationReservation) {
		t.Fatal("invalid observation time", err)
	}
}

func TestOperationAsCollectionOriginalVerdictAndRetiredCancellation(t *testing.T) {
	for _, stage := range []string{"plan-validating", "validated", "rejected"} {
		t.Run(stage, func(t *testing.T) {
			c, store, head := collectionCancelState(t, stage)
			ctx := context.Background()
			c.sealer = nil
			at := head.ActivityAt.Add(time.Second)
			before := store.Status().CommittedIndex
			got, err := c.OperationAs(ctx, head.ID, head.Actor, at, true)
			want := stage
			if stage == "plan-validating" {
				want = "validating"
			}
			if err != nil || got.State != want || got.ID != head.ID {
				t.Fatal("original observed state", got.State, err)
			}
			if stage == "plan-validating" {
				if got.Validated != nil {
					t.Fatal("provisional work fabricated verdict")
				}
			} else if got.Validated == nil || *got.Validated != (stage == "validated") {
				t.Fatal("lost sealed verdict")
			}
			for _, actor := range []string{"reader", "foreign"} {
				if denied, err := c.OperationAs(ctx, head.ID, actor, at, true); !errors.Is(err, persistence.ErrOperationNotFound) || denied.ID != "" {
					t.Fatal("foreign principal observed verdict", err)
				}
			}
			expired, err := c.OperationAs(ctx, head.ID, head.Actor, head.ExpiresAt, true)
			if err != nil || expired.State != "expired" || expired.Validated != nil {
				t.Fatal("expired staging implied executable verdict", err)
			}
			if stage != "plan-validating" {
				page, err := store.CollectionValidationPage(ctx, head.ID, 0, 1, head.ExpiresAt)
				if err != nil || page.Receipt.Header.ResultID != head.Validation.Header.ResultID {
					t.Fatal("staging expiry erased separately retained verdict", err)
				}
			}
			if store.Status().CommittedIndex != before {
				t.Fatal("read mutated catalog")
			}
			if _, err := c.CancelCollection(ctx, head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 4; n++ {
				if _, exists, _ := store.CollectionGet(head.ID); !exists {
					break
				}
				cleanupCanceledCollection(t, store, head.ID, at)
			}
			if _, exists, _ := store.CollectionGet(head.ID); exists {
				t.Fatal("fixture header not retired")
			}
			before = store.Status().CommittedIndex
			retained, err := c.OperationAs(ctx, head.ID, head.Actor, at.Add(29*24*time.Hour), true)
			if err != nil || retained.State != "canceled" || retained.ID != head.ID || retained.Validated != nil {
				t.Fatal("retired cancellation changed original outcome", err)
			}
			if foreign, err := c.OperationAs(ctx, head.ID, "foreign", at, true); !errors.Is(err, persistence.ErrOperationNotFound) || foreign.ID != "" {
				t.Fatal("retired ownership leaked", err)
			}
			raw, _ := json.Marshal(retained)
			for _, forbidden := range []string{"authority", "secret", "ciphertext", "source_fingerprint", "PRIVATE"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatal("private receipt field exposed", forbidden)
				}
			}
			if _, err := c.OperationAs(ctx, head.ID, head.Actor, at.Add(31*24*time.Hour), true); !errors.Is(err, persistence.ErrOperationExpired) {
				t.Fatal("expired receipt revived", err)
			}
			view, err := store.CatalogSnapshot()
			if err != nil || view.Len() != 0 || store.Status().CommittedIndex != before {
				t.Fatal("description activated input", err)
			}
		})
	}
}

func TestUnscopedOperationCannotReadCollection(t *testing.T) {
	c, _, op, at := newCancelCollection(t)
	got, err := c.Operation(t.Context(), op.ID)
	if (!errors.Is(err, persistence.ErrOperationNotFound) && !errors.Is(err, persistence.ErrOperationExpired)) || got.ID != "" {
		t.Fatal("unscoped operation returned collection receipt", got, err)
	}
	got, err = c.OperationAs(t.Context(), op.ID, "owner", at, true)
	if err != nil || got.ID != op.ID {
		t.Fatal("scoped owner read lost receipt", err)
	}
}
