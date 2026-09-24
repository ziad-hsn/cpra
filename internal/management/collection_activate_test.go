package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func publicActivationFixture(t *testing.T, input api.Resource) (collectionSourceFixture, persistence.CollectionState) {
	t.Helper()
	f := coordinatorFixture(t, input)
	return f, publicActivationValidate(t, f)
}

func publicActivationValidate(t *testing.T, f collectionSourceFixture) persistence.CollectionState {
	t.Helper()
	coordinatorRequest(t, &f)
	head, err := f.catalog.runCollectionValidation(t.Context(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if err != nil || head.Validation == nil || !head.Validation.Header.Valid {
		t.Fatal("compile original activation fixture", err)
	}
	for !head.Validation.HistorySealed {
		head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID,
			UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, f.at.Add(3*time.Second))
	}
	return head
}

func publicActivationHead(t *testing.T, f collectionSourceFixture, id string) persistence.CollectionState {
	t.Helper()
	head, found, err := f.store.CollectionGet(id)
	if err != nil || !found {
		t.Fatal("original collection missing", err)
	}
	return head
}

func publicActivationUnchanged(t *testing.T, f collectionSourceFixture, head persistence.CollectionState, index uint64) {
	t.Helper()
	if !reflect.DeepEqual(publicActivationHead(t, f, head.ID), head) || f.store.Status().CommittedIndex != index {
		t.Fatal("rejected activation changed original input, plan, result, or log")
	}
}

func TestCollectionPublicActivationOriginalPlanAndRetry(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer target.Close()
	f, original := publicActivationFixture(t, collectionMonitor("original", target.URL))
	keys := collectionBlockSealing(t, f.catalog)
	at := f.at.Add(4 * time.Second)
	operation, err := f.catalog.ActivateCollection(t.Context(), original.ID, original.Actor, collectionClock(at), allowCollectionCommit)
	if err != nil || operation.ID != original.ID || operation.State != "applying" || operation.ExecutionResult == nil ||
		operation.ExecutionResult.State != "pending" || operation.Committed == nil || *operation.Committed != 0 || operation.Applied == nil || *operation.Applied != 0 {
		t.Fatal("activation did not report original queued execution", operation, err)
	}
	head := publicActivationHead(t, f, original.ID)
	if head.Activation == nil || !head.Activation.At.Equal(at) || head.Activation.ResultID != original.Validation.Header.ResultID ||
		head.Activation.PlanDescriptor != original.Plan.Descriptor || head.Activation.InputProgressDigest != original.ProgressDigest || head.Execution != nil {
		t.Fatal("activation replaced original validation or executed an item")
	}
	withoutAdmission := head.Clone()
	withoutAdmission.Activation, withoutAdmission.Phase = nil, original.Phase
	if !reflect.DeepEqual(withoutAdmission, original) {
		t.Fatal("activation renewed or rewrote its immutable sources")
	}
	index := f.store.Status().CommittedIndex
	again, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || !reflect.DeepEqual(again, operation) {
		t.Fatal("lost-response retry did not reconcile original admission", again, err)
	}
	publicActivationUnchanged(t, f, head, index)
	catalog, err := f.store.CatalogSnapshotContext(t.Context())
	children, childErr := f.store.PendingOperationsContext(t.Context())
	events, eventErr := f.store.History().Page("collection-activation/"+head.ID, "", 100)
	if err != nil || childErr != nil || eventErr != nil || catalog.Len() != 0 || len(children) != 0 || len(events.Events) != 1 ||
		keys.opens.Load() != 0 || keys.wraps.Load() != 0 || providerCalls.Load() != 0 {
		t.Fatal("admission/retry repeated work, touched secrets, or changed catalog", err, childErr, eventErr)
	}
	encoded, err := json.Marshal(operation)
	if err != nil || strings.Contains(string(encoded), target.URL) || strings.Contains(string(encoded), head.Activation.ID) || strings.Contains(string(encoded), head.ProgressDigest) {
		t.Fatal("public admission leaked protected metadata", err)
	}
}

func TestCollectionPublicActivationConcurrentAndUncertainReplies(t *testing.T) {
	f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	errs := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, err := f.catalog.ActivateCollection(ctx, head.ID, head.Actor, collectionClock(f.at.Add(4*time.Second)), func(commit func() error) error {
				arrived <- struct{}{}
				select {
				case <-release:
					return commit()
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("competing requests did not reach admission", ctx.Err())
		}
	}
	close(release)
	callers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("competing request failed to reconcile the winning original identity", err)
		}
	}
	admitted := publicActivationHead(t, f, head.ID)
	events, err := f.store.History().Page("collection-activation/"+head.ID, "", 100)
	if err != nil || admitted.Activation == nil || len(events.Events) != 1 || events.Events[0].ActionID != admitted.Activation.ID {
		t.Fatal("competing activation replaced or duplicated original admission", err)
	}

	t.Run("reply-lost-after-commit", func(t *testing.T) {
		f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
		op, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(f.at.Add(4*time.Second)), func(commit func() error) error {
			if err := commit(); err != nil {
				return err
			}
			return errors.New("test transport lost the committed reply")
		})
		if err != nil || op.State != "applying" {
			t.Fatal("known retained admission was replaced by a lost reply", op, err)
		}
		admitted := publicActivationHead(t, f, head.ID)
		index := f.store.Status().CommittedIndex
		if _, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(f.at.Add(5*time.Second)), allowCollectionCommit); err != nil {
			t.Fatal(err)
		}
		publicActivationUnchanged(t, f, admitted, index)
	})
}

