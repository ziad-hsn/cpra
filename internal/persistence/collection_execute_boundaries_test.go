package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This helper seals one explicit original row through the production source,
// plan/result and admission commands. It does not run the management compiler
// or plaintext candidate preparation; those remain separate qualifications.
func executeBoundarySeal(t *testing.T, s *Store, change string, target *CatalogRecord) (CollectionState, OperatorAuthority) {
	t.Helper()
	head, authority := validationApplyInput(t, s, 1)
	input, ok, err := s.fsm.collections.Item(head.ID, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	row := planCodecRow(1, 1, input.Key, change)
	row.Source, row.Document, row.Item = input.Source, input.SourceDocument, input.SourceItem
	if target != nil {
		if target.Key != input.Key {
			t.Fatal("fixture target/source mismatch")
		}
		reverse := target.DependentsVersion
		row.Target = CollectionPlanGuard{Key: target.Key, OriginalUID: target.UID, OriginalRevision: target.Revision, OriginalGeneration: int64(target.Generation), ReverseVersion: &reverse}
	}
	header := planCodecHeader(1)
	header.PlanID = uuid.NewString()
	header.OperationID, header.UploadID, header.Actor = head.ID, head.UploadID, head.Actor
	header.IdentityFormat, header.ContentDigest, header.InputProgressDigest = head.IdentityFormat, head.ContentDigest, head.ProgressDigest
	header.ObservedIndex = s.fsm.image.Index
	var wire bytes.Buffer
	descriptor, err := EncodeCollectionPlan(context.Background(), &wire, header, func(e *CollectionPlanEncoder) error {
		if err := e.BeginRow(row); err != nil {
			return err
		}
		return e.EndRow()
	})
	if err != nil {
		t.Fatal(err)
	}
	var fragments []CollectionPlanLedgerFragment
	if _, err := DecodeCollectionPlan(context.Background(), &wire, func(f CollectionPlanFragment) error {
		fragments = append(fragments, CollectionPlanLedgerFragment{Ordinal: uint64(len(fragments) + 1), Fragment: f})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	head = planApplyAll(t, s, planApplyBegin(t, s, head, CollectionPlanBegin{Header: header, Descriptor: descriptor}), fragments)
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Millisecond)))
	result := collectionValidationPlanItem(row)
	digest, cost, err := CollectionValidationNextDigest(CollectionValidationInitialDigest(), result)
	if err != nil {
		t.Fatal(err)
	}
	begin := CollectionValidationBegin{Header: CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID, InputProgressDigest: head.ProgressDigest, ItemCount: 1, Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), Valid: true, PlanID: header.PlanID, PlanDigest: descriptor.Digest}, Descriptor: CollectionValidationDescriptor{Count: 1, Bytes: cost, Digest: digest}}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, []CollectionValidationItem{result}))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	return head, authority
}

func executeBoundaryMachine(t *testing.T, s *Store, disk bool, heads ...CollectionState) (*machine, []*collectionExecutionIndex) {
	t.Helper()
	f := catalogDeltaCloneMachine(t, s.fsm)
	f.collections = newLedgerTest(t, disk, 16<<20)
	a, b, c := validationLedgerStreams(t, s.fsm.collections)
	if err := importCollectionLedger(bytes.NewReader(a), f.collections); err != nil {
		t.Fatal(err)
	}
	if err := importCollectionPlanLedger(bytes.NewReader(b), f.collections); err != nil {
		t.Fatal(err)
	}
	if err := importCollectionValidationLedger(bytes.NewReader(c), f.collections); err != nil {
		t.Fatal(err)
	}
	view, err := f.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	var indexes []*collectionExecutionIndex
	for _, head := range heads {
		x, err := buildCollectionExecutionIndex(context.Background(), head, view, defaultCollectionExecutionIndexLimits())
		if err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, x)
	}
	return f, indexes
}

func executeBoundaryBegin(t *testing.T, head CollectionState, authority OperatorAuthority) CollectionExecuteCommand {
	t.Helper()
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	return CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: authority, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
}

func executeBoundaryUpdate(t *testing.T, begin CollectionExecuteCommand, x *collectionExecutionIndex, at time.Time) CollectionExecuteCommand {
	t.Helper()
	row, ok := x.row(1)
	if !ok || row.Row.Change != "update" {
		t.Fatal("expected update")
	}
	r := catalogDeltaRecord(row.Row.Key.Kind, row.Row.Key.ID)
	r.UID, r.Revision, r.Generation = row.Row.Target.OriginalUID, "updated-"+uuid.NewString(), uint64(row.Row.Target.OriginalGeneration)+1
	r.CreatedAt, r.UpdatedAt = at.Add(-time.Second), at
	begin.Action, begin.Ordinal = "prepare", 1
	begin.Prepared = &CollectionPreparedItem{Binding: begin.Binding, ID: uuid.NewString(), Ordinal: 1, InputOrdinal: row.Row.InputOrdinal, RowDigest: row.RowDigest, At: at, Record: r}
	return begin
}

