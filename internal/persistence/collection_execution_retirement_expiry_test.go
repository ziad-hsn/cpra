package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func assertExecutionRetirementExpiryState(t *testing.T, f *machine, want CollectionState, wantBytes int64) {
	t.Helper()
	got := f.image.Collections[want.ID]
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("history expiry or stale retry changed the original retirement state", err)
	}
	used, err := f.collections.Bytes()
	if err != nil || used != wantBytes {
		t.Fatal("history expiry or stale retry changed the exact quota refund", used, wantBytes, err)
	}
	expected, err := collectionExecutionRetirementStats(*want.ExecutionRetirement.Checkpoint, *want.Execution, want.ExecutionRetirement.PreparedRemoved)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := f.collections.ExecutionStats(want.ID)
	if err != nil || actual != expected {
		t.Fatal("retired suffix accounting changed", actual, expected, err)
	}
	if f.collectionOutcomeCommitments[want.ID] != nil || f.collectionTerminalTrees[want.ID] != nil || len(f.collectionChildren) != 0 {
		t.Fatal("retirement or replay restored executable caches or child ownership")
	}
}

func executionRetirementExpiryLog(t *testing.T, f *machine, c CollectionExecuteCommand, at time.Time) CollectionState {
	t.Helper()
	raw, err := json.Marshal(envelope{Version: CollectionExecutionRetirementFormatVersion,
		Commands: []Command{{Kind: "collection_execute", CollectionExecute: &c, At: at}}})
	if err != nil {
		t.Fatal(err)
	}
	result := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw})
	rows, ok := result.([]Result)
	if !ok || len(rows) != 1 {
		t.Fatalf("retirement replay failed: %v", result)
	}
	return validationApplyAllowed(t, rows[0])
}

// A separate expired history store prevents the restored FSM's replay from
// advancing the live fixture's history index. The original collection snapshot
// and encoded retirement commands still pass through production Restore/Apply.
func restoreExecutionRetirementAfterExpiry(t *testing.T, snapshot []byte, index uint64, expiredAt time.Time) *machine {
	t.Helper()
	history, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	if err := history.append(index, nil, expiredAt); err != nil {
		t.Fatal(err)
	}
	f := &machine{history: history}
	if err := f.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
		t.Fatal("restore with execution history already expired", err)
	}
	t.Cleanup(func() { _ = f.collections.Close() })
	return f
}

