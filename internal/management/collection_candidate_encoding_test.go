package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// This oracle uses independently created UUIDs and explicit expected generation;
// it does not call the production shape helper or reuse its placeholder.
func candidateEncodingActual(t *testing.T, r api.Resource, uid string, generation int64) []byte {
	t.Helper()
	r.Status = nil
	if uid == "" {
		uid = uuid.NewString()
	}
	r.Metadata.UID, r.Metadata.ResourceVersion, r.Metadata.Generation = uid, uuid.NewString(), generation
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCollectionCandidateEncodingExactCreateAndUpdateBoundaries(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, delta := range []int{-1, 0, 1} {
			name := "create"
			if update {
				name = "update"
			}
			t.Run(name+"/"+string(rune('1'+delta)), func(t *testing.T) {
				r := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("")})
				var old *api.Resource
				uid := ""
				generation := int64(1)
				if update {
					value := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("original")})
					value.Metadata.UID = "original-\"" + strings.Repeat("u", 150) + "\\identity"
					value.Metadata.ResourceVersion = "prior-revision"
					value.Metadata.Generation = 9
					old = &value
					uid = value.Metadata.UID
					generation = 10
				}
				value := strings.Repeat("x", api.MaxResourceBytes-len(candidateEncodingActual(t, r, uid, generation))+delta)
				r.Spec, _ = json.Marshal(api.CredentialSpec{Value: &value})
				before, _ := json.Marshal(r)
				var oldBefore []byte
				if old != nil {
					oldBefore, _ = json.Marshal(old)
				}
				expected := candidateEncodingActual(t, r, uid, generation)
				if len(expected) != api.MaxResourceBytes+delta {
					t.Fatal("independent fixture size", len(expected))
				}
				shape, err := collectionCandidateEncoding(r, old)
				if delta > 0 {
					if !errors.Is(err, ErrValidation) || shape != (collectionCandidateLayout{}) {
						t.Fatal("oversize accepted", err)
					}
				} else if err != nil || !shape.Changed || shape.Generation != generation || shape.EncodedBytes != len(expected) {
					t.Fatal("incorrect final shape", shape, err)
				}
				after, _ := json.Marshal(r)
				if !bytes.Equal(before, after) {
					t.Fatal("mutated desired resource")
				}
				if old != nil {
					after, _ := json.Marshal(old)
					if !bytes.Equal(oldBefore, after) {
						t.Fatal("mutated original resource")
					}
				}
			})
		}
	}
}
func TestCollectionCandidateEncodingGenerationAndUnchanged(t *testing.T) {
	old := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("original")})
	old.Metadata.UID, old.Metadata.ResourceVersion, old.Metadata.Generation = "original-uid", "r", math.MaxInt64
	t.Run("name-only", func(t *testing.T) {
		desired := old
		desired.Metadata.Name = api.Pointer("new display name")
		shape, err := collectionCandidateEncoding(desired, &old)
		if err != nil || !shape.Changed || shape.Generation != math.MaxInt64 || shape.EncodedBytes != len(candidateEncodingActual(t, desired, old.Metadata.UID, math.MaxInt64)) {
			t.Fatal("name update incremented spec generation", shape, err)
		}
	})
	t.Run("spec-overflow", func(t *testing.T) {
		desired := old
		desired.Spec = json.RawMessage(`{"value":"changed"}`)
		if _, err := collectionCandidateEncoding(desired, &old); !errors.Is(err, ErrValidation) {
			t.Fatal("generation overflow accepted", err)
		}
	})
	t.Run("unchanged-without-replacement-headroom", func(t *testing.T) {
		original := old
		raw, _ := json.Marshal(original)
		value := strings.Repeat("x", api.MaxResourceBytes-len(raw)+len("original"))
		original.Spec, _ = json.Marshal(api.CredentialSpec{Value: &value})
		raw, _ = json.Marshal(original)
		if len(raw) != api.MaxResourceBytes {
			t.Fatal("original is not at resource limit", len(raw))
		}
		if len(candidateEncodingActual(t, original, original.Metadata.UID, math.MaxInt64)) <= api.MaxResourceBytes {
			t.Fatal("fixture has replacement headroom")
		}
		shape, err := collectionCandidateEncoding(original, &original)
		if err != nil || shape.Changed || shape.Generation != math.MaxInt64 || shape.EncodedBytes != 0 {
			t.Fatal("unchanged allocated replacement headroom", shape, err)
		}
	})
}
func TestCollectionCandidateEncodingValidationParityAndSourceIssue(t *testing.T) {
	for _, mode := range []string{"create-overflow", "omitted-update-overflow", "omitted-name-update", "omitted-unchanged"} {
		t.Run(mode, func(t *testing.T) {
			var baseline *api.Resource
			desired := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("")})
			if mode == "create-overflow" {
				raw, _ := json.Marshal(desired)
				value := strings.Repeat("x", api.MaxResourceBytes-len(raw)-16)
				desired.Spec, _ = json.Marshal(api.CredentialSpec{Value: &value})
			} else {
				old := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("")})
				value := strings.Repeat("x", api.MaxResourceBytes-len(candidateEncodingActual(t, old, "", 1))-16)
				old.Spec, _ = json.Marshal(api.CredentialSpec{Value: &value})
				baseline = &old
				desired.Spec = json.RawMessage(`{}`)
				if mode == "omitted-update-overflow" {
					desired.Spec, _ = json.Marshal(api.CredentialSpec{Description: api.Pointer(strings.Repeat("d", 64))})
				}
				if mode == "omitted-name-update" {
					desired.Metadata.Name = api.Pointer("x")
				}
			}
			f := stagedValidationFixture(t, desired)
			if baseline != nil {
				createResource(t, f.catalog, *baseline)
			}
			wrapper := collectionBlockSealing(t, f.catalog)
			result, err := stagedValidationParity(t, &f, []api.Resource{desired})
			bad := strings.HasSuffix(mode, "overflow")
			if bad {
				if !errors.Is(err, ErrValidation) || result.Valid || len(result.Items) != 1 || result.Items[0].Issue != "invalidResource" || result.Items[0].SourceID == "" || result.Items[0].ItemID == "" {
					t.Fatal("headroom did not reject original source before success", result, err)
				}
			} else if err != nil || !result.Valid || (mode == "omitted-unchanged" && result.Items[0].Change != "unchanged") || (mode == "omitted-name-update" && result.Items[0].Change != "update") {
				t.Fatal("valid original omission rejected", result, err)
			}
			if wrapper.wraps.Load() != 0 {
				t.Fatal("validation minted encrypted candidate")
			}
			head, _, _ := f.store.CollectionGet(f.head.ID)
			if !reflect.DeepEqual(head, f.head) || head.Plan != nil || head.Validation != nil {
				t.Fatal("pure validation changed original state")
			}
		})
	}
}

