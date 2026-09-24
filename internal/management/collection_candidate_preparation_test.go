package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Every fixture uploads actual encrypted inputs, compiles the original plan,
// finalizes/publishes its verdict and admits activation through Store. Preparing
// below never submits a candidate or changes the active catalog.
func candidateFixture(t *testing.T, desired api.Resource, baseline *api.Resource) (collectionSourceFixture, persistence.CollectionState) {
	t.Helper()
	f := coordinatorFixture(t, desired)
	if baseline != nil {
		createResource(t, f.catalog, *baseline)
	}
	coordinatorRequest(t, &f)
	head, err := f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if err != nil || head.Validation == nil || !head.Validation.Header.Valid {
		t.Fatal("compile original plan", err)
	}
	at := f.at.Add(3 * time.Second)
	for !head.Validation.HistorySealed {
		head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, at)
	}
	auth, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := &persistence.CollectionActivation{ID: uuid.NewString(), Authority: auth, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest, ValidationRequest: persistence.CollectionValidationRequestFenceFor(head), At: at}
	head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: activation, ActivationAuthority: &auth}, at)
	return f, head
}
func candidateOpen(t *testing.T, f collectionSourceFixture, head persistence.CollectionState, canWrite func(persistence.CatalogKey) bool) *collectionCandidatePreparation {
	t.Helper()
	if canWrite == nil {
		canWrite = func(persistence.CatalogKey) bool { return true }
	}
	p, err := newCollectionCandidatePreparation(context.Background(), f.catalog, head.ID, head.Actor, canWrite, collectionClock(head.Activation.At.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	return p
}
func candidateObserveKeys(t *testing.T, c *Catalog) *uploadObservedWrapper {
	t.Helper()
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	w := &uploadObservedWrapper{KeyWrapper: inner}
	c.sealer, err = secureconfig.NewSealer(w)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func candidateCatalogState(t *testing.T, f collectionSourceFixture) (uint64, int) {
	t.Helper()
	v, err := f.store.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return v.Index, v.Len()
}
func TestCollectionCandidatePreparationCreateRetryAndPrivacy(t *testing.T) {
	secret := "PRIVATE-CANDIDATE-CANARY"
	f, head := candidateFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: &secret}), nil)
	before, count := candidateCatalogState(t, f)
	keys := candidateObserveKeys(t, f.catalog)
	p := candidateOpen(t, f, head, nil)
	candidate, committed, err := p.prepare(context.Background(), 1)
	if err != nil || committed {
		t.Fatal("prepare", err)
	}
	if keys.wraps != 1 || keys.unwraps != 2 {
		t.Fatal("unexpected crypto counts", keys.wraps, keys.unwraps)
	}
	r, err := f.catalog.open(context.Background(), candidate.Record)
	if err != nil {
		t.Fatal(err)
	}
	var spec api.CredentialSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil || spec.Value == nil || *spec.Value != secret {
		t.Fatal("original value lost", err)
	}
	if r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation != 1 || candidate.Record.Key.ID != "secret" || candidate.Record.CreatedAt != candidate.At || candidate.Record.UpdatedAt != candidate.At {
		t.Fatal("invalid generated identity")
	}
	opens := keys.unwraps
	again, committed, err := p.prepare(context.Background(), 1)
	if err != nil || committed || !reflect.DeepEqual(again, candidate) || keys.wraps != 1 || keys.unwraps != opens {
		t.Fatal("retry regenerated encrypted candidate", err)
	}
	candidate.Record.Payload.Ciphertext[0] ^= 1
	again2, _, err := p.prepare(context.Background(), 1)
	if err != nil || !reflect.DeepEqual(again2, again) {
		t.Fatal("returned candidate aliases private retry", err)
	}
	raw, _ := json.Marshal(again)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("plaintext serialized")
	}
	for _, s := range []string{fmt.Sprintf("%v", p), fmt.Sprintf("%#v", p), fmt.Sprintf("%+v", p)} {
		if strings.Contains(s, secret) || strings.Contains(s, head.ID) {
			t.Fatal("private holder formatting leaked")
		}
	}
	if _, err := json.Marshal(p); err == nil {
		t.Fatal("private holder serialized")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if p.keyLoaded || p.cached != nil || p.key != [32]byte{} {
		t.Fatal("close retained key/candidate")
	}
	if _, _, err := p.prepare(context.Background(), 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("closed preparation usable", err)
	}
	after, afterCount := candidateCatalogState(t, f)
	latest, _, _ := f.store.CollectionGet(head.ID)
	if before != after || count != afterCount || !reflect.DeepEqual(latest, head) || latest.Execution != nil {
		t.Fatal("preparation changed committed state")
	}
}
func TestCollectionCandidatePreparationPreservesOriginalCredentialAndGeneration(t *testing.T) {
	for _, change := range []string{"labels", "spec", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			value := "ORIGINAL-CREDENTIAL-CANARY"
			old := resource("Credential", "key", api.CredentialSpec{Value: &value})
			desired := resource("Credential", "key", api.CredentialSpec{})
			switch change {
			case "labels":
				desired.Metadata.Labels = api.Pointer(map[string]string{"team": "oncall"})
			case "spec":
				desired.Spec, _ = json.Marshal(api.CredentialSpec{Description: api.Pointer("updated description")})
			}
			f, head := candidateFixture(t, desired, &old)
			original, ok, err := f.store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "key"})
			if err != nil || !ok {
				t.Fatal(err)
			}
			keys := candidateObserveKeys(t, f.catalog)
			p := candidateOpen(t, f, head, nil)
			row, _, err := p.view.Row(context.Background(), 1, p.now())
			if err != nil || row.Target.ReverseVersion == nil {
				t.Fatal("update row missing original reverse token", err)
			}
			originalToken := *row.Target.ReverseVersion
			*row.Target.ReverseVersion++
			againRow, _, err := p.view.Row(context.Background(), 1, p.now())
			if err != nil || againRow.Target.ReverseVersion == nil || *againRow.Target.ReverseVersion != originalToken {
				t.Fatal("row target aliases detached holder", err)
			}
			candidate, _, err := p.prepare(context.Background(), 1)
			if change == "unchanged" {
				if !errors.Is(err, errCollectionPreparationNotRequired) || keys.wraps != 0 || keys.unwraps != 0 {
					t.Fatal("unchanged item did work", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := f.catalog.open(context.Background(), candidate.Record)
			if err != nil {
				t.Fatal(err)
			}
			var spec api.CredentialSpec
			if err = json.Unmarshal(r.Spec, &spec); err != nil || spec.Value == nil || *spec.Value != value {
				t.Fatal("omitted original credential was not preserved", err)
			}
			want := original.Generation
			if change == "spec" {
				want++
			}
			if candidate.Record.UID != original.UID || candidate.Record.Revision == original.Revision || candidate.Record.Generation != want || !candidate.Record.CreatedAt.Equal(original.CreatedAt) {
				t.Fatal("replaced original incarnation or incorrect generation")
			}
			current, _, _ := f.store.CatalogGet(original.Key)
			if !reflect.DeepEqual(current, original) {
				t.Fatal("preparation overwrote catalog")
			}
		})
	}
}
func TestCollectionCandidatePreparationPermissionAndTargetBeforeCrypto(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}), nil)
		keys := candidateObserveKeys(t, f.catalog)
		p := candidateOpen(t, f, head, func(persistence.CatalogKey) bool { return false })
		if _, _, err := p.prepare(context.Background(), 1); !errors.Is(err, errCollectionReadDenied) || keys.wraps != 0 || keys.unwraps != 0 {
			t.Fatal("denied item reached crypto", err)
		}
	})
	t.Run("outside-create", func(t *testing.T) {
		f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("original")}), nil)
		createResource(t, f.catalog, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("outside")}))
		keys := candidateObserveKeys(t, f.catalog)
		p := candidateOpen(t, f, head, nil)
		if _, _, err := p.prepare(context.Background(), 1); !errors.Is(err, persistence.ErrCatalogConflict) || keys.wraps != 0 || keys.unwraps != 0 {
			t.Fatal("outside target was rebased", err)
		}
	})
}
func TestCollectionCandidatePreparationCryptoBoundaryFences(t *testing.T) {
	for _, mode := range []string{"cancel-context", "cancel-parent", "outside-create"} {
		t.Run(mode, func(t *testing.T) {
			f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("original")}), nil)
			keys := candidateObserveKeys(t, f.catalog)
			p := candidateOpen(t, f, head, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var want error
			keys.beforeWrap = func(context.Context) {
				keys.beforeWrap = nil
				switch mode {
				case "cancel-context":
					cancel()
					want = context.Canceled
				case "cancel-parent":
					at := head.Activation.At.Add(time.Second)
					auth, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
					if err != nil {
						t.Fatal(err)
					}
					submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}, ActivationAuthority: &auth, ActivationFence: &persistence.CollectionActivationFence{ID: head.Activation.ID, ResultID: head.Activation.ResultID, PlanID: head.Activation.PlanID}}, at)
					want = persistence.ErrCollectionConflict
				case "outside-create":
					createResource(t, f.catalog, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("outside")}))
					want = persistence.ErrCatalogConflict
				}
			}
			if candidate, _, err := p.prepare(ctx, 1); want == nil || !errors.Is(err, want) || candidate.ID != "" || p.cached != nil {
				t.Fatal("crypto boundary accepted stale candidate", err)
			}
		})
	}
}
func TestCollectionCandidatePreparationExactFinalEncodedLimit(t *testing.T) {
	desired := resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("")})
	// Measure with independently assigned real UUID fields, rather than the
	// production size helper, then fill exactly the final wire limit.
	probe := desired
	probe.Metadata.UID, probe.Metadata.ResourceVersion, probe.Metadata.Generation = uuid.NewString(), uuid.NewString(), 1
	raw, _ := json.Marshal(probe)
	value := strings.Repeat("a", api.MaxResourceBytes-len(raw))
	desired.Spec, _ = json.Marshal(api.CredentialSpec{Value: &value})
	f, head := candidateFixture(t, desired, nil)
	keys := candidateObserveKeys(t, f.catalog)
	p := candidateOpen(t, f, head, nil)
	candidate, _, err := p.prepare(context.Background(), 1)
	if err != nil || candidate.ID == "" || keys.wraps != 1 {
		t.Fatal("exact final resource limit rejected", err)
	}
	decoded, err := f.catalog.open(context.Background(), candidate.Record)
	if err != nil {
		t.Fatal(err)
	}
	final, err := json.Marshal(decoded)
	if err != nil || len(final) != api.MaxResourceBytes {
		t.Fatal("fixture is not exactly final limit", len(final), err)
	}
}

