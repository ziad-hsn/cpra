package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

func executionRetireCommand(head CollectionState) CollectionExecuteCommand {
	return CollectionExecuteCommand{Action: "retire", Binding: head.ExecutionResult.Summary.Binding,
		Retirement: CollectionExecutionRetirementFenceFor(head)}
}

func executionRetirementFixture(t *testing.T, disk bool, count int) (*Store, CollectionState) {
	t.Helper()
	s, head, begin := preparationCacheFixture(t, disk, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		var outcome CollectionItemOutcome
		head, outcome = preparationCacheAccept(t, s, head, begin)
		head = executionResultSettle(t, s, head, outcome.Receipt, ordinal%3 != 0)
	}
	head = executionItemFinalize(t, s, head)
	for !head.ExecutionResult.HistorySealed {
		head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)))
	}
	return s, head
}

func TestCollectionExecutionRetirementCommittedPagesReplayAndRecovery(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionRetirementFixture(t, disk, 131)
			original := head.Clone()
			at := head.ExecutionResult.Summary.FinalizedAt.Add(2 * time.Second)
			first := executionRetireCommand(head)
			beforeBytes, err := s.fsm.collections.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			catalogSequence, highWater := s.fsm.image.CatalogMutationSequence, s.fsm.image.OperationHighWater
			view, status, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil || status.State != "ready" {
				t.Fatal("original protected history view", err, status.State)
			}
			head = validationApplyAllowed(t, executeStoreCommand(t, s, first, at))
			r := head.ExecutionRetirement
			if r == nil || r.Checkpoint.Progress.Processed != 128 || r.complete(head) || s.fsm.image.Version != CollectionExecutionRetirementFormatVersion {
				t.Fatal("retirement did not commit exactly one bounded paired prefix")
			}
			if !reflect.DeepEqual(head.Execution, original.Execution) || !reflect.DeepEqual(head.ExecutionResult, original.ExecutionResult) || head.RemovedRows != 0 || head.Plan.RemovedFragments != 0 || head.Validation.RemovedRows != 0 {
				t.Fatal("retirement changed original facts or source namespaces")
			}
			afterBytes, err := s.fsm.collections.Bytes()
			if err != nil || beforeBytes-afterBytes != r.Checkpoint.Progress.ChargedBytes ||
				s.fsm.image.CatalogMutationSequence != catalogSequence || s.fsm.image.OperationHighWater != highWater {
				t.Fatal("retirement quota or catalog allocation changed incorrectly", err)
			}
			retry := validationApplyAllowed(t, executeStoreCommand(t, s, first, at))
			if !reflect.DeepEqual(retry, head) {
				t.Fatal("lost first-page reply retired another page")
			}
			page, err := view.Page(t.Context(), 0, 100, at)
			if err != nil || len(page.Items) != 100 {
				t.Fatal("source retirement changed retained result view", err)
			}
			partial := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(partial, []byte(collectionExecutionRetirementSnapshotMagic)) {
				t.Fatal("partial retirement missing format12 snapshot")
			}
			f := &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(partial))); err != nil {
				t.Fatal("restore partial retirement", err)
			}
			if !collectionExecutionRetirementsEqual(f.image.Collections[head.ID].ExecutionRetirement, r) || f.collectionOutcomeCommitments[head.ID] != nil || f.collectionTerminalTrees[head.ID] != nil {
				t.Fatal("retired snapshot changed checkpoint or installed executable caches")
			}
			if err := f.collections.Close(); err != nil {
				t.Fatal(err)
			}
			if disk {
				configuration := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), configuration)
				if err != nil {
					t.Fatal("native partial log replay", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				head, _, err = s.CollectionGet(head.ID)
				if err != nil || !collectionExecutionRetirementsEqual(head.ExecutionRetirement, r) {
					t.Fatal("native replay lost original checkpoint", err)
				}
			}
			head = validationApplyAllowed(t, executeStoreCommand(t, s, executionRetireCommand(head), at.Add(time.Second)))
			stats, err := s.fsm.collections.ExecutionStats(head.ID)
			if err != nil || stats != (collectionExecutionStats{}) || !head.ExecutionRetirement.complete(head) {
				t.Fatal("remaining execution records or quota after final paired retirement", err)
			}
			lastBytes, err := s.fsm.collections.Bytes()
			if err != nil || beforeBytes-lastBytes != original.Execution.ChargedBytes {
				t.Fatal("final quota refund differs from original exact execution charge", err)
			}
			final := captureSnapshotBytes(t, s.fsm)
			f = &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(final))); err != nil {
				t.Fatal("fully retired execution restore", err)
			}
			defer f.collections.Close()
			if !f.image.Collections[head.ID].ExecutionRetirement.complete(f.image.Collections[head.ID]) {
				t.Fatal("snapshot forgot final retired commitment")
			}
			if result := collectionCommand(t, s, cleanupCommand(head), at.Add(2*time.Second)); !errors.Is(result.Err, ErrCollectionConflict) {
				t.Fatal("execution-only retirement enabled source cleanup prematurely", result.Err)
			}
		})
	}
}