func TestCollectionCandidateEncodingPolicyTwoRejectsOldAttempt(t *testing.T) {
	base, contract, kinds, caps, models := profileInputs(t)
	if collectionValidationPolicyVersion != 2 || base.ValidationPolicyVersion != 2 {
		t.Fatal("headroom policy was not versioned")
	}
	base.ValidationPolicyVersion = 1
	_, oldDigest, err := buildCollectionValidationProfile(base, contract, kinds, caps, models)
	if err != nil {
		t.Fatal(err)
	}
	_, currentDigest, err := collectionValidationProfile()
	if err != nil || currentDigest == oldDigest {
		t.Fatal("old and new profile identities match", err)
	}
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("compatible-small-input")}))
	at := f.at.Add(time.Second)
	authority, err := f.store.ObserveOperatorAuthority(context.Background(), f.head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := persistence.CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: f.head.ProgressDigest, ItemCount: f.head.ItemCount, Authority: authority, CapabilitiesDigest: oldDigest, RequestedAt: at}
	original := submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_request", OperationID: f.head.ID, UploadID: f.head.UploadID, ValidationRequest: &oldRequest}, at)
	wrapper := collectionBlockSealing(t, f.catalog)
	if _, err := f.catalog.runCollectionValidation(context.Background(), original.ID, uuid.NewString(), collectionClock(at.Add(time.Second))); !errors.Is(err, persistence.ErrCollectionConflict) {
		t.Fatal("old policy attempt silently recompiled", err)
	}
	after, _, _ := f.store.CollectionGet(original.ID)
	if !reflect.DeepEqual(after, original) || wrapper.opens.Load() != 0 || wrapper.wraps.Load() != 0 {
		t.Fatal("old attempt changed or accessed secrets")
	}

	// This explicitly constructs compatible historical metadata through durable
	// commands. It does not execute an old compiler or certify old binary output.
	// The small input is valid in both policies; the original profile remains 1.
	head := candidateEncodingAdmitHistoricalProfile(t, f, original)
	wrapper = collectionBlockSealing(t, f.catalog)
	p, err := newCollectionCandidatePreparation(context.Background(), f.catalog, head.ID, head.Actor,
		func(persistence.CatalogKey) bool { return true }, collectionClock(head.Activation.At.Add(time.Second)))
	if !errors.Is(err, persistence.ErrCollectionConflict) || p != nil {
		t.Fatal("old sealed policy silently adopted for candidate preparation", err)
	}
	after, _, _ = f.store.CollectionGet(head.ID)
	if !reflect.DeepEqual(after, head) || wrapper.opens.Load() != 0 || wrapper.wraps.Load() != 0 {
		t.Fatal("old sealed artifact changed or accessed secrets")
	}
	_, count := candidateCatalogState(t, f)
	if count != 0 || head.Execution != nil {
		t.Fatal("old profile activated a catalog item")
	}
}