func TestCollectionCandidatePreparationUnwrapRechecksAuthorityAndPermission(t *testing.T) {
	for _, mode := range []string{"context", "permission", "parent"} {
		t.Run(mode, func(t *testing.T) {
			f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("original")}), nil)
			allowed := true
			p := candidateOpen(t, f, head, func(persistence.CatalogKey) bool { return allowed })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			opens := 0
			var want error
			wrapper := sourceBeforeUnwrap{KeyWrapper: inner, before: func(context.Context) {
				opens++
				switch mode {
				case "context":
					cancel()
					want = context.Canceled
				case "permission":
					allowed = false
					want = errCollectionReadDenied
				case "parent":
					at := head.Activation.At.Add(time.Second)
					auth, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
					if err != nil {
						t.Fatal(err)
					}
					submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}, ActivationAuthority: &auth, ActivationFence: persistence.CollectionActivationFenceFor(head)}, at)
					want = persistence.ErrCollectionConflict
				}
			}}
			f.catalog.sealer, err = secureconfig.NewSealer(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			if got, _, err := p.prepare(ctx, 1); want == nil || !errors.Is(err, want) || got.ID != "" || opens != 1 {
				t.Fatal("continued decryption after invalidation", opens, err)
			}
		})
	}
}

type candidateFailKeys struct {
	secureconfig.KeyWrapper
	failOpen bool
}

