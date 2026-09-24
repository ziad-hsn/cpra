package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionValidationFaultQuotaKeepsOriginalPrefixUsable(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 3)
			head, begin, items := validationApplyIntent(t, s, head, authority, valid)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[:1]))
			ledger := s.fsm.collections
			used, err := ledger.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			secondCost, err := collectionValidationLedgerCost(head.ID, items[1])
			if err != nil {
				t.Fatal(err)
			}
			beforeRows, err := ledger.ValidationPage(head.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			beforeInput, beforePlan, beforeResult := validationLedgerStreams(t, ledger)
			// Lower only the test materialization's shared quota. The next result fits,
			// but the final row does not; the whole two-row batch must roll back.
			s.fsm.mu.Lock()
			ledger.mu.Lock()
			originalQuota := ledger.maxBytes
			ledger.maxBytes = used + secondCost
			ledger.mu.Unlock()
			s.fsm.mu.Unlock()
			result := validationApplyCommand(t, s, head, "validation_append", nil, items[1:])
			if !errors.Is(result.Err, ErrCollectionQuota) {
				t.Fatal("quota was not an explicit admission rejection", result.Err)
			}
			planApplyStateUnchanged(t, s, head)
			if got, err := ledger.Bytes(); err != nil || got != used {
				t.Fatal("rejected batch changed shared quota", got, err)
			}
			if rows, err := ledger.ValidationPage(head.ID, 0, 500); err != nil || !reflect.DeepEqual(rows, beforeRows) {
				t.Fatal("rejected batch changed prior rows", err)
			}
			input, plan, validation := validationLedgerStreams(t, ledger)
			if !bytes.Equal(input, beforeInput) || !bytes.Equal(plan, beforePlan) || !bytes.Equal(validation, beforeResult) {
				t.Fatal("quota rejection changed a namespace")
			}
			s.fsm.mu.Lock()
			ledger.mu.Lock()
			ledger.maxBytes = originalQuota
			ledger.mu.Unlock()
			s.fsm.mu.Unlock()
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[1:]))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			if head.Validation.FinalizedAt.IsZero() || head.Validation.Descriptor != begin.Descriptor || s.fsm.err != nil {
				t.Fatal("storage failed to accept original suffix after quota was available")
			}
		})
	}
}

