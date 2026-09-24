package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// Commits actual authenticated encrypted input without using management upload
// validation. Uploaded fixtures model bytes admitted before profile enforcement;
// they do not claim that a historical compiler accepted the resource.
func fileProfileFixture(t *testing.T, input api.Resource, profile string, uploaded bool) *uploadPreparationFixture {
	t.Helper()
	c, store := testCatalog(t)
	at := time.Now().UTC()
	policy, err := store.CommitAuthentication(context.Background(), collectionOwnerBootstrap(at.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(raw) })
	key := bytes.Repeat([]byte{41}, commitment.KeyBytes)
	defer clear(key)
	fingerprint := [commitment.MACBytes]byte{73, 11}
	item := persistence.CollectionItem{Ordinal: 1, Key: persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: 1}
	mac, err := commitment.ItemMAC(key, itemPosition(item), raw)
	if err != nil {
		t.Fatal(err)
	}
	item.ContentDigest = hex.EncodeToString(mac[:])
	acc, err := commitment.NewAccumulator(key, 1, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if err = acc.Add(itemPosition(item), mac); err != nil {
		t.Fatal(err)
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "team/operator", Owner: &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: "team/operator"}, NormalizationProfile: profile,
		IdentityFormat: commitment.Format, ContentDigest: hex.EncodeToString(digest[:]), ItemCount: 1, MaxEncodedBytes: 16 << 20, ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading", CreatedAt: at, ActivityAt: at, ExpiresAt: at.Add(persistence.CollectionInactivityLifetime)}
	head.Secret, err = sealCollectionIdentity(context.Background(), c.sealer, head, c.storeID, key, fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}, at)
	if uploaded {
		item.Payload, err = c.sealer.Seal(context.Background(), item.Binding(c.storeID, head.UploadID), raw)
		if err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Millisecond)
		head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}, at)
	}
	return &uploadPreparationFixture{collectionSourceFixture: collectionSourceFixture{catalog: c, store: store, head: head, items: []persistence.CollectionItem{item}, at: at}, input: []collectionUploadInput{{Ref: stagedRef(item), ContentDigest: item.ContentDigest, Resource: raw}}}
}

func TestCollectionFileProfileBuiltins(t *testing.T) {
	inputs := []api.Resource{collectionMonitor("service", "https://example.test/health"), resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("PROFILE-SECRET")}), planLogEndpoint("log", "unused.log"), resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"log"}}), resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}})}
	for _, input := range inputs {
		for _, profile := range []string{"", collection.FileNormalizationProfile} {
			t.Run(input.Kind+"/"+profile, func(t *testing.T) {
				f := fileProfileFixture(t, input, profile, false)
				p := f.openUpload(t, nil)
				row, retry, err := p.prepare(context.Background(), f.input[0])
				if err != nil || retry {
					t.Fatal("built-in profile rejected", err)
				}
				f.at = f.at.Add(time.Millisecond)
				f.head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "upload", OperationID: f.head.ID, UploadID: f.head.UploadID, Item: &row}, f.at)
				s := f.open(t, nil)
				visits := 0
				if err = s.walk(context.Background(), func(stagedItemRef, *api.Resource) error { visits++; return nil }); err != nil || visits != 1 {
					t.Fatal("staged built-in rejected", err)
				}
			})
		}
	}
}