func (w candidateFailKeys) Unwrap(ctx context.Context, b, a []byte) ([]byte, error) {
	if w.failOpen {
		return nil, errors.New("PRIVATE-PROVIDER-ERROR-CANARY")
	}
	return w.KeyWrapper.Unwrap(ctx, b, a)
}
func (w candidateFailKeys) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("PRIVATE-PROVIDER-ERROR-CANARY")
}
func TestCollectionCandidatePreparationSanitizesCryptoFailures(t *testing.T) {
	for _, open := range []bool{false, true} {
		t.Run(fmt.Sprint(open), func(t *testing.T) {
			f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}), nil)
			p := candidateOpen(t, f, head, nil)
			inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			f.catalog.sealer, err = secureconfig.NewSealer(candidateFailKeys{KeyWrapper: inner, failOpen: open})
			if err != nil {
				t.Fatal(err)
			}
			candidate, _, err := p.prepare(context.Background(), 1)
			if !errors.Is(err, ErrUnavailable) || strings.Contains(fmt.Sprint(err), "CANARY") || candidate.ID != "" || p.cached != nil {
				t.Fatal("crypto failure leaked or prepared candidate", err)
			}
		})
	}
}
func TestCollectionCandidatePreparationAuthorityExpiresDuringHeaderUnwrap(t *testing.T) {
	f, head := candidateFixture(t, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private")}), nil)
	p := candidateOpen(t, f, head, nil)
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	f.catalog.sealer, err = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: inner, before: func(context.Context) {
		opens++
		// Policy replacement requires stopped administration. Expiration is a live,
		// authoritative boundary: advance only the injected observation clock.
		p.now = collectionClock(policy.Principals[0].ExpiresAt)
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := p.prepare(context.Background(), 1)
	if !errors.Is(err, persistence.ErrOperatorAuthorityDenied) || candidate.ID != "" || opens != 1 {
		t.Fatal("expired actor continued original input decryption", opens, err)
	}
}
