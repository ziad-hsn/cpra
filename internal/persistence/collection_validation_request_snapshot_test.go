package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionValidationRequestSnapshotPreservesLegacyNamespaceFormats(t *testing.T) {
	for _, tc := range []struct {
		version      int
		magic        string
		plan, result bool
	}{
		{CollectionFormatVersion, collectionSnapshotMagic, false, false},
		{CatalogMutationFormatVersion, catalogMutationSnapshotMagic, false, false},
		{CollectionPlanFormatVersion, collectionPlanSnapshotMagic, true, false},
		{CollectionValidationFormatVersion, collectionValidationSnapshotMagic, true, true},
	} {
		t.Run(tc.magic, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 2)
			if tc.version == CatalogMutationFormatVersion {
				// Format4 was first introduced by a catalog mutation and requires
				// its nonzero sequence. Later plan/result formats may precede one.
				createCatalogFormat(t, s, tc.version, catalogRecord(t, s, "Credential", "old", "uid", "rev", "private"))
			}
			// No request command has been committed: older images retain their original
			// interpretation, including the absence of namespaces not yet introduced.
			s.fsm.image.Version = tc.version
			raw := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(raw, []byte(tc.magic)) || bytes.Contains(raw, collectionPlanLedgerMagic) != tc.plan || bytes.Contains(raw, collectionValidationLedgerMagic) != tc.result {
				t.Fatal("legacy namespace framing changed")
			}
			image, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			if image.Version != tc.version || !reflect.DeepEqual(image.Collections[head.ID], head) || image.CatalogMutationSequence != s.fsm.image.CatalogMutationSequence {
				t.Fatal("legacy image semantics changed")
			}
			if page, err := ledger.Page(head.ID, 0, 256); err != nil || len(page) != 2 {
				t.Fatal("original input lost", err)
			}
		})
	}
}

func TestCollectionValidationRequestSnapshotPreservesFormat6ResultsAndAuthority(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := "rejected"
		if valid {
			name = "validated"
		}
		t.Run(name, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 2)
			head, begin, items := validationApplyIntent(t, s, head, authority, valid)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			if s.fsm.image.Version != CollectionValidationFormatVersion {
				t.Fatal("existing commands unnecessarily promoted image")
			}
			authentication, err := s.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			// An image can later be format7 while it still holds an older format6
			// operation. The existing result, owner and current policy must survive.
			s.fsm.image.Version = CollectionValidationRequestFormatVersion
			raw := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(raw, []byte(collectionValidationRequestSnapshotMagic)) {
				t.Fatal("format7 magic absent")
			}
			i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			if i.Version != CollectionValidationRequestFormatVersion || !reflect.DeepEqual(i.Collections[head.ID], head) || !reflect.DeepEqual(i.Authentication, &authentication) || len(i.Catalog) != 0 || len(i.Monitors) != 0 {
				t.Fatal("format6 operation or authority changed")
			}
			page, err := ledger.ValidationPage(head.ID, 0, 256)
			if err != nil || !reflect.DeepEqual(page, items) {
				t.Fatal("validation result namespace changed", err)
			}
			if valid {
				count, _, err := ledger.PlanStats(head.ID)
				if err != nil || count != head.Plan.UploadedFragments {
					t.Fatal("plan namespace changed", err)
				}
			}
			if _, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt); err != nil {
				t.Fatal("policy lifecycle lost", err)
			}
			verifyRequestSnapshotStreams(t, raw)
		})
	}
}

func verifyRequestSnapshotStreams(t *testing.T, raw []byte) {
	t.Helper()
	inputOffset, planOffset, resultOffset := bytes.Index(raw, collectionLedgerMagic), bytes.Index(raw, collectionPlanLedgerMagic), bytes.Index(raw, collectionValidationLedgerMagic)
	if inputOffset < 0 || planOffset <= inputOffset || resultOffset <= planOffset {
		t.Fatal("format7 namespace order changed")
	}
	corrupt := bytes.Clone(raw)
	corrupt[len(corrupt)-1] ^= 1
	for name, broken := range map[string][]byte{
		"missing input":            append(bytes.Clone(raw[:inputOffset]), raw[planOffset:]...),
		"missing plan":             append(bytes.Clone(raw[:planOffset]), raw[resultOffset:]...),
		"missing result":           bytes.Clone(raw[:resultOffset]),
		"truncated result":         bytes.Clone(raw[:len(raw)-1]),
		"corrupt result":           corrupt,
		"duplicate result":         append(bytes.Clone(raw), raw[resultOffset:]...),
		"trailing byte":            append(bytes.Clone(raw), 0),
		"mismatched outer version": append([]byte(collectionValidationSnapshotMagic), raw[len(collectionValidationRequestSnapshotMagic):]...),
	} {
		t.Run(name, func(t *testing.T) {
			_, ledger, err := decodeSnapshot(bytes.NewReader(broken), "")
			if ledger != nil {
				_ = ledger.Close()
			}
			if err == nil {
				t.Fatal("malformed format7 snapshot accepted")
			}
		})
	}
	decoder := json.NewDecoder(bytes.NewReader(raw[len(collectionValidationRequestSnapshotMagic):]))
	var plain json.RawMessage
	if err := decoder.Decode(&plain); err != nil {
		t.Fatal(err)
	}
	_, ledger, err := decodeSnapshot(bytes.NewReader(plain), "")
	if ledger != nil {
		_ = ledger.Close()
	}
	if err == nil {
		t.Fatal("format7 raw JSON lost mandatory streams")
	}
}