// Reconstruct structurally valid historical verdict/plan metadata through real
// durable commands. This deliberately bypasses today's compiler and does not
// establish that an old binary produced this metadata. The original encrypted
// input remains unchanged, so activation must independently enforce its profile.
func fileProfileAdmitHistorical(t *testing.T, f *uploadPreparationFixture, unchanged bool) persistence.CollectionState {
	t.Helper()
	ctx := context.Background()
	var old api.Resource
	if unchanged {
		old = createResource(t, f.catalog, collectionMonitor(f.input[0].Ref.Key.ID, "https://example.test/health"))
	}
	head := coordinatorRequest(t, &f.collectionSourceFixture)
	at := head.ValidationRequest.RequestedAt.Add(time.Second)
	claim := persistence.CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}
	head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_claim", OperationID: head.ID, UploadID: head.UploadID, ValidationFence: &persistence.CollectionValidationRequestFence{RequestID: head.ValidationRequest.ID}, ValidationClaim: &claim}, at)
	fence := persistence.CollectionValidationRequestFenceFor(head)
	source, err := newCollectionValidationSourceForAttempt(ctx, f.catalog, head.ID, fence, collectionReadAll.CanRead, collectionClock(at))
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	key := f.items[0].Key
	index := f.store.Status().CommittedIndex
	result := CollectionValidation{Valid: true, Index: index, Order: []persistence.CatalogKey{key}, Items: []CollectionValidationItem{{SourceID: f.items[0].Source, ItemID: "item.00000000000000000001", Key: key, Change: "create"}}}
	plan := &collectionPlan{Header: collectionPlanHeader{OperationID: head.ID, UploadID: head.UploadID, Actor: head.Actor, IdentityFormat: head.IdentityFormat, ContentDigest: head.ContentDigest, InputProgressDigest: head.ProgressDigest, ItemCount: 1, ObservedIndex: index}, Rows: []collectionPlanRow{{Ordinal: 1, InputOrdinal: 1, Source: f.items[0].Source, Document: 1, Item: 1, Key: key, Change: "create", Target: collectionPlanGuard{Key: key, Absent: true}}}}
	if unchanged {
		result.Items[0].Change = "unchanged"
		result.Items[0].UID, result.Items[0].ResourceVersion = old.Metadata.UID, old.Metadata.ResourceVersion
		plan.Rows[0].Change = "unchanged"
		plan.Rows[0].Target = collectionPlanGuard{Key: key, OriginalUID: old.Metadata.UID, OriginalRevision: old.Metadata.ResourceVersion, OriginalGeneration: old.Metadata.Generation, ReverseVersion: api.Pointer(uint64(0))}
	}
	validation, err := prepareCollectionValidationArtifact(ctx, source, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.close()
	artifact, err := prepareCollectionPlanArtifact(ctx, plan, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	source.close()
	commit := func(c persistence.CollectionCommand) {
		at = at.Add(time.Millisecond)
		c.OperationID, c.UploadID = head.ID, head.UploadID
		if c.Action != "validation_publish" {
			c.ValidationFence = fence
		}
		head = submitStagedSource(t, f.store, c, at)
	}
	commit(persistence.CollectionCommand{Action: "plan_begin", PlanBegin: &persistence.CollectionPlanBegin{Header: artifact.header, Descriptor: artifact.descriptor}})
	var ordinal uint64
	if err = visitCollectionPlanArtifact(ctx, artifact, func(fragment persistence.CollectionPlanFragment) error {
		ordinal++
		commit(persistence.CollectionCommand{Action: "plan_append", PlanID: artifact.header.PlanID, PlanFragment: &persistence.CollectionPlanLedgerFragment{Ordinal: ordinal, Fragment: fragment}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := f.store.VerifyCollectionPlan(ctx, head.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	commit(persistence.CollectionCommand{Action: "plan_finalize", PlanFinalize: &proof})
	header := persistence.CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID, InputProgressDigest: head.ProgressDigest, ItemCount: 1, Authority: head.ValidationRequest.Authority, CapabilitiesDigest: head.ValidationRequest.CapabilitiesDigest, Valid: true, PlanID: artifact.header.PlanID, PlanDigest: artifact.descriptor.Digest}
	commit(persistence.CollectionCommand{Action: "validation_begin", ValidationBegin: &persistence.CollectionValidationBegin{Header: header, Descriptor: validation.descriptor}})
	if err = validation.emit(ctx, func(items []persistence.CollectionValidationItem) error {
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
	auth, err := f.store.ObserveOperatorAuthority(ctx, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: auth, InputProgressDigest: head.ProgressDigest, ItemCount: 1, ResultID: header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: header.CapabilitiesDigest, ValidationRequest: fence, At: at}
	return submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &auth}, at)
}

func fileProfileRetainCandidate(t *testing.T, f *uploadPreparationFixture, head persistence.CollectionState) persistence.CollectionState {
	t.Helper()
	ctx := context.Background()
	at := head.Activation.At.Add(time.Second)
	command := candidateExecutionCommand(t, f.store, head, "begin", at)
	result := candidateExecutionSubmit(t, f.store, command, at)
	if result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	head = result.Collection.Clone()
	p := candidateOpen(t, f.collectionSourceFixture, head, nil)
	row, digest, err := p.view.Row(ctx, 1, p.now())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.close(); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	record := persistence.CatalogRecord{Key: row.Key, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at}
	r := collectionMonitor(row.Key.ID, "https://example.test/health")
	r.Metadata.UID, r.Metadata.ResourceVersion, r.Metadata.Generation = record.UID, record.Revision, 1
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	record.Payload, err = f.catalog.sealer.Seal(ctx, record.Binding(f.catalog.storeID), raw)
	if err != nil {
		t.Fatal(err)
	}
	candidate := persistence.CollectionPreparedItem{Binding: command.Binding, ID: uuid.NewString(), Ordinal: 1, InputOrdinal: 1, RowDigest: digest, At: at, Record: record}
	command.Action, command.Ordinal, command.Prepared = "prepare", 1, &candidate
	result = candidateExecutionSubmit(t, f.store, command, at)
	if result.Err != nil || !result.Allowed || result.Collection.Execution.Prepared == nil {
		t.Fatal("historical prepared slot", result.Err)
	}
	return result.Collection.Clone()
}

func TestCollectionFileProfileRetainedBuiltinsAndUnavailableKey(t *testing.T) {
	for _, profile := range []string{"", collection.FileNormalizationProfile} {
		for _, missing := range []bool{false, true} {
			t.Run(profile+"/missing="+map[bool]string{false: "false", true: "true"}[missing], func(t *testing.T) {
				f := fileProfileFixture(t, collectionMonitor("service", "https://example.test/health"), profile, true)
				head := fileProfileAdmitHistorical(t, f, false)
				head = fileProfileRetainCandidate(t, f, head)
				before, _ := f.store.CatalogSnapshot()
				if missing {
					wrapper, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{99}, 32))
					f.catalog.sealer, _ = secureconfig.NewSealer(candidateFailKeys{KeyWrapper: wrapper, failOpen: true})
				}
				w := executionTestWorker(t, f.collectionSourceFixture, head)
				err := w.prepareStep(context.Background(), executionWork(t, w, head.ID))
				if missing && profile != "" {
					if !errors.Is(err, ErrUnavailable) || w.pending != nil {
						t.Fatal("profiled candidate lacked an honest key hold", err)
					}
				} else if err != nil || w.pending == nil || w.pending.command.PreparedID != head.Execution.Prepared.ID {
					t.Fatal("retained candidate changed", err)
				}
				if missing && profile != "" {
					// Existing key-loading policy makes storage unavailable on a
					// failed unwrap; no durable command or decision was submitted.
					if status := f.store.Status(); status.CommittedIndex != before.Index || status.Ready {
						t.Fatal("key failure committed an outcome or claimed readiness")
					}
					return
				}
				after, _ := f.store.CatalogSnapshot()
				latest, _, _ := f.store.CollectionGet(head.ID)
				if before.Index != after.Index || before.Len() != after.Len() || !reflect.DeepEqual(head, latest) {
					t.Fatal("profile check mutated outcome")
				}
			})
		}
	}
}
