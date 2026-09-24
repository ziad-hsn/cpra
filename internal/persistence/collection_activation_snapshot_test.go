package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Build actual committed input, a plan and a sealed result. The optional request
// follows the same claimed-artifact path as the background validator. It never
// writes the active catalog; resource payloads remain encrypted fixture data.
func activationSnapshotInput(t *testing.T, s *Store, requested bool) CollectionState {
	t.Helper()
	head := validationHistoryCrashInput(t, s, 2)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	if requested {
		head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
	}
	plan, parts := planApplyArtifact(t, s, head)
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_begin", PlanBegin: &plan})
	for _, part := range parts {
		head = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_append", PlanID: plan.Header.PlanID, PlanFragment: &part})
	}
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_finalize", PlanFinalize: &proof})
	// Reuse the original-order metadata builder, then construct a successful
	// descriptor against the already finalized plan instead of recompiling it.
	_, begin, items := validationApplyIntent(t, s, head, authority, false)
	begin.Header.Valid, begin.Header.Issue = true, ""
	begin.Header.PlanID, begin.Header.PlanDigest = plan.Header.PlanID, plan.Descriptor.Digest
	begin.Descriptor = CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}
	for n := range items {
		items[n].Change, items[n].Issue = "create", ""
		digest, size, err := CollectionValidationNextDigest(begin.Descriptor.Digest, items[n])
		if err != nil {
			t.Fatal(err)
		}
		begin.Descriptor.Count++
		begin.Descriptor.Bytes += size
		begin.Descriptor.Digest = digest
	}
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_begin", ValidationBegin: &begin})
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_append", ValidationID: begin.Header.ResultID, ValidationItems: items})
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_finalize", ValidationID: begin.Header.ResultID})
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	if head.Phase != "validated" || !head.Validation.HistorySealed {
		t.Fatal("fixture did not seal a successful original result")
	}
	return head
}

func activationSnapshotAdmit(t *testing.T, s *Store, head CollectionState) (CollectionState, CollectionCommand) {
	t.Helper()
	at := head.ActivityAt.Add(time.Second)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	a := CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID,
		PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest,
		ValidationRequest: CollectionValidationRequestFenceFor(head), At: at}
	c := CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &a, ActivationAuthority: &authority}
	return validationApplyAllowed(t, collectionCommand(t, s, c, at)), c
}

func activationSnapshotCancel(t *testing.T, s *Store, head CollectionState, at time.Time) (CollectionState, CollectionCommand) {
	t.Helper()
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	c := CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID,
		Cancel:          &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at},
		ActivationFence: CollectionActivationFenceFor(head), ActivationAuthority: &authority}
	return validationApplyAllowed(t, collectionCommand(t, s, c, at)), c
}

func activationSnapshotNoExecution(t *testing.T, s *Store) {
	t.Helper()
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.image.OperationReservations) != 0 {
		t.Fatal("activation admission or replay created catalog/controller/child-operation work")
	}
}