func TestCollectionPublicActivationRejectsUnreadyAndForeignInput(t *testing.T) {
	for _, phase := range []string{"plan-validating", "result-validating", "unsealed-success", "unsealed-rejection", "rejected", "validated"} {
		t.Run(phase, func(t *testing.T) {
			// The existing historical fixture's successful verdict deliberately
			// has a different capability digest. It remains an immutable verdict,
			// but this process cannot newly admit it under its current profile.
			catalog, store, head := collectionCancelState(t, phase)
			f := collectionSourceFixture{catalog: catalog, store: store}
			at, index := head.ActivityAt.Add(time.Second), store.Status().CommittedIndex
			if head.Validation != nil && head.Validation.FinalizedAt.After(head.ActivityAt) {
				at = head.Validation.FinalizedAt.Add(time.Second)
			}
			admit := func(func() error) error {
				t.Error("ineligible plan reached admission")
				return errors.New("unexpected admission")
			}
			if _, err := catalog.ActivateCollection(t.Context(), head.ID, "another-owner", collectionClock(at), admit); !errors.Is(err, persistence.ErrOperationNotFound) {
				t.Fatal("foreign original owner was disclosed", err)
			}
			if _, err := catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), admit); !errors.Is(err, persistence.ErrCollectionConflict) {
				t.Fatal("ineligible original verdict authorized execution", err)
			}
			publicActivationUnchanged(t, f, head, index)
		})
	}
}

