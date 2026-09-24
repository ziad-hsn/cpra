package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
)

func executionResultInput(t *testing.T, s *Store, change string) (CollectionState, CollectionExecuteCommand) {
	t.Helper()
	var target *CatalogRecord
	if change == "unchanged" {
		record := catalogDeltaRecord("Credential", "item-00001")
		record.CreatedAt, record.UpdatedAt = time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-time.Hour)
		r := createCatalog(t, s, record)
		target = &r
	}
	head, authority := executeBoundarySeal(t, s, change, target)
	begin := executeBoundaryBegin(t, head, authority)
	return head, begin
}

func executionResultDecide(t *testing.T, s *Store, head CollectionState, begin CollectionExecuteCommand, decision string) (CollectionState, *OperationReceipt) {
	t.Helper()
	at := head.Activation.At.Add(time.Second)
	if r := executeStoreCommand(t, s, begin, at); r.Err != nil {
		t.Fatal(r.Err)
	}
	command := begin
	command.Action, command.Ordinal = "decide", 1
	if decision == "accepted" {
		prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at.Add(time.Second))
		if r := executeStoreCommand(t, s, prepared, prepared.Prepared.At); r.Err != nil {
			t.Fatal(r.Err)
		}
		command = executeDecision(prepared)
	} else if decision == "conflict" {
		record := catalogDeltaRecord("Credential", "item-00001")
		record.CreatedAt, record.UpdatedAt = at, at
		createCatalog(t, s, record)
	}
	result := executeStoreCommand(t, s, command, at.Add(2*time.Second))
	if result.Err != nil || !result.Allowed {
		t.Fatal("decide", result.Err)
	}
	return *result.Collection, result.Operation
}

func executionResultSettle(t *testing.T, s *Store, head CollectionState, child *OperationReceipt, applied bool) CollectionState {
	t.Helper()
	update := OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion, Applied: applied}
	result, err := s.Submit(context.Background(), []Command{{Kind: "operation", Operation: &update, At: head.Execution.LastAt.Add(time.Second)}})
	if err != nil || len(result) != 1 || result[0].Err != nil {
		t.Fatal("settle", result, err)
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	return s.fsm.image.Collections[head.ID].Clone()
}

func executionResultFinalizeCommand(t *testing.T, head CollectionState) CollectionExecuteCommand {
	t.Helper()
	b, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	return CollectionExecuteCommand{Action: "finalize", Binding: b, Finalize: CollectionExecutionFinalizeFenceFor(head)}
}

func executionResultLog(t *testing.T, f *machine, c CollectionExecuteCommand, at time.Time) Result {
	t.Helper()
	raw, err := json.Marshal(envelope{Version: CollectionExecutionResultFormatVersion, Commands: []Command{{Kind: "collection_execute", CollectionExecute: &c, At: at}}})
	if err != nil {
		t.Fatal(err)
	}
	result := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw})
	rows, ok := result.([]Result)
	if !ok || len(rows) != 1 {
		t.Fatalf("finalization log failed: %v", result)
	}
	return rows[0]
}