func TestCollectionExecuteBoundaryUpdateAndUnchanged(t *testing.T) {
	for _, change := range []string{"update", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			s := openCatalogMemory(t)
			seed := catalogDeltaRecord("Credential", "item-00001")
			seed.CreatedAt, seed.UpdatedAt = time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-time.Hour)
			original := createCatalog(t, s, seed)
			head, authority := executeBoundarySeal(t, s, change, &original)
			f, indexes := executeBoundaryMachine(t, s, false, head)
			x := indexes[0]
			begin := executeBoundaryBegin(t, head, authority)
			at := head.Activation.At.Add(time.Second)
			if r := executeApply(t, f, begin, at, x); r.Err != nil {
				t.Fatal(r.Err)
			}
			decision := begin
			decision.Action, decision.Ordinal = "decide", 1
			if change == "update" {
				candidate := executeBoundaryUpdate(t, begin, x, at.Add(time.Second))
				candidate.Prepared.Record.CreatedAt = original.CreatedAt
				if r := executeApply(t, f, candidate, candidate.Prepared.At, x); r.Err != nil {
					t.Fatal(r.Err)
				}
				decision = executeDecision(candidate)
			}
			beforeCatalog := f.image.Catalog[original.Key.indexKey()]
			high, token := f.image.OperationHighWater, f.image.CatalogMutationSequence
			r := executeApply(t, f, decision, at.Add(2*time.Second), x)
			if r.Err != nil || !r.Allowed {
				t.Fatal("decision", r.Err)
			}
			evidence, found, err := f.collections.ExecutionRecord(head.ID, collectionExecutionOutcomeSlot(1))
			if err != nil || !found {
				t.Fatal(err)
			}
			if change == "unchanged" {
				if r.Operation != nil || r.Collection.Execution.Unchanged != 1 || evidence.Outcome.Decision != "unchanged" || f.image.OperationHighWater != high || f.image.CatalogMutationSequence != token || !reflect.DeepEqual(beforeCatalog, f.image.Catalog[original.Key.indexKey()]) || len(f.collectionChildren) != 0 {
					t.Fatal("unchanged allocated child or mutated catalog")
				}
			} else {
				got := f.image.Catalog[original.Key.indexKey()]
				if r.Operation == nil || r.Collection.Execution.Accepted != 1 || got.UID != original.UID || got.Revision == original.Revision || got.Generation != original.Generation+1 || r.Operation.OldVersion != original.Revision || f.image.OperationHighWater != high+1 || f.image.CatalogMutationSequence <= token || evidence.Outcome.MutationSequence != f.image.CatalogMutationSequence {
					t.Fatal("update lost original identity or unique mutation token")
				}
			}
			before := catalogDeltaJSON(t, f.image)
			retry := f.applyCollectionExecution(decision, f.image.Index+1, at.Add(3*time.Second), x)
			if retry.Err != nil || !retry.Allowed || len(retry.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("retry replaced original result", retry.Err)
			}
		})
	}
}

func executeBoundaryTwoParents(t *testing.T, disk bool) (*machine, []CollectionState, []CollectionExecuteCommand, []*collectionExecutionIndex) {
	t.Helper()
	s := openCatalogMemory(t)
	first, a := executeBoundarySeal(t, s, "create", nil)
	input, _, _ := s.fsm.collections.Item(first.ID, 1)
	// The second plan records the anticipated first candidate tuple for this
	// direct atomic supersession fixture; it is not a compiler observation.
	// Its guard is tested against the actual first accepted catalog record.
	expected := catalogDeltaRecord(input.Key.Kind, input.Key.ID)
	second, b := executeBoundarySeal(t, s, "update", &expected)
	f, x := executeBoundaryMachine(t, s, disk, first, second)
	heads := []CollectionState{first, second}
	begins := []CollectionExecuteCommand{executeBoundaryBegin(t, first, a), executeBoundaryBegin(t, second, b)}
	at := second.Activation.At.Add(time.Second)
	if r := executeApply(t, f, begins[0], at, x[0]); r.Err != nil {
		t.Fatal(r.Err)
	}
	candidate := executeCandidate(t, begins[0], x[0], 1, at.Add(time.Second))
	if r := executeApply(t, f, candidate, candidate.Prepared.At, x[0]); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := executeApply(t, f, executeDecision(candidate), at.Add(2*time.Second), x[0]); r.Err != nil || r.Operation == nil {
		t.Fatal("first parent acceptance", r.Err)
	}
	if r := executeApply(t, f, begins[1], at.Add(3*time.Second), x[1]); r.Err != nil {
		t.Fatal(r.Err)
	}
	update := executeBoundaryUpdate(t, begins[1], x[1], at.Add(4*time.Second))
	update.Prepared.Record.CreatedAt = f.image.Catalog[update.Prepared.Record.Key.indexKey()].CreatedAt
	if r := executeApply(t, f, update, update.Prepared.At, x[1]); r.Err != nil {
		t.Fatal(r.Err)
	}
	return f, heads, []CollectionExecuteCommand{candidate, update}, x
}