// Deliberately bypass the outer snapshot writer's image validation, while using
// real canonical namespace writers. The forged stream has valid namespace
// checksums: Restore must reject cross-header/source/plan inconsistencies.
func validationFaultSnapshot(t *testing.T, i image, l *collectionLedger) []byte {
	t.Helper()
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var b bytes.Buffer
	b.WriteString(collectionValidationSnapshotMagic)
	if err := json.NewEncoder(&b).Encode(i); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionPlanLedger(&b, v); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionValidationLedger(&b, v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func validationFaultRehash(t *testing.T, head *CollectionState, l *collectionLedger) {
	t.Helper()
	items, err := l.ValidationPage(head.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	d := CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}
	for _, item := range items {
		digest, cost, err := CollectionValidationNextDigest(d.Digest, item)
		if err != nil {
			t.Fatal(err)
		}
		d.Count++
		d.Bytes += cost
		d.Digest = digest
	}
	head.Validation.Descriptor = d
	head.Validation.ProgressDigest = d.Digest
	head.Validation.ResultBytes = d.Bytes
	_, encoded, err := l.ValidationStats(head.ID)
	if err != nil {
		t.Fatal(err)
	}
	head.Validation.EncodedBytes = encoded
}
func TestCollectionValidationFaultRestoreRejectsConsistentlyFramedTampering(t *testing.T) {
	for _, mode := range []string{"header-input", "header-plan", "descriptor", "source", "classification", "missing-row", "claimed-removal"} {
		t.Run(mode, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 3)
			head, begin, items := validationApplyIntent(t, s, head, authority, true)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			cancellation := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: head.ActivityAt.Add(time.Millisecond)}
			head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &cancellation}, cancellation.At))
			pristine := captureSnapshotBytes(t, s.fsm)
			i, ledger, err := decodeSnapshot(bytes.NewReader(pristine), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			changed := i.Collections[head.ID].Clone()
			switch mode {
			case "header-input":
				changed.Validation.Header.InputProgressDigest = strings.Repeat("a", 64)
			case "header-plan":
				changed.Validation.Header.PlanDigest = strings.Repeat("b", 64)
			case "descriptor":
				changed.Validation.Descriptor.Digest = strings.Repeat("c", 64)
			case "source", "classification":
				item := items[1]
				if mode == "source" {
					item.Source = "source.00000000000000000002"
				} else {
					item.Change = "unchanged"
				}
				raw, err := collectionValidationLedgerEncoding(head.ID, item)
				if err != nil {
					t.Fatal(err)
				}
				ledger.mu.Lock()
				delta := int64(len(raw) - len(ledger.validationRows[head.ID][2]))
				ledger.validationRows[head.ID][2] = raw
				ledger.validationOperationBytes[head.ID] += delta
				ledger.validationBytes += delta
				ledger.bytes += delta
				ledger.mu.Unlock()
				validationFaultRehash(t, &changed, ledger)
			case "missing-row":
				// Remove a real tail and correct its materialized accounting, leaving the
				// authoritative header untouched. Framing itself remains well formed.
				ledger.mu.Lock()
				cost := int64(len(ledger.validationRows[head.ID][3]))
				delete(ledger.validationRows[head.ID], 3)
				ledger.validationOperationBytes[head.ID] -= cost
				ledger.validationBytes -= cost
				ledger.bytes -= cost
				ledger.mu.Unlock()
			case "claimed-removal":
				cost, err := collectionValidationLedgerCost(head.ID, items[2])
				if err != nil {
					t.Fatal(err)
				}
				changed.Validation.RemovedRows = 1
				changed.Validation.RemovedBytes = cost
			}
			i.Collections[head.ID] = changed
			forged := validationFaultSnapshot(t, i, ledger)
			originalLedger := s.fsm.collections
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(forged))); err == nil {
				t.Fatal("restore accepted forged validation evidence")
			}
			if s.fsm.collections != originalLedger {
				t.Fatal("failed restore installed partial generation")
			}
			planApplyStateUnchanged(t, s, head)
			if rows, err := s.fsm.collections.ValidationPage(head.ID, 0, 500); err != nil || !reflect.DeepEqual(rows, items) {
				t.Fatal("failed restore changed committed result", err)
			}
			results, err := s.Submit(context.Background(), []Command{{Kind: "barrier", At: head.ActivityAt.Add(time.Second)}})
			if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
				t.Fatal("failed restore poisoned original store", err)
			}
		})
	}
}
func TestCollectionValidationFaultHistoricalFormatsRejectNewState(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 1)
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	cleanup := collectionCleanupFor(head)
	commands := []CollectionCommand{
		{Action: "validation_begin", OperationID: head.ID, UploadID: head.UploadID, ValidationBegin: &begin},
		{Action: "validation_append", OperationID: head.ID, UploadID: head.UploadID, ValidationID: begin.Header.ResultID, ValidationItems: items},
		{Action: "validation_finalize", OperationID: head.ID, UploadID: head.UploadID, ValidationID: begin.Header.ResultID},
		{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &cleanup},
	}
	for _, c := range commands {
		for version := FormatVersion; version <= CollectionValidationFormatVersion; version++ {
			raw, err := json.Marshal(envelope{Version: version, Commands: []Command{{Kind: "collection", At: head.ActivityAt, Collection: &c}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodeEnvelope(raw)
			if (err == nil) != (version == CollectionValidationFormatVersion) {
				t.Fatal("validation command format boundary changed", c.Action, version, err)
			}
		}
	}
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion} {
		i := s.fsm.image
		i.Version = version
		i.Authentication = nil
		if validateCollectionHeaders(i) == nil {
			t.Fatal("historical header admitted current validation state", version)
		}
	}
	planApplyStateUnchanged(t, s, head)
}
func TestCollectionValidationFaultCurrentAuthorityCannotRebindIntent(t *testing.T) {
	for _, mode := range []string{"result-id", "other-operator", "new-policy"} {
		t.Run(mode, func(t *testing.T) {
			s := openCatalogMemory(t)
			policy := authenticationBootstrap()
			policy.Principals = append(policy.Principals, AuthenticationPrincipal{ID: "secondary", Role: "operator", TokenSHA256: authenticationVerifier("secondary")})
			if _, err := s.CommitAuthentication(context.Background(), policy); err != nil {
				t.Fatal(err)
			}
			head, authority := validationApplyInput(t, s, 2)
			head, begin, items := validationApplyIntent(t, s, head, authority, false)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[:1]))
			changed := begin
			want := ErrCollectionConflict
			at := head.ActivityAt.Add(time.Millisecond)
			switch mode {
			case "result-id":
				changed.Header.ResultID = uuid.NewString()
			case "other-operator":
				current, err := s.ObserveOperatorAuthority(context.Background(), "secondary", at)
				if err != nil {
					t.Fatal("fixture actor is not a current named operator", err)
				}
				changed.Header.Authority = current
				want = ErrOperatorAuthorityDenied
			case "new-policy":
				state, err := s.Authentication()
				if err != nil {
					t.Fatal(err)
				}
				replacement := lifecycleReplacement(state)
				replacement.At = at
				// Exercise the committed replacement path; the public Store API
				// correctly requires stopped administration for this operation.
				if result := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); result.Err != nil {
					t.Fatal(result.Err)
				}
				current, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
				if err != nil || current == authority {
					t.Fatal("fixture did not obtain current replacement authority", err)
				}
				changed.Header.Authority = current
			}
			c := CollectionCommand{Action: "validation_begin", OperationID: head.ID, UploadID: head.UploadID, ValidationBegin: &changed}
			if result := collectionCommand(t, s, c, at); !errors.Is(result.Err, want) {
				t.Fatal("immutable validation intent rebound", result.Err)
			}
			planApplyStateUnchanged(t, s, head)
			if rows, err := s.fsm.collections.ValidationPage(head.ID, 0, 500); err != nil || !reflect.DeepEqual(rows, items[:1]) {
				t.Fatal("identity conflict changed prior result", err)
			}
			// An arbitrary fresh result ID cannot address the existing continuation.
			appendCommand := CollectionCommand{Action: "validation_append", OperationID: head.ID, UploadID: head.UploadID, ValidationID: uuid.NewString(), ValidationItems: items[1:]}
			if result := collectionCommand(t, s, appendCommand, at); !errors.Is(result.Err, ErrCollectionConflict) {
				t.Fatal("new result ID resumed original inventory", result.Err)
			}
			planApplyStateUnchanged(t, s, head)
		})
	}
}
func TestCollectionValidationFaultSummaryOnlyDescriptor(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 1)
	_, begin, _ := validationApplyIntent(t, s, head, authority, false)
	begin.Header.ItemCount = CollectionValidationMaxItems + 1
	begin.Header.SummaryOnly = true
	begin.Header.Issue = "validationLimit"
	begin.Descriptor = CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}
	if err := begin.validate(); err != nil {
		t.Fatal("valid empty limit summary rejected", err)
	}
	for name, mutate := range map[string]func(*CollectionValidationBegin){
		"success":              func(b *CollectionValidationBegin) { b.Header.Valid = true },
		"wrong issue":          func(b *CollectionValidationBegin) { b.Header.Issue = "invalidResource" },
		"within item bound":    func(b *CollectionValidationBegin) { b.Header.ItemCount = CollectionValidationMaxItems },
		"nonempty count":       func(b *CollectionValidationBegin) { b.Descriptor.Count = 1 },
		"nonempty bytes":       func(b *CollectionValidationBegin) { b.Descriptor.Bytes = 4 },
		"wrong initial digest": func(b *CollectionValidationBegin) { b.Descriptor.Digest = strings.Repeat("f", 64) },
		"plan attached": func(b *CollectionValidationBegin) {
			b.Header.PlanID = uuid.NewString()
			b.Header.PlanDigest = strings.Repeat("a", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := begin
			mutate(&bad)
			if err := bad.validate(); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("malformed limit summary accepted", err)
			}
		})
	}
	// This is descriptor validation only; it does not claim a large upload ran.
}