func TestCollectionActivationSnapshotFrozenAdmission(t *testing.T) {
	s := openCatalogMemory(t)
	head := activationSnapshotInput(t, s, true)
	head, command := activationSnapshotAdmit(t, s, head)
	if s.fsm.image.Version != CollectionActivationFormatVersion || head.Phase != "applying" {
		t.Fatal("activation did not establish format8 admission")
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	before := &collectionTestSink{}
	if err := frozen.Persist(before); err != nil {
		t.Fatal(err)
	}
	want := head.Clone()
	// Returned nested metadata and later committed cancellation must not mutate
	// the image and namespace views already captured by Snapshot.
	head.Activation.ValidationRequest.ClaimID = uuid.NewString()
	command.Activation.ValidationRequest.RequestID = uuid.NewString()
	canceled, _ := activationSnapshotCancel(t, s, want, want.Activation.At.Add(time.Second))
	if canceled.Phase != "canceled" {
		t.Fatal("fixture did not cancel")
	}
	after := &collectionTestSink{}
	if err := frozen.Persist(after); err != nil || !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("captured snapshot changed after returned metadata mutation or cancellation", err)
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(before.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != CollectionActivationFormatVersion || !reflect.DeepEqual(i.Collections[want.ID], want) ||
		!reflect.DeepEqual(i.Authentication, s.fsm.image.Authentication) {
		t.Fatal("snapshot changed original admitted authority/result/plan/request")
	}
	if rows, err := ledger.Page(want.ID, 0, 256); err != nil || len(rows) != int(want.ItemCount) {
		t.Fatal("input namespace was lost", err)
	}
	if count, _, err := ledger.PlanStats(want.ID); err != nil || count != want.Plan.UploadedFragments {
		t.Fatal("plan namespace was lost", err)
	}
	if rows, err := ledger.ValidationPage(want.ID, 0, 256); err != nil || uint64(len(rows)) != want.Validation.Descriptor.Count {
		t.Fatal("result namespace was lost", err)
	}
	activationSnapshotNoExecution(t, s)
}

func TestCollectionActivationSnapshotRejectsMalformedBindingsAndDowngrade(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	raw := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	inputOffset := bytes.Index(raw, collectionLedgerMagic)
	outerEnd := bytes.IndexByte(raw, '\n') + 1
	if inputOffset < outerEnd || string(raw[:outerEnd]) != "CPRA-COLLECTION-SNAPSHOT-8\n" {
		t.Fatal("format8 framing absent")
	}
	for _, version := range []int{3, 4, 5, 6, 7} {
		downgraded := i
		downgraded.Version = version
		if validateCollectionHeaders(downgraded) == nil {
			t.Fatal("activation metadata accepted in historical format", version)
		}
		var blob bytes.Buffer
		blob.WriteString("CPRA-COLLECTION-SNAPSHOT-")
		blob.WriteByte(byte('0' + version))
		blob.WriteByte('\n')
		if err := json.NewEncoder(&blob).Encode(downgraded); err != nil {
			t.Fatal(err)
		}
		blob.Write(raw[inputOffset:])
		_, opened, err := decodeSnapshot(bytes.NewReader(blob.Bytes()), "")
		if opened != nil {
			_ = opened.Close()
		}
		if err == nil {
			t.Fatal("activation was decoded under historical framing", version)
		}
	}
	for name, change := range map[string]func(*CollectionState){
		"missing activation":        func(h *CollectionState) { h.Activation = nil },
		"wrong input commitment":    func(h *CollectionState) { h.Activation.InputProgressDigest = strings.Repeat("a", 64) },
		"wrong input count":         func(h *CollectionState) { h.Activation.ItemCount++ },
		"wrong result identity":     func(h *CollectionState) { h.Activation.ResultID = uuid.NewString() },
		"wrong result descriptor":   func(h *CollectionState) { h.Activation.ResultDescriptor.Digest = strings.Repeat("a", 64) },
		"wrong plan identity":       func(h *CollectionState) { h.Activation.PlanID = uuid.NewString() },
		"wrong plan descriptor":     func(h *CollectionState) { h.Activation.PlanDescriptor.Digest = strings.Repeat("a", 64) },
		"wrong profile":             func(h *CollectionState) { h.Activation.CapabilitiesDigest = strings.Repeat("a", 64) },
		"wrong owner":               func(h *CollectionState) { h.Activation.Authority.Actor = "another-operator" },
		"wrong claim":               func(h *CollectionState) { h.Activation.ValidationRequest.ClaimID = uuid.NewString() },
		"missing claim fence":       func(h *CollectionState) { h.Activation.ValidationRequest = nil },
		"before sealed result":      func(h *CollectionState) { h.Activation.At = h.Validation.FinalizedAt.Add(-time.Nanosecond) },
		"after upload admission":    func(h *CollectionState) { h.Activation.At = h.ExpiresAt },
		"unpublished result":        func(h *CollectionState) { h.Validation.HistorySealed = false },
		"applying with terminal at": func(h *CollectionState) { h.TerminalAt = h.Activation.At },
		"activation in validated":   func(h *CollectionState) { h.Phase = "validated" },
	} {
		t.Run(name, func(t *testing.T) {
			broken := i
			broken.Collections = maps.Clone(i.Collections)
			state := head.Clone()
			change(&state)
			broken.Collections[head.ID] = state
			var blob bytes.Buffer
			blob.Write(raw[:outerEnd])
			if err := json.NewEncoder(&blob).Encode(broken); err != nil {
				t.Fatal(err)
			}
			blob.Write(raw[inputOffset:])
			_, opened, err := decodeSnapshot(bytes.NewReader(blob.Bytes()), "")
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatal("malformed activation snapshot accepted")
			}
		})
	}
	planOffset, resultOffset := bytes.Index(raw, collectionPlanLedgerMagic), bytes.Index(raw, collectionValidationLedgerMagic)
	corrupt := bytes.Clone(raw)
	corrupt[len(corrupt)-1] ^= 1
	for name, broken := range map[string][]byte{
		"missing input":    append(bytes.Clone(raw[:inputOffset]), raw[planOffset:]...),
		"missing plan":     append(bytes.Clone(raw[:planOffset]), raw[resultOffset:]...),
		"missing result":   raw[:resultOffset],
		"duplicate result": append(bytes.Clone(raw), raw[resultOffset:]...),
		"trailing byte":    append(bytes.Clone(raw), 0),
		"corrupt footer":   corrupt,
	} {
		t.Run(name, func(t *testing.T) {
			_, opened, err := decodeSnapshot(bytes.NewReader(broken), "")
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatal("format8 lost mandatory namespace framing")
			}
		})
	}
}

