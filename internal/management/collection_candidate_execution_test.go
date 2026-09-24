package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func candidateExecutionCatalog(t *testing.T, s *persistence.Store) *Catalog {
	t.Helper()
	key, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog(s, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}
func candidateExecutionFixture(t *testing.T, disk bool, desired api.Resource) (collectionSourceFixture, persistence.CollectionState, *runtimeconfig.Config) {
	t.Helper()
	if !disk {
		f, head := candidateFixture(t, desired, nil)
		return f, head, nil
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	admin, err := persistence.OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := admin.CommitAuthentication(context.Background(), collectionOwnerBootstrap(time.Now().UTC().Add(-time.Second)))
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := persistence.Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := candidateExecutionCatalog(t, store)
	f := stagedSourceFixtureStore(t, c, store, 1, func(int) (persistence.CatalogKey, []byte) {
		raw, err := json.Marshal(desired)
		if err != nil {
			t.Fatal(err)
		}
		return persistence.CatalogKey{Kind: desired.Kind, ID: desired.Metadata.ID}, raw
	}, func(head *persistence.CollectionState) {
		head.Actor = "team/operator"
		head.Owner = &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: head.Actor}
	}, nil)
	coordinatorRequest(t, &f)
	head, err := c.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if err != nil || head.Validation == nil || !head.Validation.Header.Valid {
		t.Fatal("real collection compilation", err)
	}
	at := f.at.Add(3 * time.Second)
	for !head.Validation.HistorySealed {
		head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, at)
	}
	authority, err := store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	admission := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest, ValidationRequest: persistence.CollectionValidationRequestFenceFor(head), At: at}
	head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &admission, ActivationAuthority: &authority}, at)
	return f, head, &config
}
func candidateExecutionCommand(t *testing.T, s *persistence.Store, head persistence.CollectionState, action string, at time.Time) persistence.CollectionExecuteCommand {
	t.Helper()
	auth, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	return persistence.CollectionExecuteCommand{Action: action, Binding: persistence.CollectionExecutionBinding{OperationID: head.ID, UploadID: head.UploadID, ActivationID: head.Activation.ID, PlanID: head.Plan.Header.PlanID, PlanDigest: head.Plan.Descriptor.Digest}, Authority: auth, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
}
func candidateExecutionSubmit(t *testing.T, s *persistence.Store, c persistence.CollectionExecuteCommand, at time.Time) persistence.Result {
	t.Helper()
	result, err := s.Submit(context.Background(), []persistence.Command{{Kind: "collection_execute", At: at, CollectionExecute: &c}})
	if err != nil || len(result) != 1 {
		t.Fatal("isolated execution submit", err)
	}
	return result[0]
}
func candidateExecutionPrepare(t *testing.T, f collectionSourceFixture, head persistence.CollectionState) (persistence.CollectionPreparedItem, persistence.CollectionExecuteCommand, persistence.CollectionState) {
	t.Helper()
	at := head.Activation.At.Add(time.Second)
	command := candidateExecutionCommand(t, f.store, head, "begin", at)
	result := candidateExecutionSubmit(t, f.store, command, at)
	if result.Err != nil || !result.Allowed || result.Collection == nil || result.Collection.Execution == nil {
		t.Fatal("execution begin", result.Err)
	}
	head = result.Collection.Clone()
	p := candidateOpen(t, f, head, nil)
	candidate, committed, err := p.prepare(context.Background(), 1)
	if err != nil || committed {
		t.Fatal("prepare real original input", err)
	}
	if err = p.close(); err != nil {
		t.Fatal(err)
	}
	command.Action, command.Ordinal, command.Prepared = "prepare", 1, &candidate
	result = candidateExecutionSubmit(t, f.store, command, candidate.At)
	if result.Err != nil || !result.Allowed || result.Collection.Execution.Prepared == nil {
		t.Fatal("commit real candidate", result.Err)
	}
	return candidate, command, result.Collection.Clone()
}
func candidateExecutionDecision(c persistence.CollectionExecuteCommand) persistence.CollectionExecuteCommand {
	c.Action, c.PreparedID, c.Prepared = "decide", c.Prepared.ID, nil
	return c
}

func TestCollectionCandidateExecutionRealMonitorAndEncryptedRetry(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			var requests atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer target.Close()
			f, head, config := candidateExecutionFixture(t, disk, collectionMonitor("service", target.URL))
			beforeCatalog, _ := f.store.CatalogSnapshot()
			candidate, prepare, prepared := candidateExecutionPrepare(t, f, head)
			if after, _ := f.store.CatalogSnapshot(); after.Len() != beforeCatalog.Len() {
				t.Fatal("preparation changed active catalog")
			}
			exact := candidateExecutionSubmit(t, f.store, prepare, candidate.At.Add(time.Millisecond))
			if exact.Err != nil || !reflect.DeepEqual(exact.Collection, &prepared) || len(exact.Events) != 0 {
				t.Fatal("prepare retry replaced candidate", exact.Err)
			}
			// A committed retry must not depend on the currently available wrapping key.
			// This wrapper rejects all crypto. The helper still returns the exact original
			// authenticated ciphertext from the committed preparation slot.
			normalSealer := f.catalog.sealer
			inner, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{99}, 32))
			broken, err := secureconfig.NewSealer(candidateFailKeys{KeyWrapper: inner, failOpen: true})
			if err != nil {
				t.Fatal(err)
			}
			f.catalog.sealer = broken
			p := candidateOpen(t, f, prepared, nil)
			recovered, committed, err := p.prepare(context.Background(), 1)
			if err != nil || !committed || !candidateExecutionSameWire(recovered, candidate) {
				t.Fatal("committed preparation changed its canonical bytes or required crypto", err)
			}
			_ = p.close()
			f.catalog.sealer = normalSealer
			if disk {
				if err := f.store.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := persistence.Open(context.Background(), *config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				f.store = reopened
				f.catalog = candidateExecutionCatalog(t, reopened)
				after, ok, err := f.store.CollectionGet(head.ID)
				if err != nil || !ok || !reflect.DeepEqual(after, prepared) {
					t.Fatal("restart replaced prepared identity", err)
				}
				resumed := candidateOpen(t, f, after, nil)
				got, committed, err := resumed.prepare(context.Background(), 1)
				if err != nil || !committed || !candidateExecutionSameWire(got, candidate) {
					t.Fatal("snapshot candidate cannot reconcile", err)
				}
				_ = resumed.close()
			}
			decision := candidateExecutionDecision(prepare)
			accepted := candidateExecutionSubmit(t, f.store, decision, candidate.At.Add(time.Second))
			if accepted.Err != nil || !accepted.Allowed || accepted.Operation == nil || accepted.Operation.ID == head.ID || accepted.Collection.Execution.Processed != 1 || accepted.Collection.Execution.Accepted != 1 || accepted.Collection.Execution.Prepared != nil {
				t.Fatal("catalog conditional acceptance", accepted.Err)
			}
			stored, exists, err := f.store.CatalogGet(candidate.Record.Key)
			if err != nil || !exists || stored.UID != candidate.Record.UID || stored.Revision != candidate.Record.Revision || !reflect.DeepEqual(stored.Payload, candidate.Record.Payload) {
				t.Fatal("accepted catalog changed encrypted candidate", err)
			}
			decoded, err := f.catalog.open(context.Background(), stored)
			if err != nil {
				t.Fatal(err)
			}
			var spec api.MonitorSpec
			if err = json.Unmarshal(decoded.Spec, &spec); err != nil || spec.Check.Driver.Type != "http" || !bytes.Contains(spec.Check.Driver.Config, []byte(target.URL)) {
				t.Fatal("accepted monitor differs from original input", err)
			}
			raw, _ := json.Marshal(stored)
			if bytes.Contains(raw, []byte(target.URL)) {
				t.Fatal("plaintext target entered persisted catalog")
			}
			originalHead := accepted.Collection.Clone()
			originalChild, err := f.store.Operation(accepted.Operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			retry := candidateExecutionSubmit(t, f.store, decision, candidate.At.Add(2*time.Second))
			if retry.Err != nil || len(retry.Events) != 0 || retry.Operation != nil || !reflect.DeepEqual(retry.Collection, &originalHead) {
				t.Fatal("decision retry duplicated effect", retry.Err)
			}
			if disk {
				if err := f.store.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := persistence.Open(context.Background(), *config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				f.store = reopened
				f.catalog = candidateExecutionCatalog(t, reopened)
				retry = candidateExecutionSubmit(t, f.store, decision, candidate.At.Add(3*time.Second))
				if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(retry.Collection, &originalHead) {
					t.Fatal("accepted snapshot replay changed original outcome", retry.Err)
				}
			}
			child, err := f.store.Operation(accepted.Operation.ID)
			if err != nil || !reflect.DeepEqual(child, originalChild) || child.State != "committed" || requests.Load() != 0 {
				t.Fatal("acceptance invoked controller/provider or replaced original child", err)
			}
			pending, err := f.store.PendingOperations()
			if err != nil || len(pending) != 1 || pending[0].ID != child.ID {
				t.Fatal("retries allocated another child", err)
			}
		})
	}
}