func TestCollectionPublicActivationFreshAuthorityAndAdmission(t *testing.T) {
	for _, failure := range []string{"denied", "canceled", "stopping", "expired-authority", "canceled-original"} {
		t.Run(failure, func(t *testing.T) {
			f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
			index := f.store.Status().CommittedIndex
			at := f.at.Add(4 * time.Second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			denied := errors.New("test current request is no longer authorized")
			want := error(denied)
			admit := func(commit func() error) error {
				switch failure {
				case "denied":
					return denied
				case "canceled":
					cancel()
					want = context.Canceled
				case "stopping":
					f.catalog.validationStopping.Store(true)
					want = ErrUnavailable
				case "expired-authority":
					at = at.Add(2 * time.Hour)
					want = persistence.ErrOperatorAuthorityDenied
				case "canceled-original":
					_, err := f.catalog.CancelCollection(ctx, head.ID, head.Actor, func() time.Time { return at }, allowCollectionCommit)
					if err != nil {
						return err
					}
					head = publicActivationHead(t, f, head.ID)
					index = f.store.Status().CommittedIndex + 1 // The stale committed admission is rejected.
					want = persistence.ErrCollectionConflict
				}
				return commit()
			}
			if _, err := f.catalog.ActivateCollection(ctx, head.ID, head.Actor, func() time.Time { return at }, admit); !errors.Is(err, want) {
				t.Fatal("admission did not honor current cancellation/policy/lifecycle", failure, err, want)
			}
			publicActivationUnchanged(t, f, head, index)
		})
	}
}

func TestCollectionPublicActivationPreservesStaleGuards(t *testing.T) {
	desired := resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("approved-value")})
	f, original := publicActivationFixture(t, desired)
	createResource(t, f.catalog, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("outside-writer")}))
	if _, err := f.catalog.ActivateCollection(t.Context(), original.ID, original.Actor, collectionClock(f.at.Add(4*time.Second)), allowCollectionCommit); err != nil {
		t.Fatal("admission revalidated instead of preserving the approved guards", err)
	}
	head := publicActivationHead(t, f, original.ID)
	if !reflect.DeepEqual(head.Plan, original.Plan) || !reflect.DeepEqual(head.Validation, original.Validation) {
		t.Fatal("activation refreshed original plan/validation")
	}
	w := executionTestWorker(t, f, head)
	// Begin, then record the original target conflict without preparation.
	for range 2 {
		executionAdvance(t, w, head.ID)
	}
	head = publicActivationHead(t, f, head.ID)
	if head.Execution == nil || head.Execution.Processed != 1 || head.Execution.Conflicts != 1 || head.Execution.Accepted != 0 {
		t.Fatal("original absent-target guard was silently refreshed", head.Execution)
	}
}

func TestCollectionPublicActivationRetainedCanceledResult(t *testing.T) {
	f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
	at := f.at.Add(4 * time.Second)
	if _, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	head = publicActivationHead(t, f, head.ID)
	originalActivation := head.Activation.Clone()
	at = at.Add(time.Second)
	if _, err := f.catalog.CancelCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	head = publicActivationHead(t, f, head.ID)
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			if retained {
				at = at.Add(time.Second)
				binding := persistence.CollectionExecutionBinding{OperationID: head.ID, UploadID: head.UploadID, ActivationID: head.Activation.ID,
					PlanID: head.Activation.PlanID, PlanDigest: head.Activation.PlanDescriptor.Digest}
				result := candidateExecutionSubmit(t, f.store, persistence.CollectionExecuteCommand{Action: "finalize", Binding: binding,
					Finalize: persistence.CollectionExecutionFinalizeFenceFor(head)}, at)
				if result.Err != nil || result.Collection == nil {
					t.Fatal("finalize canceled original activation", result.Err)
				}
				head = executionProjectionPublish(t, f, result.Collection.Clone(), at)
				result = candidateExecutionSubmit(t, f.store, persistence.CollectionExecuteCommand{Action: "retire", Binding: binding,
					Retirement: persistence.CollectionExecutionRetirementFenceFor(head)}, at)
				if result.Err != nil || result.Collection == nil {
					t.Fatal("retire finalized original execution", result.Err)
				}
				head = result.Collection.Clone()
				for range 10 {
					at = at.Add(time.Second)
					result = candidateExecutionSubmit(t, f.store, persistence.CollectionExecuteCommand{Action: "retire_sources", Binding: binding,
						SourceRetirement: persistence.CollectionExecutionSourceRetirementFenceFor(head)}, at)
					if result.Err != nil || result.Collection == nil {
						t.Fatal("retire original sources", result.Err)
					}
					head = result.Collection.Clone()
					if _, found, _ := f.store.CollectionGet(head.ID); !found {
						break
					}
				}
				if _, found, _ := f.store.CollectionGet(head.ID); found {
					t.Fatal("retained fixture did not remove the source header")
				}
			}
			index := f.store.Status().CommittedIndex
			op, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit)
			if err != nil || op.State != "canceled" || op.ID != head.ID || f.store.Status().CommittedIndex != index ||
				!reflect.DeepEqual(head.Activation, &originalActivation) || retained && (op.ExecutionResult == nil || op.ExecutionResult.State != "ready" || op.ExecutionResult.Summary.ResultID != originalActivation.ID) {
				t.Fatal("retry reopened or replaced a canceled original result", op, err)
			}
		})
	}
}

