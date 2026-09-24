//go:build externaljobs

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
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func testJobType() api.JobType {
	return api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "custom-check"}, Spec: api.JobTypeSpec{Kind: "check", Version: "1", Handler: "custom-check", ProtocolVersion: "1", Timeout: "5s", ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target"],"properties":{"target":{"type":"string","minLength":1,"maxLength":256}}}`), ResultSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}}
}

func jobTypeCatalog(t *testing.T) (*Catalog, *persistence.Store) {
	t.Helper()
	c, store := testCatalog(t)
	if _, err := store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	return c, store
}

func commitTestJobType(t *testing.T, c *Catalog, input api.JobType, version string, create bool) api.JobType {
	t.Helper()
	prepared, err := c.PrepareJobType(t.Context(), input, version, create, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.CommitJobType(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.ResourceVersion == "" || result.Metadata.UID == "" {
		t.Fatal("missing committed identity")
	}
	if _, err = c.CommitJobType(t.Context(), prepared); !errors.Is(err, ErrValidation) {
		t.Fatal("prepared mutation reused", err)
	}
	return result
}

func TestJobTypeCatalogVersionRetentionAndReopen(t *testing.T) {
	c, store := jobTypeCatalog(t)
	r := testJobType()
	r.Spec.ResultSchema = json.RawMessage(`{"const":"job-schema-private-canary"}`)
	r = commitTestJobType(t, c, r, "", true)
	first, ok, err := store.JobType(t.Context(), r.Metadata.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	r.Metadata.Name = api.Pointer("Renamed handler")
	r = commitTestJobType(t, c, r, r.Metadata.ResourceVersion, false)
	second, ok, err := store.JobType(t.Context(), r.Metadata.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Versions, second.Versions) {
		t.Fatal("metadata replacement rewrote immutable version")
	}
	r.Spec.Version = "2"
	r.Spec.Timeout = "10s"
	r = commitTestJobType(t, c, r, r.Metadata.ResourceVersion, false)
	deleted, err := c.PrepareDeleteJobType(t.Context(), r.Metadata.ID, r.Metadata.ResourceVersion, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.CommitJobType(t.Context(), deleted); err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetJobType(t.Context(), r.Metadata.ID); !errors.Is(err, persistence.ErrCatalogNotFound) {
		t.Fatal("tombstone readable", err)
	}
	newIncarnation := testJobType()
	newIncarnation.Spec.Version = "3"
	newIncarnation = commitTestJobType(t, c, newIncarnation, "", true)
	if newIncarnation.Metadata.UID == r.Metadata.UID {
		t.Fatal("recreation reused incarnation")
	}
	state, ok, err := store.JobType(t.Context(), r.Metadata.ID)
	if err != nil || !ok || len(state.Versions) != 3 {
		t.Fatal("versions lost", err)
	}
	if !reflect.DeepEqual(first.Versions["1"], state.Versions["1"]) {
		t.Fatal("retained original ciphertext changed")
	}
	raw, _ := json.Marshal(state)
	if bytes.Contains(raw, []byte("job-schema-private-canary")) {
		t.Fatal("plaintext schema entered persistent state")
	}
	reopened, err := NewCatalog(store, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Verify(t.Context()); err != nil {
		t.Fatal("startup verification failed", err)
	}
	got, err := reopened.GetJobType(t.Context(), r.Metadata.ID)
	if err != nil || got.Metadata.UID != newIncarnation.Metadata.UID {
		t.Fatal("reopened catalog mismatch", err)
	}
}

func TestJobTypeCatalogRejectsChangedContractAndStaleAuthority(t *testing.T) {
	c, store := jobTypeCatalog(t)
	r := testJobType()
	r.Spec.ParameterSchema = json.RawMessage(`{"const":9007199254740993}`)
	r = commitTestJobType(t, c, r, "", true)
	index := store.Status().CommittedIndex
	r.Spec.ParameterSchema = json.RawMessage(`{"const":9007199254740992}`)
	if _, err := c.PrepareJobType(t.Context(), r, r.Metadata.ResourceVersion, false, "team/operator"); !errors.Is(err, persistence.ErrJobTypeVersionConflict) {
		t.Fatal("numeric contract change accepted", err)
	}
	if store.Status().CommittedIndex != index {
		t.Fatal("rejected preparation wrote durable state")
	}
	r.Spec.ParameterSchema = json.RawMessage(`{"const":9007199254740993.0}`)
	prepared, err := c.PrepareJobType(t.Context(), r, r.Metadata.ResourceVersion, false, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	// Another admitted writer changes the resource after preparation.
	current, err := c.GetJobType(t.Context(), r.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Metadata.Name = api.Pointer("concurrent update")
	commitTestJobType(t, c, current, current.Metadata.ResourceVersion, false)
	if _, err = c.CommitJobType(t.Context(), prepared); !errors.Is(err, persistence.ErrJobTypeConflict) {
		t.Fatal("stale replacement committed", err)
	}
	if _, err = c.PrepareJobType(t.Context(), testJobType(), "", true, "unprovisioned"); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("missing operator accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.PrepareJobType(ctx, testJobType(), "", true, "team/operator"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestJobTypeCatalogInvalidContractsHaveNoEffects(t *testing.T) {
	c, store := jobTypeCatalog(t)
	for _, change := range []func(*api.JobType){
		func(r *api.JobType) { r.Metadata.ID = "http" },
		func(r *api.JobType) { r.Metadata.ID = "kubernetes" },
		func(r *api.JobType) { r.Spec.Handler = "../handler" },
		func(r *api.JobType) { r.Spec.ProtocolVersion = "2" },
		func(r *api.JobType) { r.Spec.Timeout = "25h" },
		func(r *api.JobType) { r.Metadata.UID = strings.Repeat("x", 257) },
		func(r *api.JobType) { r.Metadata.ResourceVersion = strings.Repeat("x", 257) },
		func(r *api.JobType) { r.Spec.Timeout = strings.Repeat("0", 65) + "s" },
		func(r *api.JobType) { r.Spec.RejectionCodes = []string{"rejected", "rejected"} },
		func(r *api.JobType) {
			r.Spec.ParameterSchema = json.RawMessage(`{"$ref":"https://secret-canary.invalid/schema"}`)
		},
	} {
		r := testJobType()
		change(&r)
		index := store.Status().CommittedIndex
		_, err := c.PrepareJobType(t.Context(), r, "", true, "team/operator")
		if err == nil || strings.Contains(fmt.Sprint(err), "secret-canary") {
			t.Fatal("invalid contract admitted or leaked input", err)
		}
		if store.Status().CommittedIndex != index {
			t.Fatal("invalid contract changed state")
		}
	}
}

type jobExpiryWrapper struct {
	secureconfig.KeyWrapper
	until time.Time
	calls int
}

func (w *jobExpiryWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	w.calls++
	timer := time.NewTimer(time.Until(w.until))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return w.KeyWrapper.Wrap(ctx, key, aad)
}

func TestJobTypePreparationExpiryDuringSeal(t *testing.T) {
	c, store := testCatalog(t)
	at := time.Now().UTC()
	policy := collectionOwnerBootstrap(at)
	policy.Principals[0].ExpiresAt = at.Add(time.Second)
	if _, err := store.CommitAuthentication(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	local, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &jobExpiryWrapper{KeyWrapper: local, until: policy.Principals[0].ExpiresAt}
	c.sealer, err = secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	index := store.Status().CommittedIndex
	if _, err = c.PrepareJobType(t.Context(), testJobType(), "", true, "team/operator"); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("expired preparation returned sealed work", err)
	}
	if wrapper.calls != 1 {
		t.Fatal("fixture did not expire during wrapping")
	}
	if store.Status().CommittedIndex != index {
		t.Fatal("expired encryption preparation wrote state")
	}
}

func TestJobTypeCatalogRevokedPreparationAcrossRestartAndRedactedFormatting(t *testing.T) {
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	r := testJobType()
	r.Spec.ResultSchema = json.RawMessage(`{"const":"secret-format-canary"}`)
	prepared, err := c.PrepareJobType(t.Context(), r, "", true, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{prepared, *prepared} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if fmt.Sprintf(format, value) != "prepared job type (configuration omitted)" {
				t.Fatal("prepared value formatting leaked fields")
			}
		}
		if _, err = json.Marshal(value); err == nil {
			t.Fatal("prepared value serialized")
		}
	}
	if _, err = json.Marshal(prepared); err == nil {
		t.Fatal("prepared object serializes")
	}
	policy, err := store.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	policy.Principals[0].Revoked = true
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err := persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	_, err = admin.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "replace", ExpectedEpoch: policy.Epoch, ExpectedRevision: policy.Revision, Epoch: policy.Epoch, Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(), Principals: policy.Principals})
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	at := time.Now().UTC()
	prepared.command.Value.Record.CreatedAt, prepared.command.Value.Record.UpdatedAt = at, at
	if _, err = reopened.CommitJobType(t.Context(), prepared.command, at); !errors.Is(err, persistence.ErrAuthenticationConflict) && !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		t.Fatal("revoked preparation admitted", err)
	}
	if _, ok, err := reopened.JobType(t.Context(), r.Metadata.ID); err != nil || ok {
		t.Fatal("rejected preparation retained a type", err)
	}
}

func TestJobTypeStartupAuthenticatesMetadataVersionProof(t *testing.T) {
	c, store := jobTypeCatalog(t)
	r := commitTestJobType(t, c, testJobType(), "", true)
	p, err := c.PrepareJobType(t.Context(), r, r.Metadata.ResourceVersion, false, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	// The trusted preparation boundary is not a caller assertion. A forged
	// internal command can contain different ciphertext with the same selectors;
	// startup must authenticate its spec against the retained immutable version.
	forged := p.resource
	forged.Spec.ParameterSchema = json.RawMessage(`{"const":"changed"}`)
	raw, _ := json.Marshal(forged)
	p.command.Value.Record.Payload, err = c.sealer.Seal(t.Context(), p.command.Value.Record.Binding(c.storeID), raw)
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.CommitJobType(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCatalog(store, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Verify(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("changed retained contract passed startup", err)
	}
	if reopened.Ready() {
		t.Fatal("corrupt type catalog ready")
	}
}