func TestCollectionCandidateExecutionPreservesOmittedCredential(t *testing.T) {
	secret := "PRIVATE-EXECUTION-CREDENTIAL-CANARY"
	baseline := resource("Credential", "secret", api.CredentialSpec{Value: &secret})
	desired := resource("Credential", "secret", api.CredentialSpec{Description: api.Pointer("new purpose")})
	f, head := candidateFixture(t, desired, &baseline)
	old, _, _ := f.store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "secret"})
	candidate, prepare, _ := candidateExecutionPrepare(t, f, head)
	accepted := candidateExecutionSubmit(t, f.store, candidateExecutionDecision(prepare), candidate.At.Add(time.Second))
	if accepted.Err != nil || accepted.Operation == nil || accepted.Collection.Execution.Accepted != 1 {
		t.Fatal("credential update not accepted", accepted.Err)
	}
	stored, _, _ := f.store.CatalogGet(old.Key)
	if stored.UID != old.UID || stored.Revision == old.Revision || stored.Generation != old.Generation+1 || !stored.CreatedAt.Equal(old.CreatedAt) {
		t.Fatal("credential original identity lost")
	}
	r, err := f.catalog.open(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	var spec api.CredentialSpec
	if err = json.Unmarshal(r.Spec, &spec); err != nil || spec.Value == nil || *spec.Value != secret || spec.Description == nil || *spec.Description != "new purpose" {
		t.Fatal("original omitted secret changed", err)
	}
	public, err := f.catalog.Get(context.Background(), "Credential", "secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(public)
	storedBytes, _ := json.Marshal(stored)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(storedBytes, []byte(secret)) {
		t.Fatal("accepted credential became readable plaintext")
	}
}

func TestCollectionCandidateExecutionRechecksBeforeDecision(t *testing.T) {
	for _, mode := range []string{"cancel", "authority-expiry", "outside-target"} {
		t.Run(mode, func(t *testing.T) {
			f, head := candidateFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("original")}), nil)
			candidate, prepare, prepared := candidateExecutionPrepare(t, f, head)
			at := candidate.At.Add(time.Second)
			want := error(nil)
			switch mode {
			case "cancel":
				auth, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
				if err != nil {
					t.Fatal(err)
				}
				prepared = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}, ActivationAuthority: &auth, ActivationFence: persistence.CollectionActivationFenceFor(head)}, at)
				want = persistence.ErrCollectionConflict
			case "authority-expiry":
				policy, err := f.store.Authentication()
				if err != nil {
					t.Fatal(err)
				}
				at = policy.Principals[0].ExpiresAt
				want = persistence.ErrOperatorAuthorityDenied
			case "outside-target":
				createResource(t, f.catalog, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("outside")}))
			}
			before, _ := f.store.CatalogSnapshot()
			beforeTarget, beforeExists := before.Get(candidate.Record.Key)
			beforeChildren, err := f.store.PendingOperations()
			if err != nil {
				t.Fatal(err)
			}
			r := candidateExecutionSubmit(t, f.store, candidateExecutionDecision(prepare), at)
			if want != nil {
				if !errors.Is(r.Err, want) || r.Operation != nil {
					t.Fatal("stale authority/parent committed", r.Err)
				}
				after, _, _ := f.store.CollectionGet(head.ID)
				if !reflect.DeepEqual(after, prepared) {
					t.Fatal("denial changed prepared progress")
				}
			} else if r.Err != nil || !r.Allowed || r.Operation != nil || r.Collection.Execution.Conflicts != 1 || r.Collection.Execution.Accepted != 0 || r.Collection.Execution.Prepared != nil {
				t.Fatal("outside target was overwritten", r.Err)
			}
			after, _ := f.store.CatalogSnapshot()
			afterTarget, afterExists := after.Get(candidate.Record.Key)
			afterChildren, err := f.store.PendingOperations()
			if err != nil || after.Len() != before.Len() || afterExists != beforeExists || !reflect.DeepEqual(afterTarget, beforeTarget) || !reflect.DeepEqual(afterChildren, beforeChildren) {
				t.Fatal("denied decision changed catalog/child identity", err)
			}
		})
	}
}