func TestCollectionExecuteBoundaryCrossParentSupersession(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f, heads, candidates, x := executeBoundaryTwoParents(t, disk)
			first, _, err := f.collections.ExecutionRecord(heads[0].ID, collectionExecutionOutcomeSlot(1))
			if err != nil {
				t.Fatal(err)
			}
			originalChild := first.Outcome.Receipt.ID
			high := f.image.OperationHighWater
			r := executeApply(t, f, executeDecision(candidates[1]), candidates[1].Prepared.At.Add(time.Second), x[1])
			if r.Err != nil || r.Operation == nil {
				t.Fatal("supersession", r.Err)
			}
			terminal, found, err := f.collections.ExecutionRecord(heads[0].ID, collectionExecutionTerminalSlot(1))
			if err != nil || !found || terminal.Terminal.ChildID != originalChild || terminal.Terminal.Outcome != "superseded" {
				t.Fatal("original terminal absent", err)
			}
			old := f.image.Collections[heads[0].ID].Execution
			next := f.image.Collections[heads[1].ID].Execution
			if old.ChildSuperseded != 1 || old.ChildTerminals != 1 || next.Accepted != 1 || next.Prepared != nil || len(f.collectionChildren) != 1 || f.image.OperationHighWater != high+1 {
				t.Fatal("cross-parent progress was not atomic")
			}
			if _, ok := f.image.Operations[originalChild]; ok {
				t.Fatal("superseded child still pending")
			}
			if f.collectionChildren[r.Operation.ID].Binding.OperationID != heads[1].ID {
				t.Fatal("new child linked to wrong parent")
			}
			if _, ok := f.image.Operations[r.Operation.ID]; !ok {
				t.Fatal("new child missing")
			}
			for _, head := range heads {
				p := f.image.Collections[head.ID].Execution
				if !f.collectionOutcomeCommitments[head.ID].matches(p.Binding, p.Processed, p.OutcomeDigest) {
					t.Fatal("outcome cache mismatches parent")
				}
			}
		})
	}
}