func TestCollectionValidationRequestSnapshotRetainsUniqueCatalogTokens(t *testing.T) {
	s := openCatalogMemory(t)
	head := validationHistoryCrashInput(t, s, 1)
	requestSnapshotBegin(t, s, head)
	shared := createCatalogFormat(t, s, CollectionValidationRequestFormatVersion, catalogRecord(t, s, "Credential", "shared", "shared-uid", "s1", "private"))
	a := createCatalogFormat(t, s, CollectionValidationRequestFormatVersion, catalogRecord(t, s, "Endpoint", "a", "a-uid", "a1", "private", shared.Key))
	b := catalogRecord(t, s, "Endpoint", "b", "b-uid", "b1", "private", shared.Key)
	before := s.fsm.image.CatalogMutationSequence
	if before < s.fsm.image.Index {
		before = s.fsm.image.Index
	}
	results := applyCatalogFormat(t, s, CollectionValidationRequestFormatVersion,
		formatCatalogCommand(updateCatalogMutation(t, s, a, "a2", "private", shared.Key)),
		formatCatalogCommand(CatalogMutation{Record: b, Create: true, Conditions: catalogConditions(t, s, b.References)}))
	for n, r := range results {
		if r.Err != nil || !r.Allowed || r.CatalogMutationSequence != before+uint64(n)+1 {
			t.Fatal("format7 lost unique mutation sequence", r.Err)
		}
	}
	if s.fsm.image.Version != CollectionValidationRequestFormatVersion || results[0].CatalogMutationSequence == results[1].CatalogMutationSequence {
		t.Fatal("same-entry tokens collided")
	}
	original := requireCatalog(t, s, shared.Key)
	raw := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.CatalogMutationSequence != results[1].CatalogMutationSequence || !reflect.DeepEqual(i.Catalog[shared.Key.indexKey()], original) || i.Authentication.Version != AuthenticationLifecycleFormatVersion {
		t.Fatal("catalog or authentication lifecycle state lost")
	}
}

func requestSnapshotBegin(t *testing.T, s *Store, head CollectionState) (CollectionState, CollectionCommand) {
	t.Helper()
	at := head.ActivityAt.Add(time.Millisecond)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	request := CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), RequestedAt: at}
	command := CollectionCommand{Action: "validation_request", OperationID: head.ID, UploadID: head.UploadID, ValidationRequest: &request}
	return validationApplyAllowed(t, collectionCommand(t, s, command, at)), command
}

func requestSnapshotClaim(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	claim := CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: head.ActivityAt.Add(time.Millisecond)}
	command := CollectionCommand{Action: "validation_claim", OperationID: head.ID, UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationClaim: &claim}
	return validationApplyAllowed(t, collectionCommand(t, s, command, claim.At))
}

func requestSnapshotInterrupt(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	interruption := CollectionValidationInterruption{ID: uuid.NewString(), Reason: "coordinatorRestarted", At: head.ActivityAt.Add(time.Millisecond)}
	progress := collectionCleanupFor(head)
	command := CollectionCommand{Action: "validation_interrupt", OperationID: head.ID, UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationProgress: &progress, ValidationInterruption: &interruption}
	return validationApplyAllowed(t, collectionCommand(t, s, command, interruption.At))
}

func TestCollectionValidationRequestSnapshotFreezesOriginalRequestAndClaim(t *testing.T) {
	s := openCatalogMemory(t)
	head := validationHistoryCrashInput(t, s, 2)
	if s.fsm.image.Version != CollectionValidationFormatVersion {
		t.Fatal("old input was not format6")
	}
	old, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	oldBytes := &collectionTestSink{}
	if err := old.Persist(oldBytes); err != nil {
		t.Fatal(err)
	}
	head, command := requestSnapshotBegin(t, s, head)
	if s.fsm.image.Version != CollectionValidationRequestFormatVersion {
		t.Fatal("request did not promote format")
	}
	again := &collectionTestSink{}
	if err := old.Persist(again); err != nil || !bytes.Equal(oldBytes.Bytes(), again.Bytes()) {
		t.Fatal("format7 promotion mutated frozen6 snapshot", err)
	}
	pending := head.Clone()
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	head = requestSnapshotClaim(t, s, head)
	head = requestSnapshotInterrupt(t, s, head)
	sink := &collectionTestSink{}
	if err := frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	original, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if !reflect.DeepEqual(original.Collections[head.ID], pending) {
		t.Fatal("frozen snapshot shared later claim/interruption")
	}
	current := captureSnapshotBytes(t, s.fsm)
	restored, materialized, err := decodeSnapshot(bytes.NewReader(current), "")
	if err != nil {
		t.Fatal(err)
	}
	defer materialized.Close()
	if !reflect.DeepEqual(restored.Collections[head.ID], head) || head.Phase != "interrupted" || len(restored.Catalog) != 0 || len(restored.Monitors) != 0 || len(restored.Operations) != 0 {
		t.Fatal("request metadata lost or activated work")
	}
	if bytes.Contains(current, []byte(authenticationToken)) || bytes.Contains(current, []byte("never-write-this-collection-plaintext")) {
		t.Fatal("snapshot leaked credentials")
	}
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion, CollectionValidationFormatVersion} {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{{Kind: "collection", At: command.ValidationRequest.RequestedAt, Collection: &command}}})
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatal("request accepted in old log format", version)
		}
	}
}