func TestCollectionExecutionResultClassificationAndImmutableRetry(t *testing.T) {
	for _, tc := range []struct {
		name, change, decision, outcome string
		applied                         bool
	}{
		{"applied", "create", "accepted", "completed", true},
		{"projection failed stays committed", "create", "accepted", "partial", false},
		{"all unchanged", "unchanged", "unchanged", "completed", false},
		{"all conflicts", "create", "conflict", "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, tc.change)
			initialActivity := head.ActivityAt
			head, child := executionResultDecide(t, s, head, begin, tc.decision)
			if child != nil {
				head = executionResultSettle(t, s, head, child, tc.applied)
			}
			command := executionResultFinalizeCommand(t, head)
			at := head.Execution.LastAt.Add(time.Second)
			beforeCatalog, beforeToken, beforeHighWater := catalogDeltaJSON(t, s.fsm.image.Catalog), s.fsm.image.CatalogMutationSequence, s.fsm.image.OperationHighWater
			result := executeStoreCommand(t, s, command, at)
			if result.Err != nil || !result.Allowed || result.Collection.Phase != tc.outcome || result.Collection.ExecutionResult == nil || len(result.Events) != 2 {
				t.Fatal("finalize", result, result.Err)
			}
			if s.fsm.collectionExecutionIndex != nil || s.fsm.collectionExecutionLedger != nil {
				t.Fatal("finalization retained disposable executor plan index")
			}
			got := result.Collection.ExecutionResult.Summary
			if got.Outcome != tc.outcome || !got.FinalizedAt.Equal(at) || got.Unattempted != 0 || !result.Collection.ActivityAt.Equal(initialActivity) ||
				!bytes.Equal(beforeCatalog, catalogDeltaJSON(t, s.fsm.image.Catalog)) || beforeToken != s.fsm.image.CatalogMutationSequence || beforeHighWater != s.fsm.image.OperationHighWater || len(s.fsm.collectionChildren) != 0 {
				t.Fatal("finalization changed execution facts")
			}
			if child != nil && (got.Fence.Progress.Accepted != 1 || got.Fence.Progress.ChildApplied != map[bool]uint64{true: 1, false: 0}[tc.applied]) {
				t.Fatal("committed/applied distinction lost")
			}
			if result.Collection.ExecutionResult.HistorySealed || result.Collection.ExecutionResult.Published != 0 {
				t.Fatal("anchor claimed published items")
			}
			receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || receipt.Execution == nil || receipt.Phase != tc.outcome {
				t.Fatal("terminal receipt", receipt, err)
			}
			page, err := s.History().Page("collection-execution/"+head.ID, "", 10)
			if err != nil || len(page.Events) != 1 || page.Events[0].CollectionExecution == nil || !page.Events[0].At.Equal(at) {
				t.Fatal("anchor", page, err)
			}
			retry := executeStoreCommand(t, s, command, at.Add(time.Hour))
			if retry.Err != nil || len(retry.Events) != 0 || !collectionExecutionSummariesEqual(&got, &retry.Collection.ExecutionResult.Summary) {
				t.Fatal("retry replaced original result", retry.Err)
			}
			again, _ := s.History().Page("collection-execution/"+head.ID, "", 10)
			if len(again.Events) != 1 {
				t.Fatal("retry appended anchor")
			}
			work, err := s.CollectionExecutionWork(context.Background(), at.Add(time.Second))
			if err != nil || len(work) != 0 {
				t.Fatal("finalized parent offered for execution", work, err)
			}
			blob := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(blob, []byte(collectionExecutionResultSnapshotMagic)) {
				t.Fatal("missing explicit format10")
			}
			f := &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(blob))); err != nil {
				t.Fatal("restore", err)
			}
			defer f.collections.Close()
			recovered := executionResultLog(t, f, command, at.Add(2*time.Hour))
			if recovered.Err != nil || len(recovered.Events) != 0 || !collectionExecutionSummariesEqual(&got, &recovered.Collection.ExecutionResult.Summary) {
				t.Fatal("replayed retry changed result", recovered.Err)
			}
			result.Collection.ExecutionResult.Summary.Fence.Progress.Processed = 0
			if s.fsm.image.Collections[head.ID].ExecutionResult.Summary.Fence.Progress.Processed != 1 {
				t.Fatal("returned mutable alias")
			}
		})
	}
}

func TestCollectionExecutionResultWaitsForOriginalChildAndFence(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, begin, "accepted")
	stale := executionResultFinalizeCommand(t, head)
	if r := executeStoreCommand(t, s, stale, head.Execution.LastAt.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) || r.Allowed {
		t.Fatal("pending child finalized", r.Err)
	}
	head = executionResultSettle(t, s, head, child, true)
	if r := executeStoreCommand(t, s, stale, head.Execution.LastAt.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("stale terminal root accepted", r.Err)
	}
	command := executionResultFinalizeCommand(t, head)
	if r := executeStoreCommand(t, s, command, head.Execution.LastAt.Add(-time.Nanosecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("backward observation accepted", r.Err)
	}
	s.fsm.mu.Lock()
	for n := range s.fsm.image.Authentication.Principals {
		s.fsm.image.Authentication.Principals[n].Revoked = true
	}
	s.fsm.mu.Unlock()
	if r := executeStoreCommand(t, s, command, head.Execution.LastAt.Add(time.Second)); r.Err != nil || !r.Allowed {
		t.Fatal("credential revocation prevented committed evidence", r.Err)
	}
}