func TestCollectionValidationFaultOwnedCreateRequiresCurrentFormat(t *testing.T) {
	s, _, authority := authorityFixture(t)
	command := collectionCreateFixture(t, s, 1)
	command.Create.Actor = authority.Actor
	command.Create.Owner = &authority
	at := command.Create.ActivityAt
	for version := FormatVersion; version <= CollectionValidationFormatVersion; version++ {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{{Kind: "collection", At: at, Collection: &command}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(raw)
		if (err == nil) != (version == CollectionValidationFormatVersion) {
			t.Fatal("owned creation downgraded", version, err)
		}
	}
	head := validationApplyAllowed(t, collectionCommand(t, s, command, at))
	if head.Owner == nil || *head.Owner != authority {
		t.Fatal("creation lost original permanent owner")
	}
	got := head.Clone()
	got.Owner.Actor = "secondary"
	unchanged, found, err := s.CollectionGet(head.ID)
	if err != nil || !found || unchanged.Owner == nil || *unchanged.Owner != authority {
		t.Fatal("caller mutated owner through clone", err)
	}
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion} {
		i := s.fsm.image
		i.Version = version
		i.Authentication = nil
		if validateCollectionHeaders(i) == nil {
			t.Fatal("historical format admitted an owner fence", version)
		}
	}
}