func TestCollectionPublicActivationRetryRechecksAdmission(t *testing.T) {
	for _, gate := range []string{"denied", "stopping", "expired-authority", "foreign-owner"} {
		t.Run(gate, func(t *testing.T) {
			f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
			at := f.at.Add(4 * time.Second)
			if _, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
				t.Fatal(err)
			}
			head = publicActivationHead(t, f, head.ID)
			index, actor := f.store.Status().CommittedIndex, head.Actor
			denied := errors.New("test current request is denied")
			var want error = denied
			called := false
			if gate == "foreign-owner" {
				actor = "another-owner"
				want = persistence.ErrOperationNotFound
			}
			_, err := f.catalog.ActivateCollection(t.Context(), head.ID, actor, func() time.Time { return at }, func(commit func() error) error {
				called = true
				switch gate {
				case "denied":
					return denied
				case "stopping":
					f.catalog.validationStopping.Store(true)
					want = ErrUnavailable
				case "expired-authority":
					at = at.Add(2 * time.Hour)
					want = persistence.ErrOperatorAuthorityDenied
				}
				return commit()
			})
			if !errors.Is(err, want) || called == (gate == "foreign-owner") {
				t.Fatal("retained admission bypassed current request gate", gate, err)
			}
			publicActivationUnchanged(t, f, head, index)
		})
	}
}

func TestCollectionPublicActivationNativeReplayAndRevocation(t *testing.T) {
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	admin, err := persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := collectionOwnerBootstrap(time.Now().UTC().Add(-time.Second))
	bootstrap.Principals[0].ExpiresAt = time.Time{} // Exercise upload expiry independently of the token.
	policy, err := admin.CommitAuthentication(t.Context(), bootstrap)
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	if err = admin.Close(); err != nil {
		t.Fatal(err)
	}
	open := func() (*Catalog, *persistence.Store) {
		store, err := persistence.Open(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return candidateExecutionCatalog(t, store), store
	}
	catalog, store := open()
	desired := resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")})
	fixture := func() (collectionSourceFixture, persistence.CollectionState) {
		f := stagedSourceFixtureStore(t, catalog, store, 1, func(int) (persistence.CatalogKey, []byte) {
			raw, err := json.Marshal(desired)
			if err != nil {
				t.Fatal(err)
			}
			return persistence.CatalogKey{Kind: desired.Kind, ID: desired.Metadata.ID}, raw
		}, func(head *persistence.CollectionState) {
			head.Actor = "team/operator"
			head.Owner = &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: head.Actor}
		}, nil)
		return f, publicActivationValidate(t, f)
	}
	f, head := fixture()
	at := f.at.Add(4 * time.Second)
	op, err := catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit)
	if err != nil {
		t.Fatal(err)
	}
	head = publicActivationHead(t, f, head.ID)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, store = open()
	f.catalog, f.store = catalog, store
	index := store.Status().CommittedIndex
	again, err := catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at.Add(time.Second)), allowCollectionCommit)
	if err != nil || !reflect.DeepEqual(op, again) {
		t.Fatal("reopen retry changed original admission", err)
	}
	publicActivationUnchanged(t, f, head, index)
	// A second original upload passes authority but expires while waiting for admission.
	expiredFixture, expiredHead := fixture()
	index = store.Status().CommittedIndex
	clock := expiredFixture.at.Add(4 * time.Second)
	_, err = catalog.ActivateCollection(t.Context(), expiredHead.ID, expiredHead.Actor, func() time.Time { return clock }, func(commit func() error) error {
		clock = expiredHead.ExpiresAt.Add(time.Second)
		return commit()
	})
	if !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("expired original upload admitted", err)
	}
	publicActivationUnchanged(t, expiredFixture, expiredHead, index+1) // A committed admission command rejects the expired artifact.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err = persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	revoked := policy.Clone().Principals
	revoked[0].Revoked = true
	_, err = admin.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "replace", Epoch: policy.Epoch,
		ExpectedEpoch: policy.Epoch, ExpectedRevision: policy.Revision, Revision: uuid.NewString(), Actor: "local-administrator", At: at.Add(2 * time.Second), Principals: revoked})
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	if err = admin.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, store = open()
	f.catalog, f.store = catalog, store
	index = store.Status().CommittedIndex
	if _, err = catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at.Add(3*time.Second)), allowCollectionCommit); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("revoked owner reused the retained original admission", err)
	}
	publicActivationUnchanged(t, f, head, index)
}