func TestCollectionExecutionResultCancellationPreservesReceiptAndAbandonedPreparation(t *testing.T) {
	for _, stage := range []string{"before begin", "prepared", "child pending"} {
		t.Run(stage, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, "create")
			var child *OperationReceipt
			at := head.Activation.At.Add(time.Second)
			if stage == "child pending" {
				head, child = executionResultDecide(t, s, head, begin, "accepted")
				at = head.Execution.LastAt.Add(time.Second)
			}
			if stage == "prepared" {
				head = validationApplyAllowed(t, executeStoreCommand(t, s, begin, at))
				prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at.Add(time.Second))
				head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
				at = prepared.Prepared.At.Add(time.Second)
			}
			head, _ = activationSnapshotCancel(t, s, head, at)
			original, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil {
				t.Fatal(err)
			}
			if child != nil {
				if r := executeStoreCommand(t, s, executionResultFinalizeCommand(t, head), at); !errors.Is(r.Err, ErrCollectionConflict) {
					t.Fatal("pending canceled child finalized", r.Err)
				}
				head = executionResultSettle(t, s, head, child, false)
			}
			at = at.Add(3 * time.Second)
			result := executeStoreCommand(t, s, executionResultFinalizeCommand(t, head), at)
			if result.Err != nil || len(result.Events) != 1 || result.Collection.Phase != "canceled" || result.Collection.ExecutionResult == nil || !result.Collection.TerminalAt.Equal(original.TerminalAt) {
				t.Fatal("canceled finalization", result.Err)
			}
			current, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || !collectionReceiptsEqual(original, current) {
				t.Fatal("late result replaced cancellation receipt", err)
			}
			summary := result.Collection.ExecutionResult.Summary
			if stage == "before begin" && (result.Collection.Execution != nil || summary.Fence.Progress != nil || summary.Unattempted != head.ItemCount) {
				t.Fatal("invented execution begin")
			}
			if stage == "prepared" && (summary.Fence.Progress.Prepared == nil || summary.Fence.Progress.Accepted != 0 || summary.Unattempted != head.ItemCount) {
				t.Fatal("abandoned candidate counted as accepted")
			}
			if _, l, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), ""); err != nil {
				t.Fatal("stopped summary snapshot", err)
			} else {
				l.Close()
			}
			cleanup := CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &CollectionCleanup{}}
			if r := s.fsm.cleanupCollection(cleanup, at); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("unpublished execution evidence retired", r.Err)
			}
		})
	}
}