func TestCollectionExecutionRetirementHistoryExpiryBetweenBatches(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionRetirementFixture(t, disk, 131)
			original := head.Clone()
			beforeBytes, err := s.fsm.collections.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			catalogSequence, highWater := s.fsm.image.CatalogMutationSequence, s.fsm.image.OperationHighWater
			firstAt := head.ExecutionResult.Summary.FinalizedAt.Add(2 * time.Second)
			first := executionRetireCommand(head)
			head = validationApplyAllowed(t, executeStoreCommand(t, s, first, firstAt))
			if head.ExecutionRetirement.Checkpoint.Progress.Processed != 128 || head.ExecutionRetirement.complete(head) {
				t.Fatal("fixture did not leave a second retirement batch")
			}
			partialBytes := beforeBytes - head.ExecutionRetirement.Checkpoint.Progress.ChargedBytes
			assertExecutionRetirementExpiryState(t, s.fsm, head, partialBytes)
			// Freeze the next command and its time before expiry. Its admission
			// may be delayed, but the immutable original fence must not change.
			next, nextAt := executionRetireCommand(head), firstAt.Add(time.Second)
			retirementBeforeExpiry := head.ExecutionRetirement.Clone()
			expiryAt := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, expiryAt))
			if !head.ExecutionResult.HistoryExpiredAt.Equal(expiryAt) ||
				!collectionExecutionRetirementsEqual(head.ExecutionRetirement, &retirementBeforeExpiry) ||
				!collectionExecutionProgressEqual(*head.Execution, *original.Execution) ||
				!collectionExecutionSummariesEqual(&head.ExecutionResult.Summary, &original.ExecutionResult.Summary) {
				t.Fatal("committed history expiry changed original execution or retirement facts")
			}
			if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, firstAt); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("pre-expiry observation time resurrected expired history", err)
			}
			partial := head.Clone()
			for range 2 {
				validationApplyAllowed(t, executeStoreCommand(t, s, first, firstAt))
				assertExecutionRetirementExpiryState(t, s.fsm, partial, partialBytes)
			}
			partialSnapshot := captureSnapshotBytes(t, s.fsm)
			partialIndex := s.fsm.image.Index
			// Replay the same commands with a later local retention cutoff and
			// no retained item history. Deletion follows committed facts only.
			f := restoreExecutionRetirementAfterExpiry(t, partialSnapshot, partialIndex, expiryAt.AddDate(0, 0, 3))
			assertExecutionRetirementExpiryState(t, f, partial, partialBytes)
			executionRetirementExpiryLog(t, f, first, firstAt)
			assertExecutionRetirementExpiryState(t, f, partial, partialBytes)
			replayedFinal := executionRetirementExpiryLog(t, f, next, nextAt)
			finalBytes := beforeBytes - original.Execution.ChargedBytes
			if !replayedFinal.ExecutionRetirement.complete(replayedFinal) || !replayedFinal.ExecutionRetirement.UpdatedAt.Equal(nextAt) {
				t.Fatal("snapshot replay refreshed the original time or left rows behind")
			}
			assertExecutionRetirementExpiryState(t, f, replayedFinal, finalBytes)
			for _, retry := range []struct {
				command CollectionExecuteCommand
				at      time.Time
			}{{first, firstAt}, {next, nextAt}} {
				executionRetirementExpiryLog(t, f, retry.command, retry.at)
				assertExecutionRetirementExpiryState(t, f, replayedFinal, finalBytes)
			}
			if disk {
				configuration := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), configuration)
				if err != nil {
					t.Fatal("native replay after committed history expiry", err)
				}
				reopened := s
				t.Cleanup(func() { _ = reopened.Close() })
				assertExecutionRetirementExpiryState(t, s.fsm, partial, partialBytes)
				validationApplyAllowed(t, executeStoreCommand(t, s, first, firstAt))
				assertExecutionRetirementExpiryState(t, s.fsm, partial, partialBytes)
			}
			head = validationApplyAllowed(t, executeStoreCommand(t, s, next, nextAt))
			assertExecutionRetirementExpiryState(t, s.fsm, replayedFinal, finalBytes)
			if s.fsm.image.CatalogMutationSequence != catalogSequence || s.fsm.image.OperationHighWater != highWater {
				t.Fatal("expiry or retirement changed catalog/operation allocation")
			}
			for _, retry := range []struct {
				command CollectionExecuteCommand
				at      time.Time
			}{{first, firstAt}, {next, nextAt}} {
				validationApplyAllowed(t, executeStoreCommand(t, s, retry.command, retry.at))
				assertExecutionRetirementExpiryState(t, s.fsm, head, finalBytes)
			}
			finalSnapshot := captureSnapshotBytes(t, s.fsm)
			complete := restoreExecutionRetirementAfterExpiry(t, finalSnapshot, s.fsm.image.Index, expiryAt.AddDate(0, 0, 4))
			assertExecutionRetirementExpiryState(t, complete, head, finalBytes)
			executionRetirementExpiryLog(t, complete, next, nextAt)
			assertExecutionRetirementExpiryState(t, complete, head, finalBytes)
			if disk {
				configuration := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), configuration)
				if err != nil {
					t.Fatal("native replay after final retirement and stale retries", err)
				}
				reopened := s
				t.Cleanup(func() { _ = reopened.Close() })
				assertExecutionRetirementExpiryState(t, s.fsm, head, finalBytes)
			}
		})
	}
}