func TestCollectionPublicActivationRetryRechecksRetainedResult(t *testing.T) {
	for _, boundary := range []string{"deadline", "cutoff"} {
		t.Run(boundary, func(t *testing.T) {
			f, head := publicActivationFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
			at := f.at.Add(4 * time.Second)
			if _, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
				t.Fatal(err)
			}
			at = at.Add(time.Second)
			if _, err := f.catalog.CancelCollection(t.Context(), head.ID, head.Actor, collectionClock(at), allowCollectionCommit); err != nil {
				t.Fatal(err)
			}
			head = publicActivationHead(t, f, head.ID)
			at = at.Add(time.Second)
			binding := persistence.CollectionExecutionBinding{OperationID: head.ID, UploadID: head.UploadID, ActivationID: head.Activation.ID, PlanID: head.Activation.PlanID, PlanDigest: head.Activation.PlanDescriptor.Digest}
			result := candidateExecutionSubmit(t, f.store, persistence.CollectionExecuteCommand{Action: "finalize", Binding: binding, Finalize: persistence.CollectionExecutionFinalizeFenceFor(head)}, at)
			if result.Err != nil || result.Collection == nil {
				t.Fatal(result.Err)
			}
			head = executionProjectionPublish(t, f, result.Collection.Clone(), at)
			index := f.store.Status().CommittedIndex
			op, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, func() time.Time { return at }, func(commit func() error) error {
				if err := commit(); err != nil {
					return err
				}
				deadline := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)
				if boundary == "deadline" {
					at = deadline
					return nil
				}
				return f.store.History().Expire(deadline)
			})
			if err != nil || op.State != "canceled" || op.ExecutionResult == nil || op.ExecutionResult.State != "expired" || op.ExecutionResult.Summary.ResultID != head.Activation.ID {
				t.Fatal("read-only retry returned stale ready availability after admission wait", op, err)
			}
			publicActivationUnchanged(t, f, head, index)
		})
	}
}

func TestCollectionPublicActivationRetryRechecksRestoreEpoch(t *testing.T) {
	f, head, config := candidateExecutionFixture(t, true, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}))
	at := head.Activation.At.Add(time.Second)
	originalStore := f.store
	op, err := f.catalog.ActivateCollection(t.Context(), head.ID, head.Actor, func() time.Time { return at }, func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		if err := originalStore.Close(); err != nil {
			return err
		}
		if err := persistence.MarkRestored(config.Storage.Directory, at); err != nil {
			return err
		}
		admin, err := persistence.OpenAdministrative(t.Context(), *config)
		if err != nil {
			return err
		}
		defer admin.Close()
		policy, err := admin.Authentication()
		if err != nil || !policy.ResetRequired {
			t.Fatal("restore failed to fence old authority", err)
		}
		provision := collectionOwnerBootstrap(at.Add(time.Second))
		provision.Mode, provision.Epoch, provision.ExpectedEpoch, provision.ExpectedRevision = "provision", policy.Epoch, policy.Epoch, policy.Revision
		if _, err := admin.CommitAuthentication(t.Context(), provision); err != nil {
			return err
		}
		if err := admin.Close(); err != nil {
			return err
		}
		reopened, err := persistence.Open(t.Context(), *config)
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = reopened.Close() })
		// A synchronous test splice models replacement during recovery without
		// accessing private FSM locks or manufacturing a new operation epoch.
		f.catalog.store = reopened
		at = at.Add(2 * time.Second)
		if _, err := reopened.ObserveOperatorAuthority(t.Context(), head.Actor, at); err != nil {
			t.Fatal("restored current authority is not ready", err)
		}
		return nil
	})
	if !errors.Is(err, persistence.ErrOperationExpired) || op.ExecutionResult != nil || op.State != "" {
		t.Fatal("pre-restore admission receipt escaped current operation epoch", op, err)
	}
}