func TestCollectionActivationSnapshotRetainsCatalogMutationTokens(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	activationSnapshotNoExecution(t, s)
	// Separate explicit catalog commands establish unrelated preexisting facts;
	// collection admission itself above created none of these records.
	shared := createCatalogFormat(t, s, CollectionActivationFormatVersion, catalogRecord(t, s, "Credential", "shared", "shared-uid", "s1", "private"))
	a := createCatalogFormat(t, s, CollectionActivationFormatVersion, catalogRecord(t, s, "Endpoint", "a", "a-uid", "a1", "private", shared.Key))
	b := catalogRecord(t, s, "Endpoint", "b", "b-uid", "b1", "private", shared.Key)
	before := max(s.fsm.image.CatalogMutationSequence, s.fsm.image.Index)
	results := applyCatalogFormat(t, s, CollectionActivationFormatVersion,
		formatCatalogCommand(updateCatalogMutation(t, s, a, "a2", "private", shared.Key)),
		formatCatalogCommand(CatalogMutation{Record: b, Create: true, Conditions: catalogConditions(t, s, b.References)}))
	for n, result := range results {
		if result.Err != nil || !result.Allowed || result.CatalogMutationSequence != before+uint64(n)+1 {
			t.Fatal("format8 lost distinct same-entry mutation tokens", result.Err)
		}
	}
	if s.fsm.image.Version != CollectionActivationFormatVersion {
		t.Fatal("catalog command downgraded activation image")
	}
	want := requireCatalog(t, s, shared.Key)
	i, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.CatalogMutationSequence != results[1].CatalogMutationSequence || !reflect.DeepEqual(i.Catalog[shared.Key.indexKey()], want) ||
		!reflect.DeepEqual(i.Collections[head.ID], head) || i.Authentication.Version != AuthenticationLifecycleFormatVersion {
		t.Fatal("format8 snapshot lost catalog or authority lifecycle semantics")
	}
}