func TestCollectionExecutionRetirementRejectsForeignFormatAndLiveWork(t *testing.T) {
	_, head := executionRetirementFixture(t, false, 1)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	c := executionRetireCommand(head)
	command := Command{Kind: "collection_execute", CollectionExecute: &c, At: at}
	if commandWriteFormat(command) != CollectionExecutionRetirementFormatVersion {
		t.Fatal("retirement writer did not select format12")
	}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == CollectionExecutionRetirementFormatVersion || version == CollectionExecutionSourceRetirementFormatVersion || version == CollectionReselectionFormatVersion || externalStorageFormat(version)) {
			t.Fatal("retirement envelope format", version, err)
		}
	}
	for _, mutate := range []func(*CollectionExecuteCommand){
		func(c *CollectionExecuteCommand) { c.Authority = *head.Owner },
		func(c *CollectionExecuteCommand) { c.Ordinal = 1 },
		func(c *CollectionExecuteCommand) { c.Action = "begin" },
		func(c *CollectionExecuteCommand) { c.Retirement.Result.ProgressDigest = "bad" },
		func(c *CollectionExecuteCommand) { c.Retirement.Result.HistorySealed = false },
	} {
		changed := executionRetireCommand(head)
		mutate(&changed)
		if changed.validate(at) == nil {
			t.Fatal("retirement accepted foreign fields or malformed result")
		}
	}
	live, original, begin := preparationCacheFixture(t, false, 1)
	original, _ = preparationCacheAccept(t, live, original, begin)
	invalid := executionRetireCommand(head)
	invalid.Binding = begin.Binding
	invalid.Retirement.Result.Summary.Binding = begin.Binding
	invalid.Retirement.Result.Summary.Fence.Progress.Binding = begin.Binding
	if r := executeStoreCommand(t, live, invalid, at); !errors.Is(r.Err, ErrCollectionInvalid) && !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("live parent accepted retirement", r.Err)
	}
}

func TestCollectionExecutionRetirementNoBeginAndPreparedCancellation(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, "create")
			at := head.Activation.At.Add(time.Second)
			if prepared {
				head = validationApplyAllowed(t, executeStoreCommand(t, s, begin, at))
				candidate := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at.Add(time.Second))
				head = validationApplyAllowed(t, executeStoreCommand(t, s, candidate, candidate.Prepared.At))
			}
			head, _ = activationSnapshotCancel(t, s, head, at.Add(3*time.Second))
			head = executionItemFinalize(t, s, head)
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt))
			at = head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			head = validationApplyAllowed(t, executeStoreCommand(t, s, executionRetireCommand(head), at))
			if !head.ExecutionRetirement.complete(head) || head.ExecutionRetirement.PreparedRemoved != prepared {
				t.Fatal("canceled execution retirement lost original prepared accounting")
			}
			f := &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
				t.Fatal("canceled retired snapshot", err)
			}
			defer f.collections.Close()
			_, state, err := s.CollectionExecutionResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil || state.State != "ready" {
				t.Fatal("retirement removed retained canceled result", state, err)
			}
		})
	}
}

func TestCollectionExecutionRetirementRequiresOriginalValidation(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionRetirementFixture(t, disk, 1)
			ledger := s.fsm.collections
			// Keep the sealed descriptor and its counters intact, but damage the
			// original verdict row. Retirement must certify the row before removal.
			ledger.mu.Lock()
			if disk {
				err := ledger.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerValidation).Bucket([]byte(head.ID)).Put(collectionOrdinal(1), []byte("{}"))
				})
				if err != nil {
					ledger.mu.Unlock()
					t.Fatal(err)
				}
			} else {
				ledger.validationRows[head.ID][1] = []byte("{}")
			}
			ledger.mu.Unlock()
			before := catalogDeltaJSON(t, s.fsm.image)
			stats, err := ledger.ExecutionStats(head.ID)
			if err != nil {
				t.Fatal(err)
			}
			c := executionRetireCommand(head)
			raw, err := json.Marshal(envelope{Version: CollectionExecutionRetirementFormatVersion,
				Commands: []Command{{Kind: "collection_execute", CollectionExecute: &c, At: head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)}}})
			if err != nil {
				t.Fatal(err)
			}
			result := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
			if _, failed := result.(error); !failed || s.fsm.err == nil || !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image)) {
				t.Fatal("corrupt original verdict admitted execution retirement", result)
			}
			after, err := ledger.ExecutionStats(head.ID)
			if err != nil || after != stats {
				t.Fatal("failed certification removed execution rows or released their charge", err)
			}
		})
	}
}
