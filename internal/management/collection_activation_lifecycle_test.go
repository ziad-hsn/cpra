package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// This uses the actual compiler and immutable artifacts. Only activation
// admission is submitted privately: public Activate and item execution remain
// absent. The target accounts independently for accidental provider invocation.
func admittedCollectionFixture(t *testing.T, target string) (collectionSourceFixture, persistence.CollectionState) {
	t.Helper()
	catalog, store := testCatalog(t)
	bootstrap := collectionOwnerBootstrap(time.Now().UTC().Add(-time.Second))
	bootstrap.Principals[0].ExpiresAt = bootstrap.At.Add(40 * 24 * time.Hour)
	policy, err := store.CommitAuthentication(context.Background(), bootstrap)
	if err != nil {
		t.Fatal("provision dedicated fixture authority", err)
	}
	input := collectionMonitor("admitted", target)
	f := stagedSourceFixtureStore(t, catalog, store, 1, func(int) (persistence.CatalogKey, []byte) {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}, raw
	}, func(head *persistence.CollectionState) {
		head.Actor = "team/operator"
		head.Owner = &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: head.Actor}
	}, nil)
	coordinatorRequest(t, &f)
	head, err := f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if err != nil || head.Validation == nil || !head.Validation.Header.Valid {
		t.Fatal("original compiler result", err)
	}
	head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID,
		UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID}, f.at.Add(3*time.Second))
	if !head.Validation.HistorySealed {
		t.Fatal("original result not sealed")
	}
	at := f.at.Add(4 * time.Second)
	authority, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority,
		InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor,
		PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor,
		CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest,
		ValidationRequest:  persistence.CollectionValidationRequestFenceFor(head), At: at}
	head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID,
		UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &authority}, at)
	if head.Phase != "applying" || head.Activation == nil {
		t.Fatal("private activation admission missing")
	}
	return f, head
}

func TestCollectionActivationAdmissionManagementLifetimeAndCancellation(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	f, original := admittedCollectionFixture(t, target.URL)
	ctx := context.Background()
	// Further operations have no reason to open or seal provider configuration.
	wrapper := collectionBlockSealing(t, f.catalog)
	opens := wrapper.opens.Load()
	for _, days := range []int{2, 31} {
		at := original.Activation.At.AddDate(0, 0, days)
		point, err := f.catalog.OperationAs(ctx, original.ID, original.Actor, at, true)
		if err != nil || point.State != "applying" || point.ID != original.ID || point.Validated != nil ||
			point.Committed == nil || *point.Committed != 0 || point.Applied == nil || *point.Applied != 0 {
			t.Fatal("live admitted operation expired or claimed execution", days, point, err)
		}
		view, err := f.catalog.ManagementOperationSnapshot(ctx, original.Actor, at, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		items, next, err := view.Page(ctx, "", "", 100)
		if err != nil || next != "" || len(items) != 1 || !reflect.DeepEqual(items[0], point) {
			t.Fatal("list/detail disagree for admitted operation", days, next, err)
		}
		_, _, err = f.store.CollectionValidationResultView(ctx, original.ID, original.Actor, at)
		if days == 2 && err != nil || days == 31 && !errors.Is(err, persistence.ErrOperationExpired) {
			t.Fatal("validation retention coupled to activation lifetime", days, err)
		}
	}
	at := original.Activation.At.AddDate(0, 0, 31)
	worker, err := f.catalog.StartCollectionValidationCoordinator(ctx, collectionClock(at))
	if err != nil {
		t.Fatal("startup misclassified admitted operation", err)
	}
	worker.BeginStop()
	join, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := worker.Wait(join); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.CancelCollection(ctx, original.ID, "another-owner", collectionClock(at), allowCollectionCommit); !errors.Is(err, persistence.ErrOperationNotFound) {
		t.Fatal("foreign actor learned or canceled operation", err)
	}
	result, err := f.catalog.CancelCollection(ctx, original.ID, original.Actor, collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || result.State != "canceled" || result.ID != original.ID || result.Committed == nil || *result.Committed != 0 {
		t.Fatal("admitted operation could not be canceled after original deadline", result, err)
	}
	head, exists, err := f.store.CollectionGet(original.ID)
	if err != nil || !exists || !reflect.DeepEqual(head.Activation, original.Activation) || head.Phase != "canceled" {
		t.Fatal("cancellation replaced original activation", err)
	}
	index := f.store.Status().CommittedIndex
	again, err := f.catalog.CancelCollection(ctx, original.ID, original.Actor, collectionClock(at.Add(2*time.Second)), func(func() error) error {
		t.Error("terminal cancellation retried a write")
		return errors.New("unexpected admission")
	})
	if err != nil || !reflect.DeepEqual(again, result) || f.store.Status().CommittedIndex != index {
		t.Fatal("terminal retry changed original disposition", err)
	}
	events, err := f.store.History().Page("collection/"+original.ID, "", 100)
	if err != nil || len(events.Events) != 1 || events.Events[0].Type != "collection_canceled" {
		t.Fatal("cancellation did not retain one terminal receipt", err)
	}
	catalog, err := f.store.CatalogSnapshot()
	if err != nil || catalog.Len() != 0 || calls.Load() != 0 || wrapper.opens.Load() != opens || wrapper.wraps.Load() != 0 {
		t.Fatal("admission observation/startup/cancellation executed provider work or decrypted input", err)
	}
}

func TestCollectionActivationCancellationRechecksCurrentAuthority(t *testing.T) {
	f, original := admittedCollectionFixture(t, "http://127.0.0.1:1/never-invoked")
	at := original.Activation.At.Add(time.Second)
	_, err := f.catalog.CancelCollection(context.Background(), original.ID, original.Actor, func() time.Time { return at }, func(commit func() error) error {
		// Time can cross a credential deadline while admission waits. The
		// original owner metadata must not waive the actual write-time check.
		at = original.Activation.At.AddDate(0, 0, 41)
		return commit()
	})
	if !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("admission retained a stale cancellation grant", err)
	}
	head, exists, err := f.store.CollectionGet(original.ID)
	if err != nil || !exists || !reflect.DeepEqual(head, original) {
		t.Fatal("denied cancellation changed original activation", err)
	}
}
