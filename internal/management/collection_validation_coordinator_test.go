package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func coordinatorFixture(t *testing.T, input ...api.Resource) collectionSourceFixture {
	t.Helper()
	c, store := testCatalog(t)
	policy, err := store.CommitAuthentication(context.Background(), collectionOwnerBootstrap(time.Now().UTC().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	return stagedSourceFixtureStore(t, c, store, len(input), func(i int) (persistence.CatalogKey, []byte) {
		r := input[i]
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}, raw
	}, func(head *persistence.CollectionState) {
		head.Actor = "team/operator"
		head.Owner = &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: head.Actor}
	}, nil)
}

func coordinatorRequest(t *testing.T, f *collectionSourceFixture) persistence.CollectionState {
	t.Helper()
	op, err := f.catalog.RequestCollectionValidation(context.Background(), f.head.ID, "team/operator", collectionClock(f.at.Add(time.Second)), allowCollectionCommit)
	if err != nil || op.ID != f.head.ID || op.State != "validating" {
		t.Fatal("validation request", op, err)
	}
	head, ok, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !ok || head.ValidationRequest == nil {
		t.Fatal("request missing", err)
	}
	return head
}

func TestCollectionValidationCoordinatorCommitsOneVerdictWithoutActivation(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "invalid"}[invalid], func(t *testing.T) {
			var calls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer target.Close()
			log := filepath.Join(t.TempDir(), "never-opened.log")
			secret := "PRIVATE-COORDINATOR-CANARY"
			inputs := []api.Resource{
				collectionMonitor("api", target.URL),
				resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
				resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"log"}}),
				planLogEndpoint("log", log),
				resource("Credential", "secret", api.CredentialSpec{Value: &secret}),
			}
			if invalid {
				inputs[4] = resource("Credential", "secret", api.CredentialSpec{}) // Missing create value.
			}
			f := coordinatorFixture(t, inputs...)
			requested := coordinatorRequest(t, &f)
			wrapper := collectionBlockSealing(t, f.catalog)
			run := uuid.NewString()
			state, err := f.catalog.runCollectionValidation(context.Background(), f.head.ID, run, collectionClock(f.at.Add(2*time.Second)))
			if err != nil {
				t.Fatal("attempt failed", state.Phase, err)
			}
			v := state.Validation
			if v == nil || v.FinalizedAt.IsZero() || v.Header.Valid == invalid || state.ValidationRequest.ID != requested.ValidationRequest.ID || state.ValidationRequest.Claim.RunID != run {
				t.Fatal("original request/claim/verdict not committed")
			}
			if !invalid && (state.Plan == nil || state.Plan.FinalizedAt.IsZero() || v.Header.PlanID != state.Plan.Header.PlanID) {
				t.Fatal("successful result lost frozen plan identity")
			}
			if invalid && state.Plan != nil {
				t.Fatal("rejection retained an executable plan")
			}
			view, err := f.store.CatalogSnapshot()
			if err != nil || view.Len() != 0 || calls.Load() != 0 || wrapper.wraps.Load() != 0 {
				t.Fatal("validation changed catalog, invoked provider, or prepared encrypted mutation", err)
			}
			if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("validation opened notification target", err)
			}
			raw, err := json.Marshal(state)
			if err != nil || bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(target.URL)) || bytes.Contains(raw, []byte(log)) {
				t.Fatal("plaintext entered request/plan/result metadata", err)
			}
			beforeOpens := wrapper.opens.Load()
			_, err = f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(3*time.Second)))
			if !errors.Is(err, persistence.ErrCollectionConflict) || wrapper.opens.Load() != beforeOpens {
				t.Fatal("second coordinator recompiled the original input", err)
			}
			_, err = f.catalog.RequestCollectionValidation(context.Background(), f.head.ID, "team/operator", collectionClock(f.at.Add(4*time.Second)), allowCollectionCommit)
			after, _, readErr := f.store.CollectionGet(f.head.ID)
			if err != nil || readErr != nil || !reflect.DeepEqual(after, state) {
				t.Fatal("request retry renewed or replaced the verdict", err, readErr)
			}
		})
	}
}

