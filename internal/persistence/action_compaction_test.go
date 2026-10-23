//go:build linux || darwin || windows

package persistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestActionCompactionFenceFreezesCollectionState(t *testing.T) {
	state := image{Version: NotificationConditionFormatVersion,
		ActionStorage: &actionStorageState{Sequence: 3, Position: actionLedgerPosition{Index: 3}, Retired: 1, RetiredBytes: 512},
		Collections: map[string]CollectionState{"collection": {ExecutionRetirement: &CollectionExecutionRetirementState{
			Checkpoint: &CollectionExecutionRetirementCheckpoint{Progress: CollectionExecutionProgress{Processed: 2}},
		}}},
	}
	fence := actionCompactionFence(state)
	if !sameLedgerState(fence, state) {
		t.Fatal("captured fence differs before any change")
	}
	state.Collections["collection"].ExecutionRetirement.Checkpoint.Progress.Processed++
	state.ActionStorage.Sequence++
	if sameLedgerState(fence, state) || fence.Collections["collection"].ExecutionRetirement.Checkpoint.Progress.Processed != 2 || fence.ActionStorage.Sequence != 3 {
		t.Fatal("live pointer mutation changed frozen publication identity")
	}
}

func TestActionCompactionPreservesCollectionsAndPinnedEvidence(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	source := publishGenerationFixture(t, owner)
	op, item := ledgerTestOperation(1), ledgerTestItem(1)
	if err := source.Append(op, item); err != nil {
		t.Fatal(err)
	}
	row := testActionRow("completed")
	row.Action.State = Succeeded
	row.Action.FinishedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := source.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}); err != nil {
		t.Fatal(err)
	}
	reader, err := source.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := source.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{{MonitorID: row.MonitorID, Action: Action{ID: row.Action.ID}, Deleted: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CommitActions(actionLedgerPosition{Index: 3}, []actionLedgerRow{testActionRow("unknown")}); err != nil {
		t.Fatal(err)
	}
	frozen, err := source.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	candidate, err := openCollectionLedger(owner.directory, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidate.Close() })
	owner.live.Store(candidate.generation, candidate)
	fence := image{Version: NotificationConditionFormatVersion, ActionStorage: &actionStorageState{Sequence: 3, Position: actionLedgerPosition{Index: 3}, Retired: 1, RetiredBytes: 512}}
	if err := copyCollectionGeneration(t.Context(), fence, frozen, candidate); err != nil {
		t.Fatal(err)
	}
	state, err := candidate.actionAccounting()
	if err != nil || state.Tombstones != 0 || state.Protected != 1 {
		t.Fatalf("replacement logical accounting: %+v, %v", state, err)
	}
	if got, ok, err := candidate.Item(op, 1); err != nil || !ok || got.Key != item.Key {
		t.Fatal("unrelated collection lost", got.Key, ok, err)
	}
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &Store{fsm: &machine{collections: source, image: fence}, collectionGenerations: owner,
		actionCompaction: &actionCompaction{source: source, fence: fence, done: make(chan actionCompactionResult, 1), cancel: cancel}}
	store.actionCompaction.done <- actionCompactionResult{ledger: candidate}
	if condition, err := store.finishActionCompaction(); condition != "" || err != nil {
		t.Fatal(condition, err)
	}
	if store.fsm.collections != candidate || owner.catalog.Current != candidate.generation || owner.catalog.Previous != source.generation {
		t.Fatal("replacement publication did not preserve the completed pair")
	}
	if _, ok, err := reader.Get(t.Context(), row.Action.ID); err != nil || !ok {
		t.Fatal("retired owner invalidated the older cursor", ok, err)
	}
	if source.closedNative.Load() {
		t.Fatal("snapshot and cursor pins did not retain old generation")
	}
	if err := frozen.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !source.closedNative.Load() {
		t.Fatal("old generation remained open after the final pin")
	}
}

func TestActionCompactionRejectsChangedLedgerBeforePublication(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	source := publishGenerationFixture(t, owner)
	candidate, err := openCollectionLedger(owner.directory, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidate.Close() })
	owner.live.Store(candidate.generation, candidate)
	fence := image{Version: NotificationConditionFormatVersion, ActionStorage: &actionStorageState{Sequence: 1, Position: actionLedgerPosition{Index: 1}, Retired: 1, RetiredBytes: 512}}
	current := fence
	current.ActionStorage = &actionStorageState{Sequence: 2, Position: actionLedgerPosition{Index: 2}, Retired: 1, RetiredBytes: 512}
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &Store{fsm: &machine{collections: source, image: current}, collectionGenerations: owner,
		actionCompaction: &actionCompaction{source: source, fence: fence, done: make(chan actionCompactionResult, 1), cancel: cancel}}
	store.actionCompaction.done <- actionCompactionResult{ledger: candidate}
	if condition, err := store.finishActionCompaction(); condition != "state_changed" || err != nil {
		t.Fatal(condition, err)
	}
	if store.fsm.collections != source || owner.catalog.Current != source.generation || !candidate.closedNative.Load() {
		t.Fatal("stale replacement changed current ownership or leaked a writer")
	}
}

func TestActionCompactionCancellationKeepsSource(t *testing.T) {
	source, target := newLedgerTest(t, true, 1<<20), newLedgerTest(t, true, 1<<20)
	frozen, err := source.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := copyCollectionGeneration(ctx, image{Version: NotificationConditionFormatVersion}, frozen, target); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved", err)
	}
	if err := frozen.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Append(ledgerTestOperation(1), ledgerTestItem(1)); err != nil {
		t.Fatal("cancelled copy affected source", err)
	}
}