func TestCollectionValidationFaultHistoricalActorReuseCannotAdoptUpload(t *testing.T) {
	s := openCatalogMemory(t)
	old := authenticationBootstrap()
	// Historical logs allowed deletion and textual actor reuse. Replay those
	// literal old semantics; this is not a current administrative capability.
	legacy := lifecycleCommand(t, s, CollectionPlanFormatVersion, old)
	if legacy.Err != nil {
		t.Fatal(legacy.Err)
	}
	command := collectionCreateFixture(t, s, 1)
	command.Create.Actor = "oncall"
	observed, err := s.ObserveCollectionOwner(context.Background(), "oncall", command.Create.ActivityAt)
	if err != nil || observed != nil {
		t.Fatal("historical policy supplied permanent identity", err)
	}
	head := validationApplyAllowed(t, collectionCommand(t, s, command, command.Create.ActivityAt))
	input := collectionItemFixture(t, s, head, 1, "old-owned-text")
	head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, input, head.ActivityAt.Add(time.Millisecond)))
	remove := lifecycleReplacement(*legacy.Authentication)
	remove.Version = 0
	remove.Principals = nil
	remove.At = head.ActivityAt.Add(time.Millisecond)
	removed := lifecycleCommand(t, s, CollectionPlanFormatVersion, remove)
	if removed.Err != nil {
		t.Fatal(removed.Err)
	}
	reuse := lifecycleReplacement(*removed.Authentication)
	reuse.Version = 0
	reuse.Principals = old.Principals
	reused := lifecycleCommand(t, s, CollectionPlanFormatVersion, reuse)
	if reused.Err != nil {
		t.Fatal(reused.Err)
	}
	upgrade := lifecycleReplacement(*reused.Authentication)
	current := lifecycleCommand(t, s, CollectionValidationFormatVersion, upgrade)
	if current.Err != nil {
		t.Fatal(current.Err)
	}
	authority, err := s.ObserveOperatorAuthority(context.Background(), "oncall", upgrade.At)
	if err != nil {
		t.Fatal("reused text is not a current named operator", err)
	}
	_, begin, _ := validationApplyIntent(t, s, head, authority, false)
	result := collectionCommand(t, s, CollectionCommand{Action: "validation_begin", OperationID: head.ID, UploadID: head.UploadID, ValidationBegin: &begin}, upgrade.At)
	if !errors.Is(result.Err, ErrOperatorAuthorityDenied) {
		t.Fatal("new operator adopted legacy upload through reused actor text", result.Err)
	}
	planApplyStateUnchanged(t, s, head)
	if n, b, err := s.fsm.collections.ValidationStats(head.ID); err != nil || n != 0 || b != 0 {
		t.Fatal("denied historical owner allocated result rows", err)
	}
	if _, found, err := s.CollectionGet(head.ID); err != nil || !found {
		t.Fatal("denied adoption hid retained historical input", err)
	}
}