func TestCollectionExecuteBoundaryCrossParentNativeWriteFailure(t *testing.T) {
	f, heads, candidates, x := executeBoundaryTwoParents(t, true)
	before := catalogDeltaJSON(t, f.image)
	originalChild, _, _ := f.collections.ExecutionRecord(heads[0].ID, collectionExecutionOutcomeSlot(1))
	priorChildren := make(collectionChildLinks)
	for id, link := range f.collectionChildren {
		priorChildren[id] = link
	}
	cacheA, cacheB := f.collectionOutcomeCommitments[heads[0].ID], f.collectionOutcomeCommitments[heads[1].ID]
	oldStats, _ := f.collections.ExecutionStats(heads[0].ID)
	newStats, _ := f.collections.ExecutionStats(heads[1].ID)
	childCommitDenyWrites(t, f.collections)
	r := f.applyCollectionExecution(executeDecision(candidates[1]), f.image.Index+1, candidates[1].Prepared.At.Add(time.Second), x[1])
	var path *os.PathError
	if r.Allowed || !errors.As(r.Err, &path) || f.err == nil || len(r.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || !reflect.DeepEqual(priorChildren, f.collectionChildren) || f.collectionOutcomeCommitments[heads[0].ID] != cacheA || f.collectionOutcomeCommitments[heads[1].ID] != cacheB {
		t.Fatal("failed transaction changed either parent's active state", r.Err)
	}
	afterOld, err := f.collections.ExecutionStats(heads[0].ID)
	if err != nil || afterOld != oldStats {
		t.Fatal("old parent ledger changed", err)
	}
	afterNew, err := f.collections.ExecutionStats(heads[1].ID)
	if err != nil || afterNew != newStats {
		t.Fatal("new parent ledger changed", err)
	}
	if _, found, err := f.collections.ExecutionRecord(heads[0].ID, collectionExecutionTerminalSlot(1)); err != nil || found {
		t.Fatal("uncommitted terminal became visible", err)
	}
	if _, found, err := f.collections.ExecutionRecord(heads[1].ID, collectionExecutionOutcomeSlot(1)); err != nil || found {
		t.Fatal("uncommitted acceptance became visible", err)
	}
	if _, ok := f.image.Operations[originalChild.Outcome.Receipt.ID]; !ok {
		t.Fatal("old child lost after failure")
	}
}

func TestCollectionExecuteBoundaryNoCandidateTargetConflict(t *testing.T) {
	f, head, begin, x := executeFixture(t, false)
	at := head.Activation.At.Add(time.Second)
	if r := executeApply(t, f, begin, at, x); r.Err != nil {
		t.Fatal(r.Err)
	}
	decide := begin
	decide.Action, decide.Ordinal = "decide", 1
	before := catalogDeltaJSON(t, f.image)
	if r := f.applyCollectionExecution(decide, f.image.Index+1, at.Add(time.Second), x); !errors.Is(r.Err, ErrCollectionConflict) || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
		t.Fatal("no-candidate decision invented conflict against unchanged target", r.Err)
	}
	row, _ := x.row(1)
	outside := catalogDeltaRecord(row.Row.Key.Kind, row.Row.Key.ID)
	outside.UID = "outside-uid"
	outside.CreatedAt, outside.UpdatedAt = at.Add(time.Second), at.Add(time.Second)
	if r := f.applyCatalog(CatalogMutation{Record: outside, Create: true}, f.image.Index+1, at.Add(time.Second), CatalogMutationFormatVersion); r.Err != nil {
		t.Fatal(r.Err)
	}
	f.image.Index++
	high, token := f.image.OperationHighWater, f.image.CatalogMutationSequence
	r := executeApply(t, f, decide, at.Add(2*time.Second), x)
	if r.Err != nil || !r.Allowed || r.Operation != nil || r.Collection.Execution.Conflicts != 1 || r.Collection.Execution.Prepared != nil || f.image.OperationHighWater != high || f.image.CatalogMutationSequence != token || f.image.Catalog[outside.Key.indexKey()].UID != "outside-uid" {
		t.Fatal("known original target conflict did not remain side-effect free", r.Err)
	}
}

func TestCollectionExecuteBoundaryAuthorityAndLifecycleFences(t *testing.T) {
	for _, mode := range []string{"revoked", "canceled", "old-epoch", "profile"} {
		t.Run(mode, func(t *testing.T) {
			f, head, begin, x := executeFixture(t, false)
			at := head.Activation.At.Add(time.Second)
			if r := executeApply(t, f, begin, at, x); r.Err != nil {
				t.Fatal(r.Err)
			}
			candidate := executeCandidate(t, begin, x, 1, at.Add(time.Second))
			if r := executeApply(t, f, candidate, candidate.Prepared.At, x); r.Err != nil {
				t.Fatal(r.Err)
			}
			switch mode {
			case "revoked":
				for j := range f.image.Authentication.Principals {
					if f.image.Authentication.Principals[j].ID == head.Actor {
						f.image.Authentication.Principals[j].Revoked = true
					}
				}
			case "canceled":
				state := f.image.Collections[head.ID].Clone()
				state.Phase = "canceled"
				state.TerminalAt = at.Add(2 * time.Second)
				state.Cancellation = &CollectionCancellation{ID: uuid.NewString(), Actor: state.Actor, At: state.TerminalAt}
				f.image.Collections[state.ID] = state
			case "old-epoch":
				f.image.OperationEpoch = uuid.NewString()
				f.image.OperationHighWater = 0
			case "profile":
				candidate.CapabilitiesDigest = strings.Repeat("e", 64)
			}
			before := catalogDeltaJSON(t, f.image)
			stats, _ := f.collections.ExecutionStats(head.ID)
			r := f.applyCollectionExecution(executeDecision(candidate), f.image.Index+1, at.Add(3*time.Second), x)
			if r.Err == nil || r.Allowed || len(r.Events) != 0 || f.err != nil || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("unauthorized/obsolete request changed execution", r.Err)
			}
			after, _ := f.collections.ExecutionStats(head.ID)
			if after != stats {
				t.Fatal("rejection changed prepared evidence")
			}
			if mode == "revoked" && !errors.Is(r.Err, ErrOperatorAuthorityDenied) || mode == "old-epoch" && !errors.Is(r.Err, ErrOperationExpired) || mode == "canceled" && !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("wrong lifecycle classification", r.Err)
			}
		})
	}
}