func TestCollectionExecutionResultStrictFormatsAndHistoricalWire(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	finalize := executionResultFinalizeCommand(t, head)
	command := Command{Kind: "collection_execute", CollectionExecute: &finalize, At: head.TerminalAt.Add(time.Second)}
	if commandWriteFormat(command) != 10 || commandWriteFormat(Command{CollectionExecute: &begin}) != 9 {
		t.Fatal("writer format changed")
	}
	for version := 1; version <= 11; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == 10 || version == 11) {
			t.Fatal("wrong finalize format acceptance", version, err)
		}
	}
	for _, mutate := range []func(*CollectionExecuteCommand){
		func(c *CollectionExecuteCommand) { c.Authority = begin.Authority },
		func(c *CollectionExecuteCommand) { c.CapabilitiesDigest = begin.CapabilitiesDigest },
		func(c *CollectionExecuteCommand) { c.Ordinal = 1 },
		func(c *CollectionExecuteCommand) { c.PreparedID = uuid.NewString() },
		func(c *CollectionExecuteCommand) { c.Finalize = nil },
		func(c *CollectionExecuteCommand) { c.Action = "begin" },
	} {
		c := finalize
		mutate(&c)
		if c.validate(command.At) == nil {
			t.Fatal("finalize ignored foreign/absent fields")
		}
	}
	// Frozen format9 nested projection: the new optional field must not alter any
	// historical byte, including order and explicit zero-value authority fields.
	type format9Execute struct {
		Action             string                     `json:"action"`
		Binding            CollectionExecutionBinding `json:"binding"`
		Authority          OperatorAuthority          `json:"authority"`
		CapabilitiesDigest string                     `json:"capabilities_digest"`
		Ordinal            uint64                     `json:"ordinal,omitempty"`
		PreparedID         string                     `json:"prepared_id,omitempty"`
		Prepared           *CollectionPreparedItem    `json:"prepared,omitempty"`
	}
	old := format9Execute{begin.Action, begin.Binding, begin.Authority, begin.CapabilitiesDigest, begin.Ordinal, begin.PreparedID, begin.Prepared}
	want, _ := json.Marshal(old)
	got, _ := json.Marshal(begin)
	if !bytes.Equal(want, got) {
		t.Fatal("historical format9 wire changed", string(got))
	}
}

func TestCollectionExecutionResultSnapshotRejectsChangedFacts(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, begin, "accepted")
	head = executionResultSettle(t, s, head, child, true)
	r := executeStoreCommand(t, s, executionResultFinalizeCommand(t, head), head.Execution.LastAt.Add(time.Second))
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	blob := captureSnapshotBytes(t, s.fsm)
	for name, mutate := range map[string]func(*image){
		"future projection": func(i *image) {
			s := i.Collections[head.ID]
			s.ExecutionResult.Summary.Version++
			i.Collections[head.ID] = s
		},
		"changed outcome": func(i *image) {
			s := i.Collections[head.ID]
			s.ExecutionResult.Summary.Outcome = "partial"
			i.Collections[head.ID] = s
		},
		"changed root": func(i *image) {
			s := i.Collections[head.ID]
			s.ExecutionResult.Summary.Fence.Progress.TerminalRoot = identity("different")
			i.Collections[head.ID] = s
		},
		"unproved publication": func(i *image) {
			s := i.Collections[head.ID]
			s.ExecutionResult.HistorySealed = true
			i.Collections[head.ID] = s
		},
		"changed original actor": func(i *image) {
			s := i.Collections[head.ID]
			s.ExecutionResult.Summary.Actor = "somebody-else"
			i.Collections[head.ID] = s
		},
		"missing immutable summary": func(i *image) { s := i.Collections[head.ID]; s.ExecutionResult = nil; i.Collections[head.ID] = s },
	} {
		t.Run(name, func(t *testing.T) {
			rest := blob[len(collectionExecutionResultSnapshotMagic):]
			end := bytes.IndexByte(rest, '\n')
			var i image
			if err := json.Unmarshal(rest[:end], &i); err != nil {
				t.Fatal(err)
			}
			mutate(&i)
			raw, _ := json.Marshal(i)
			forged := append([]byte(collectionExecutionResultSnapshotMagic), raw...)
			forged = append(forged, rest[end:]...)
			if _, l, err := decodeSnapshot(bytes.NewReader(forged), ""); err == nil {
				l.Close()
				t.Fatal("forged result accepted")
			}
		})
	}
	downgraded := bytes.Replace(blob, []byte(collectionExecutionResultSnapshotMagic), []byte(collectionExecutionSnapshotMagic), 1)
	downgraded = bytes.Replace(downgraded, []byte(`"version":10`), []byte(`"version":9`), 1)
	if _, l, err := decodeSnapshot(bytes.NewReader(downgraded), ""); err == nil {
		l.Close()
		t.Fatal("format9 silently accepted result")
	}
}