func TestCollectionValidationFaultOwnerRotationBeforeFirstValidation(t *testing.T) {
	s := openCatalogMemory(t)
	head, originalAuthority := validationApplyInput(t, s, 2)
	originalOwner := *head.Owner
	state, err := s.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	replacement := lifecycleReplacement(state)
	replacement.At = head.ActivityAt.Add(time.Millisecond)
	replacement.Principals[0].TokenSHA256 = authenticationVerifier("rotated-before-validation")
	if result := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); result.Err != nil {
		t.Fatal(result.Err)
	}
	at := replacement.At.Add(time.Millisecond)
	current, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil || current.Actor != originalAuthority.Actor || current.Epoch != originalAuthority.Epoch || current.Revision == originalAuthority.Revision {
		t.Fatal("rotation did not preserve permanent identity with a new authority", err)
	}
	_, stale, items := validationApplyIntent(t, s, head, originalAuthority, false)
	command := CollectionCommand{Action: "validation_begin", OperationID: head.ID, UploadID: head.UploadID, ValidationBegin: &stale}
	if result := collectionCommand(t, s, command, at); !errors.Is(result.Err, ErrAuthenticationConflict) {
		t.Fatal("stale policy began validation", result.Err)
	}
	planApplyStateUnchanged(t, s, head)
	fresh := stale
	fresh.Header.Authority = current
	command.ValidationBegin = &fresh
	head = validationApplyAllowed(t, collectionCommand(t, s, command, at))
	if head.Owner == nil || *head.Owner != originalOwner || head.Validation.Header.Authority != current {
		t.Fatal("rotation rebound upload owner instead of capturing validation authority")
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	if *head.Owner != originalOwner || head.Validation.FinalizedAt.IsZero() {
		t.Fatal("new current authority failed to complete original owned inventory")
	}
}

func TestCollectionValidationFaultCapturedOwnerRotationBeforeCreate(t *testing.T) {
	s, state, _ := authorityFixture(t)
	command := collectionCreateFixture(t, s, 1)
	command.Create.Actor = "oncall"
	captured, err := s.ObserveCollectionOwner(context.Background(), command.Create.Actor, command.Create.CreatedAt)
	if err != nil || captured == nil {
		t.Fatal("owner observation", err)
	}
	command.Create.Owner = captured
	replacement := lifecycleReplacement(state)
	replacement.At = command.Create.CreatedAt.Add(time.Millisecond)
	replacement.Principals[0].TokenSHA256 = authenticationVerifier("rotated-after-owner-observation")
	if result := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); result.Err != nil {
		t.Fatal(result.Err)
	}
	// Keep the submission's timestamp current so this rejects the stale policy
	// revision itself, rather than merely rejecting a past observation time.
	at := replacement.At.Add(time.Millisecond)
	command.Create.CreatedAt, command.Create.ActivityAt, command.Create.ExpiresAt = at, at, at.Add(CollectionInactivityLifetime)
	beforeHighWater, beforeCollections := s.fsm.image.OperationHighWater, len(s.fsm.image.Collections)
	beforeLedger := s.fsm.collections
	if result := collectionCommand(t, s, command, at); !errors.Is(result.Err, ErrAuthenticationConflict) {
		t.Fatal("captured stale owner allocated an upload", result.Err)
	}
	if s.fsm.image.OperationHighWater != beforeHighWater || len(s.fsm.image.Collections) != beforeCollections || s.fsm.collections != beforeLedger || s.fsm.err != nil {
		t.Fatal("rejected creation changed allocation or poisoned storage")
	}
	current, err := s.ObserveCollectionOwner(context.Background(), command.Create.Actor, at)
	if err != nil || current == nil || current.Revision == captured.Revision {
		t.Fatal("new current owner observation", err)
	}
	command.Create.Owner = current
	created := validationApplyAllowed(t, collectionCommand(t, s, command, at))
	if created.Owner == nil || *created.Owner != *current || s.fsm.image.OperationHighWater != beforeHighWater+1 {
		t.Fatal("fresh authority failed to allocate exactly one upload")
	}
}