// Canonical wire bytes intentionally treat omitted empty reference arrays alike.
// Encryption envelopes, IDs, timestamps and every serialized field must remain
// identical; a nil-vs-empty Go slice after Raft JSON replay is not re-encryption.
func candidateExecutionSameWire(a, b persistence.CollectionPreparedItem) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

func TestCollectionCandidateExecutionUnchangedNeedsNoCandidateOrChild(t *testing.T) {
	original := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("original")})
	f, head := candidateFixture(t, resource("Credential", "secret", api.CredentialSpec{}), &original)
	before, _, _ := f.store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "secret"})
	pendingBefore, err := f.store.PendingOperations()
	if err != nil {
		t.Fatal(err)
	}
	command := candidateExecutionCommand(t, f.store, head, "begin", head.Activation.At.Add(time.Second))
	started := candidateExecutionSubmit(t, f.store, command, head.Activation.At.Add(time.Second))
	if started.Err != nil || started.Collection == nil {
		t.Fatal(started.Err)
	}
	p := candidateOpen(t, f, *started.Collection, nil)
	keys := candidateObserveKeys(t, f.catalog)
	if _, _, err := p.prepare(context.Background(), 1); !errors.Is(err, errCollectionPreparationNotRequired) || keys.wraps != 0 || keys.unwraps != 0 {
		t.Fatal("unchanged original needed crypto", err)
	}
	_ = p.close()
	command.Action, command.Ordinal = "decide", 1
	result := candidateExecutionSubmit(t, f.store, command, head.Activation.At.Add(2*time.Second))
	if result.Err != nil || result.Operation != nil || result.Collection.Execution.Unchanged != 1 || result.Collection.Execution.Accepted != 0 || result.Collection.Execution.Prepared != nil {
		t.Fatal("unchanged conditional decision", result.Err)
	}
	after, _, _ := f.store.CatalogGet(before.Key)
	pendingAfter, err := f.store.PendingOperations()
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pendingBefore, pendingAfter) {
		t.Fatal("unchanged item allocated mutation or child", err)
	}
}
