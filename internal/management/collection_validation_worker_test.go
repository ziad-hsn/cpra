package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type blockedValidationKeys struct {
	secureconfig.KeyWrapper
	entered chan context.Context
	release chan struct{}
	once    sync.Once
	close   sync.Once
	opens   atomic.Int64
}

func (k *blockedValidationKeys) Unwrap(ctx context.Context, payload, aad []byte) ([]byte, error) {
	k.opens.Add(1)
	k.once.Do(func() {
		k.entered <- ctx
		<-k.release // Deliberately uncooperative adapter; supervisor must retain ownership.
	})
	return k.KeyWrapper.Unwrap(ctx, payload, aad)
}

func blockValidationKeys(t *testing.T, catalog *Catalog) *blockedValidationKeys {
	t.Helper()
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	k := &blockedValidationKeys{KeyWrapper: inner, entered: make(chan context.Context, 1), release: make(chan struct{})}
	catalog.sealer, err = secureconfig.NewSealer(k)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.unblock)
	return k
}

func (k *blockedValidationKeys) unblock() { k.close.Do(func() { close(k.release) }) }

func validationWorkerCleanup(t *testing.T, w *CollectionValidationCoordinator, keys *blockedValidationKeys) {
	t.Helper()
	t.Cleanup(func() {
		w.BeginStop()
		if keys != nil {
			keys.unblock()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = w.Wait(ctx)
		select {
		case <-w.Done():
		default:
			t.Error("test left a compiler owner alive")
		}
	})
}

func workerWake(w *CollectionValidationCoordinator) {
	select {
	case w.catalog.validationWake <- struct{}{}:
	default:
	}
}

func workerAwaitUnwrap(t *testing.T, keys *blockedValidationKeys) context.Context {
	t.Helper()
	select {
	case ctx := <-keys.entered:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach real encrypted input")
		return nil
	}
}

func TestCollectionValidationWorkerSingleOwnerAndBoundedShutdown(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "first", api.CredentialSpec{Value: api.Pointer("private")}))
	second := resource("Credential", "second", api.CredentialSpec{Value: api.Pointer("private")})
	other := stagedSourceFixtureStore(t, f.catalog, f.store, 1, func(int) (persistence.CatalogKey, []byte) {
		raw, _ := json.Marshal(second)
		return persistence.CatalogKey{Kind: second.Kind, ID: second.Metadata.ID}, raw
	}, func(head *persistence.CollectionState) { head.Actor, head.Owner = f.head.Actor, f.head.Owner }, nil)
	at := other.at.Add(10 * time.Second)
	for i, id := range []string{f.head.ID, other.head.ID} {
		if _, err := f.catalog.RequestCollectionValidation(context.Background(), id, f.head.Actor, collectionClock(at.Add(time.Duration(i)*time.Millisecond)), allowCollectionCommit); err != nil {
			t.Fatal(err)
		}
	}
	keys := blockValidationKeys(t, f.catalog)
	now := collectionClock(at.Add(time.Second))
	w, err := f.catalog.StartCollectionValidationCoordinator(context.Background(), now)
	if err != nil || !w.Ready() {
		t.Fatal("start", err)
	}
	validationWorkerCleanup(t, w, keys)
	workerAwaitUnwrap(t, keys)
	work, err := f.store.CollectionValidationWork(context.Background(), now())
	if err != nil || len(work) != 2 || work[0].Request.Claim == nil || work[1].Request.Claim != nil || keys.opens.Load() != 1 {
		t.Fatal("more than one queued attempt began", err)
	}
	secondCatalog, err := NewCatalog(f.store, f.catalog.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondCatalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := secondCatalog.StartCollectionValidationCoordinator(context.Background(), now); duplicate != nil || !errors.Is(err, persistence.ErrCollectionCoordinatorRegistered) {
		t.Fatal("second Catalog could retire a live owner's claim", err)
	}
	w.BeginStop()
	w.BeginStop()
	if w.Ready() {
		t.Fatal("stop retained readiness")
	}
	if _, err := f.catalog.RequestCollectionValidation(context.Background(), other.head.ID, f.head.Actor, now, allowCollectionCommit); !errors.Is(err, ErrUnavailable) {
		t.Fatal("validation admission remained open while draining", err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Wait(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("uncooperative unwrap did not retain dependency ownership", err)
	}
	select {
	case <-w.Done():
		t.Fatal("coordinator reported joined before the unwrap returned")
	default:
	}
	if !f.store.Status().Ready {
		t.Fatal("ordinary cancellation poisoned storage")
	}
	keys.unblock()
	joined, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := w.Wait(joined); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Flush(joined); err != nil {
		t.Fatal(err)
	}
	after, err := f.store.CollectionValidationWork(joined, now())
	if err != nil || !reflect.DeepEqual(work, after) || keys.opens.Load() != 1 {
		t.Fatal("shutdown rewrote original intent or began next queued work", err)
	}
	if duplicate, err := secondCatalog.StartCollectionValidationCoordinator(joined, now); duplicate != nil || !errors.Is(err, persistence.ErrCollectionCoordinatorRegistered) {
		t.Fatal("joined owner released startup-sweep registration", err)
	}
}

func TestCollectionValidationWorkerAuthorityExpiryCancelsBeforeNextInput(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	coordinatorRequest(t, &f)
	var clock atomic.Int64
	clock.Store(f.at.Add(5 * time.Second).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	keys := blockValidationKeys(t, f.catalog)
	w, err := f.catalog.StartCollectionValidationCoordinator(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	validationWorkerCleanup(t, w, keys)
	active := workerAwaitUnwrap(t, keys)
	policy, err := f.store.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	// Live policy replacement requires stopped administration. Advance only the
	// supplied observation clock to the actual committed principal deadline.
	clock.Store(policy.Principals[0].ExpiresAt.UnixNano())
	workerWake(w)
	select {
	case <-active.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("authority expiry did not cancel blocked compilation")
	}
	keys.unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		r, err := f.store.CollectionReceipt(ctx, f.head.ID, now())
		if err == nil && r.Phase == "interrupted" {
			if r.Validation != nil || r.ValidationRequest.Interruption.Reason != "authorityChanged" || r.ValidationRequest.Claim.RunID != w.runID {
				t.Fatal("authority expiry replaced original claim or became invalid input")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("expired authority did not retain interruption", err, w.Err())
		case <-w.Done():
			t.Fatal("authority expiry was treated as infrastructure failure", w.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if !w.Ready() || keys.opens.Load() != 1 {
		t.Fatal("authority expiry admitted more encrypted input or failed coordinator")
	}
}

func TestCollectionValidationWorkerFatalClockClosesAdmissionButAllowsFlush(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	coordinatorRequest(t, &f)
	var clock atomic.Int64
	clock.Store(f.at.Add(5 * time.Second).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	keys := blockValidationKeys(t, f.catalog)
	w, err := f.catalog.StartCollectionValidationCoordinator(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	validationWorkerCleanup(t, w, keys)
	active := workerAwaitUnwrap(t, keys)
	clock.Store(f.at.UnixNano())
	workerWake(w)
	select {
	case <-active.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("inconsistent observation did not cancel compiler")
	}
	if w.Ready() || f.catalog.Ready() || !f.store.Status().Ready {
		t.Fatal("coordinator failure did not isolate admission from storage integrity")
	}
	select {
	case <-w.Done():
		t.Fatal("fatal error abandoned a live unwrap")
	default:
	}
	keys.unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Wait(ctx); !errors.Is(err, persistence.ErrCollectionConflict) || !errors.Is(w.Err(), err) {
		t.Fatal("fatal observation error lost after join", err)
	}
	if err := f.store.Flush(ctx); err != nil {
		t.Fatal("coordinator-only failure blocked the shutdown flush", err)
	}
}

func TestCollectionValidationWorkerProfileMismatchRetiresBeforeDecryption(t *testing.T) {
	// Exercise a real older-build profile through admission, not a mutable FSM
	// header. The original request never gains permission to decrypt this input.
	other := coordinatorFixture(t, resource("Credential", "old", api.CredentialSpec{Value: api.Pointer("private")}))
	at := other.at.Add(time.Second)
	r := persistence.CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: other.head.ProgressDigest, ItemCount: other.head.ItemCount,
		Authority: *other.head.Owner, CapabilitiesDigest: string(bytes.Repeat([]byte{'a'}, 64)), RequestedAt: at}
	_ = submitStagedSource(t, other.store, persistence.CollectionCommand{Action: "validation_request", OperationID: other.head.ID,
		UploadID: other.head.UploadID, ValidationRequest: &r}, at)
	keys := collectionBlockSealing(t, other.catalog)
	w, err := other.catalog.StartCollectionValidationCoordinator(context.Background(), collectionClock(at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	validationWorkerCleanup(t, w, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		receipt, err := other.store.CollectionReceipt(ctx, other.head.ID, at.Add(time.Second))
		if err == nil && receipt.Phase == "interrupted" {
			if receipt.ValidationRequest.Claim != nil || receipt.ValidationRequest.Interruption.Reason != "capabilitiesChanged" || keys.opens.Load() != 0 {
				t.Fatal("mismatched profile was claimed or decrypted")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("profile mismatch did not retire", err)
		case <-time.After(time.Millisecond):
		}
	}
	w.BeginStop()
	if err := w.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