func TestCollectionValidationRequestSnapshotRejectsDowngradeAndMalformedMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	head := validationHistoryCrashInput(t, s, 2)
	head, _ = requestSnapshotBegin(t, s, head)
	head = requestSnapshotClaim(t, s, head)
	raw := captureSnapshotBytes(t, s.fsm)
	image, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	inputOffset := bytes.Index(raw, collectionLedgerMagic)
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion, CollectionValidationFormatVersion} {
		downgraded := image
		downgraded.Version = version
		if validateCollectionHeaders(downgraded) == nil {
			t.Fatal("request metadata accepted by old image", version)
		}
	}
	cases := []struct {
		name string
		edit func(*CollectionState)
	}{
		{"missing request", func(h *CollectionState) { h.ValidationRequest = nil }},
		{"interrupted without request or timestamp", func(h *CollectionState) {
			h.Phase, h.ValidationRequest, h.TerminalAt = "interrupted", nil, time.Time{}
		}},
		{"wrong input digest", func(h *CollectionState) { h.ValidationRequest.InputProgressDigest = strings.Repeat("a", 64) }},
		{"wrong count", func(h *CollectionState) { h.ValidationRequest.ItemCount++ }},
		{"missing owner", func(h *CollectionState) { h.Owner = nil }},
		{"wrong actor", func(h *CollectionState) { h.ValidationRequest.Authority.Actor = "other" }},
		{"invalid capabilities", func(h *CollectionState) { h.ValidationRequest.CapabilitiesDigest = "invalid" }},
		{"oversized identifier", func(h *CollectionState) { h.ValidationRequest.ID = strings.Repeat("x", 4096) }},
		{"late claim", func(h *CollectionState) { h.ValidationRequest.Claim.At = h.ActivityAt.Add(time.Second) }},
		{"unknown interruption", func(h *CollectionState) {
			h.Phase = "interrupted"
			h.TerminalAt = h.ActivityAt
			h.ValidationRequest.Interruption = &CollectionValidationInterruption{ID: uuid.NewString(), Reason: "private-provider-diagnostic", At: h.ActivityAt}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := image
			broken.Collections = maps.Clone(image.Collections)
			state := head.Clone()
			tc.edit(&state)
			broken.Collections[head.ID] = state
			var blob bytes.Buffer
			blob.WriteString(collectionValidationRequestSnapshotMagic)
			if err := json.NewEncoder(&blob).Encode(broken); err != nil {
				t.Fatal(err)
			}
			blob.Write(raw[inputOffset:])
			_, opened, err := decodeSnapshot(bytes.NewReader(blob.Bytes()), "")
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatal("malformed request accepted")
			}
		})
	}
	verifyRequestSnapshotStreams(t, raw)
}

func TestCollectionValidationRequestSnapshotRealRaftRestart(t *testing.T) {
	for _, phase := range []string{"pending", "claimed", "interrupted"} {
		for _, snapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/snapshot-%v", phase, snapshot), func(t *testing.T) {
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
				defer s.Close()
				head := validationHistoryCrashInput(t, s, 2)
				head, _ = requestSnapshotBegin(t, s, head)
				if phase == "interrupted" {
					head = requestSnapshotClaim(t, s, head)
				}
				if snapshot {
					if err := s.Snapshot(); err != nil {
						t.Fatal(err)
					}
				}
				switch phase {
				case "claimed":
					head = requestSnapshotClaim(t, s, head)
				case "interrupted":
					head = requestSnapshotInterrupt(t, s, head)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				lock, err := LockOffline(config.Storage.Directory)
				if err != nil {
					t.Fatal("stopped format7 validation", err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				got, found, err := reopened.CollectionGet(head.ID)
				if err != nil || !found || !reflect.DeepEqual(got, head) || reopened.fsm.image.Version != CollectionValidationRequestFormatVersion {
					t.Fatal("restart lost original request/claim/interruption", err)
				}
				if len(reopened.fsm.image.Catalog) != 0 || len(reopened.fsm.image.Monitors) != 0 || len(reopened.fsm.image.Operations) != 0 {
					t.Fatal("request replay activated work")
				}
				// The public upload-page API correctly closes when validation is
				// requested. Inspect persisted ciphertext through the owned ledger.
				page, err := reopened.fsm.collections.Page(head.ID, 0, 256)
				if err != nil || len(page) != 2 {
					t.Fatal("original input lost", err)
				}
			})
		}
	}
}