func candidateEncodingAdmitHistoricalProfile(t *testing.T, f collectionSourceFixture, head persistence.CollectionState) persistence.CollectionState {
	t.Helper()
	ctx := context.Background()
	at := head.ValidationRequest.RequestedAt.Add(2 * time.Second)
	claim := persistence.CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}
	head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_claim", OperationID: head.ID,
		UploadID: head.UploadID, ValidationFence: &persistence.CollectionValidationRequestFence{RequestID: head.ValidationRequest.ID}, ValidationClaim: &claim}, at)
	fence := persistence.CollectionValidationRequestFenceFor(head)
	source, err := newCollectionValidationSourceForAttempt(ctx, f.catalog, head.ID, fence, collectionReadAll.CanRead, collectionClock(at))
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	view, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	validation, plan, validationErr := f.catalog.compileStagedCollectionPlan(ctx, view, source, collectionReadAll)
	if validationErr != nil || !validation.Valid {
		t.Fatal("compatible historical fixture failed current validation", validationErr)
	}
	result, err := prepareCollectionValidationArtifact(ctx, source, validation, validationErr)
	if err != nil {
		t.Fatal(err)
	}
	defer result.close()
	artifact, err := prepareCollectionPlanArtifact(ctx, plan, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	source.close()
	commit := func(command persistence.CollectionCommand) {
		at = at.Add(time.Millisecond)
		command.OperationID, command.UploadID = head.ID, head.UploadID
		if command.Action != "validation_publish" {
			command.ValidationFence = fence
		}
		head = submitStagedSource(t, f.store, command, at)
	}
	commit(persistence.CollectionCommand{Action: "plan_begin", PlanBegin: &persistence.CollectionPlanBegin{Header: artifact.header, Descriptor: artifact.descriptor}})
	var ordinal uint64
	if err := visitCollectionPlanArtifact(ctx, artifact, func(fragment persistence.CollectionPlanFragment) error {
		ordinal++
		commit(persistence.CollectionCommand{Action: "plan_append", PlanID: artifact.header.PlanID,
			PlanFragment: &persistence.CollectionPlanLedgerFragment{Ordinal: ordinal, Fragment: fragment}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := f.store.VerifyCollectionPlan(ctx, head.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	commit(persistence.CollectionCommand{Action: "plan_finalize", PlanFinalize: &proof})
	header := persistence.CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID,
		InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Authority: head.ValidationRequest.Authority,
		CapabilitiesDigest: head.ValidationRequest.CapabilitiesDigest, Valid: true, PlanID: artifact.header.PlanID, PlanDigest: artifact.descriptor.Digest}
	commit(persistence.CollectionCommand{Action: "validation_begin", ValidationBegin: &persistence.CollectionValidationBegin{Header: header, Descriptor: result.descriptor}})
	if err := result.emit(ctx, func(items []persistence.CollectionValidationItem) error {
		commit(persistence.CollectionCommand{Action: "validation_append", ValidationID: header.ResultID, ValidationItems: items})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commit(persistence.CollectionCommand{Action: "validation_finalize", ValidationID: header.ResultID})
	for !head.Validation.HistorySealed {
		commit(persistence.CollectionCommand{Action: "validation_publish", ValidationID: header.ResultID, ValidationPublished: head.Validation.Published})
	}
	at = at.Add(time.Millisecond)
	authority, err := f.store.ObserveOperatorAuthority(ctx, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest,
		ItemCount: head.ItemCount, ResultID: header.ResultID, ResultDescriptor: head.Validation.Descriptor,
		PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: header.CapabilitiesDigest,
		ValidationRequest: fence, At: at}
	return submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID,
		UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &authority}, at)
}