func TestCollectionActivationSnapshotPreservesFormats3Through7(t *testing.T) {
	for _, version := range []int{3, 4, 5, 6, 7} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 2)
			if version == 4 {
				createCatalogFormat(t, s, version, catalogRecord(t, s, "Credential", "older", "uid", "revision", "private"))
			}
			if version >= 6 {
				// An old input can coexist with authentication lifecycle metadata.
				if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
					t.Fatal(err)
				}
			}
			s.fsm.image.Version = version
			raw := captureSnapshotBytes(t, s.fsm)
			i, view, err := decodeSnapshot(bytes.NewReader(raw), "")
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if i.Version != version || !reflect.DeepEqual(i.Collections[head.ID], head) || !reflect.DeepEqual(i.Catalog, s.fsm.image.Catalog) ||
				i.CatalogMutationSequence != s.fsm.image.CatalogMutationSequence || !reflect.DeepEqual(i.Authentication, s.fsm.image.Authentication) {
				t.Fatal("historical metadata changed during format8-capable decode")
			}
			if rows, err := view.Page(head.ID, 0, 256); err != nil || len(rows) != 2 {
				t.Fatal("historical input lost", err)
			}
			if bytes.Contains(raw, collectionPlanLedgerMagic) != (version >= 5) || bytes.Contains(raw, collectionValidationLedgerMagic) != (version >= 6) {
				t.Fatal("historical namespace set changed")
			}
		})
	}
}

func TestCollectionActivationSnapshotExplicitRestoreInvalidatesAdmission(t *testing.T) {
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	head, command := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The marker chronology follows the committed future-time fixture.
	if err := MarkRestored(config.Storage.Directory, head.Activation.At.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	s.fsm.mu.RLock()
	got := s.fsm.image.Collections[head.ID].Clone()
	epoch := s.fsm.image.OperationEpoch
	s.fsm.mu.RUnlock()
	oldEpoch, _, _ := ParseOperationHandle(head.ID)
	if epoch == oldEpoch || got.Phase != "invalidated" || got.InvalidatedByRestore == "" || !reflect.DeepEqual(got.Activation, head.Activation) ||
		!reflect.DeepEqual(got.Plan, head.Plan) || !reflect.DeepEqual(got.Validation, head.Validation) {
		t.Fatal("restore revived or rewrote original activation")
	}
	if _, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: got.TerminalAt, Collection: &command}}); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("administrative restore admitted execution", err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("resnapshot restored activation", err)
	}
	activationSnapshotNoExecution(t, s)
	state, err := s.Authentication()
	if err != nil || !state.ResetRequired {
		t.Fatal("explicit restore did not require new authority", err)
	}
	provision := authenticationBootstrap()
	provision.Mode, provision.Epoch, provision.ExpectedEpoch, provision.ExpectedRevision = "provision", state.Epoch, state.Epoch, state.Revision
	provision.At = got.TerminalAt.Add(time.Second)
	provision.Principals[0].ExpiresAt = provision.At.Add(time.Hour)
	provision.Principals[0].TokenSHA256 = authenticationVerifier("activation-fresh-after-restore")
	if _, err := s.CommitAuthentication(context.Background(), provision); err != nil {
		t.Fatal("provision normal service after restore", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("offline validation of restored activation", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal("normal service after legitimate reprovision", err)
	}
	at := provision.At.Add(time.Second)
	current, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil || current.Actor != head.Actor || current.Epoch == head.Activation.Authority.Epoch {
		t.Fatal("fixture did not establish fresh authority with same actor text", err)
	}
	beforeNamespaces := activationCrashNamespaceDigests(t, s)
	beforeHistory, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	command.ActivationAuthority = &current
	result := collectionCommand(t, s, command, at)
	if !errors.Is(result.Err, ErrOperationExpired) || result.Allowed || len(result.Events) != 0 {
		t.Fatal("new service authority revived an activation from the old operation epoch", result.Err)
	}
	s.fsm.mu.RLock()
	after := s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.RUnlock()
	afterHistory, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil || !reflect.DeepEqual(afterHistory, beforeHistory) || !reflect.DeepEqual(after, got) ||
		activationCrashNamespaceDigests(t, s) != beforeNamespaces {
		t.Fatal("rejected old-epoch admission changed metadata, original namespaces or terminal outcome", err)
	}
	activationSnapshotNoExecution(t, s)
	if err := s.Snapshot(); err != nil {
		t.Fatal("snapshot normal service after rejected old activation", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("offline validation after normal-service epoch rejection", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