func TestCollectionValidationCoordinatorConcurrentRequestsReconcile(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	var arrivals sync.WaitGroup
	arrivals.Add(2)
	var workers sync.WaitGroup
	workers.Add(2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			defer workers.Done()
			calls := 0
			now := func() time.Time {
				calls++
				if calls == 2 { // Both original receipts were observed before either submission.
					arrivals.Done()
					arrivals.Wait()
				}
				return f.at.Add(time.Second)
			}
			op, err := f.catalog.RequestCollectionValidation(context.Background(), f.head.ID, "team/operator", now, allowCollectionCommit)
			if err == nil && op.ID != f.head.ID {
				err = errors.New("request changed operation")
			}
			errs <- err
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("competing validation request did not reconcile", err)
		}
	}
	head, _, _ := f.store.CollectionGet(f.head.ID)
	if head.ValidationRequest == nil || head.ValidationRequest.Claim != nil || head.Plan != nil || head.Validation != nil {
		t.Fatal("admission compiled or replaced its queued state")
	}
}

func TestCollectionValidationCoordinatorAdmissionAndClaimFailuresDoNotDecrypt(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	denied := errors.New("admission closed")
	_, err := f.catalog.RequestCollectionValidation(context.Background(), f.head.ID, "team/operator", collectionClock(f.at.Add(time.Second)), func(func() error) error { return denied })
	if !errors.Is(err, denied) {
		t.Fatal("admission bypassed", err)
	}
	_, err = f.catalog.RequestCollectionValidation(context.Background(), f.head.ID, "other/operator", collectionClock(f.at.Add(time.Second)), allowCollectionCommit)
	if !errors.Is(err, persistence.ErrOperationNotFound) {
		t.Fatal("foreign operation disclosed", err)
	}
	before, _, _ := f.store.CollectionGet(f.head.ID)
	if !reflect.DeepEqual(before, f.head) {
		t.Fatal("denied admission changed state")
	}
	coordinatorRequest(t, &f)
	wrapper := collectionBlockSealing(t, f.catalog)
	_, err = f.catalog.runCollectionValidation(context.Background(), f.head.ID, "invalid-run", collectionClock(f.at.Add(2*time.Second)))
	if err == nil || wrapper.opens.Load() != 0 {
		t.Fatal("invalid claim reached encrypted input", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.catalog.runCollectionValidation(canceled, f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if !errors.Is(err, context.Canceled) || wrapper.opens.Load() != 0 {
		t.Fatal("canceled runner decrypted input", err)
	}
	head, _, _ := f.store.CollectionGet(f.head.ID)
	if head.ValidationRequest.Claim != nil || head.Plan != nil {
		t.Fatal("failed claim modified attempt")
	}
}

func TestCollectionValidationCoordinatorInterruptedAppendKeepsOriginalClaimAndPrefix(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	coordinatorRequest(t, &f)
	wrapper := collectionBlockSealing(t, f.catalog)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := func() time.Time {
		head, _, err := f.store.CollectionGet(f.head.ID)
		if err != nil {
			t.Fatal(err)
		}
		if head.Plan != nil {
			cancel() // Reject the first append after the real plan_begin commit.
		}
		return f.at.Add(2 * time.Second)
	}
	run := uuid.NewString()
	state, err := f.catalog.runCollectionValidation(ctx, f.head.ID, run, now)
	if !errors.Is(err, context.Canceled) || state.ID != f.head.ID || state.Plan == nil || state.Plan.UploadedFragments != 0 || state.Validation != nil || state.ValidationRequest.Claim.RunID != run {
		t.Fatal("failed append lost original committed prefix or became a verdict", state.Phase, err)
	}
	stored, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !reflect.DeepEqual(state, stored) {
		t.Fatal("returned last observed state differs from failed append prefix", err)
	}
	opens := wrapper.opens.Load()
	_, err = f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(3*time.Second)))
	after, _, readErr := f.store.CollectionGet(f.head.ID)
	if !errors.Is(err, persistence.ErrCollectionConflict) || wrapper.opens.Load() != opens || readErr != nil || !reflect.DeepEqual(stored, after) {
		t.Fatal("second attempt decrypted or replaced an interrupted compiler claim", err, readErr)
	}
}

func TestCollectionValidationCoordinatorPipeJoinsOnVisitorFailureAndCancellation(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareCollectionPlanArtifact(context.Background(), plan, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("append rejected")
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		finished := make(chan error, 1)
		go func() {
			finished <- visitCollectionPlanArtifact(ctx, artifact, func(persistence.CollectionPlanFragment) error { return want })
		}()
		select {
		case err := <-finished:
			if canceled && !errors.Is(err, context.Canceled) || !canceled && !errors.Is(err, want) {
				t.Fatal("visitor/cancellation error lost", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("encoder did not join")
		}
		cancel()
	}
	if err := visitCollectionPlanArtifact(context.Background(), artifact, func(persistence.CollectionPlanFragment) error { return nil }); err != nil {
		t.Fatal("interrupted stream damaged frozen descriptor", err)
	}
}