func TestCollectionExecutionResultColdCertificationRejectsMissingEvidence(t *testing.T) {
	for _, namespace := range []string{"input", "outcome", "terminal", "orphan"} {
		t.Run(namespace, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, "create")
			head, child := executionResultDecide(t, s, head, begin, "accepted")
			head = executionResultSettle(t, s, head, child, true)
			c := executionResultFinalizeCommand(t, head)
			s.fsm.collections.mu.Lock()
			switch namespace {
			case "input":
				delete(s.fsm.collections.rows[head.ID], 1)
			case "outcome":
				delete(s.fsm.collections.executionRows[head.ID], collectionExecutionOutcomeSlot(1))
			case "terminal":
				delete(s.fsm.collections.executionRows[head.ID], collectionExecutionTerminalSlot(1))
			case "orphan":
				s.fsm.collections.executionRows[head.ID]["unknown"] = []byte(`{}`)
			}
			s.fsm.collections.mu.Unlock()
			before := catalogDeltaJSON(t, s.fsm.image)
			raw, _ := json.Marshal(envelope{Version: 10, Commands: []Command{{Kind: "collection_execute", CollectionExecute: &c, At: head.Execution.LastAt.Add(time.Second)}}})
			result := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
			if _, ok := result.(error); !ok || s.fsm.err == nil || !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image)) {
				t.Fatal("corrupt namespace finalized", result)
			}
		})
	}
}

// Classification cases beyond the one-row transaction fixtures preserve the
// bounded progress contract while checking explicit decision/child distinctions.
func TestCollectionExecutionResultOutcomeTable(t *testing.T) {
	for _, tc := range []struct {
		progress CollectionExecutionProgress
		outcome  string
		ready    bool
	}{
		{CollectionExecutionProgress{ItemCount: 3, Processed: 2, Accepted: 2, ChildTerminals: 2, ChildApplied: 2}, "", false},
		{CollectionExecutionProgress{ItemCount: 3, Processed: 3, Accepted: 2, Unchanged: 1, ChildTerminals: 2, ChildApplied: 2}, "completed", true},
		{CollectionExecutionProgress{ItemCount: 3, Processed: 3, Accepted: 2, Conflicts: 1, ChildTerminals: 2, ChildApplied: 2}, "partial", true},
		{CollectionExecutionProgress{ItemCount: 3, Processed: 3, Accepted: 3, ChildTerminals: 3, ChildSuperseded: 3}, "partial", true},
		{CollectionExecutionProgress{ItemCount: 3, Processed: 3, Conflicts: 1, DependencyBlocked: 2}, "failed", true},
	} {
		t.Run(fmt.Sprint(tc.progress), func(t *testing.T) {
			outcome, ready := collectionExecutionOutcome(CollectionExecutionFinalizeFence{Phase: "applying", Progress: &tc.progress})
			if outcome != tc.outcome || ready != tc.ready {
				t.Fatal(outcome, ready)
			}
		})
	}
}

func TestCollectionExecutionResultUnstartedAuditQuotaPreservesHealth(t *testing.T) {
	s, head := executionIndexFixture(t, false, "over-limit")
	head, admission := activationSnapshotAdmit(t, s, head)
	begin := executeBoundaryBegin(t, head, *admission.ActivationAuthority)
	at := head.Activation.At.Add(time.Second)
	if r := executeStoreCommand(t, s, begin, at); !errors.Is(r.Err, ErrCollectionQuota) || r.Allowed {
		t.Fatal("oversized begin", r.Err)
	}
	head, _ = activationSnapshotCancel(t, s, head, at.Add(time.Second))
	c := executionResultFinalizeCommand(t, head)
	before := catalogDeltaJSON(t, head)
	result := executeStoreCommand(t, s, c, at.Add(2*time.Second))
	if !errors.Is(result.Err, ErrCollectionQuota) || result.Allowed || len(result.Events) != 0 || s.fsm.err != nil || !s.Status().Ready {
		t.Fatal("valid oversized unstarted admission poisoned store", result.Err, s.fsm.err)
	}
	if !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image.Collections[head.ID])) {
		t.Fatal("unqualified finalization changed retained admission")
	}
	if err := s.maintainCollections(at.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image.Collections[head.ID])) {
		t.Fatal("unfinalized admitted input was retired")
	}
}
