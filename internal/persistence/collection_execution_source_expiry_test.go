package persistence

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func sourceRetirementReplay(t *testing.T, f *machine, command CollectionExecuteCommand, at time.Time) Result {
	t.Helper()
	raw, err := json.Marshal(envelope{Version: CollectionExecutionSourceRetirementFormatVersion,
		Commands: []Command{{Kind: "collection_execute", CollectionExecute: &command, At: at}}})
	if err != nil {
		t.Fatal(err)
	}
	value := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw})
	results, ok := value.([]Result)
	if !ok || len(results) != 1 {
		t.Fatalf("source retirement replay failed: %v", value)
	}
	return results[0]
}

func TestCollectionExecutionSourceRetirementHistoryExpiryBetweenBatches(t *testing.T) {
	s, head := sourceRetirementFixture(t, false, 257)
	first, firstAt := sourceRetirementCommand(head), head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	head = validationApplyAllowed(t, executeStoreCommand(t, s, first, firstAt))
	if head.Validation.RemovedRows == 0 || head.Validation.RemovedRows == head.Validation.Uploaded {
		t.Fatal("fixture did not leave a partial validation prefix")
	}
	next, nextAt := sourceRetirementCommand(head), firstAt.Add(time.Second)
	expiryAt := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)
	head = validationApplyAllowed(t, executionPublishStep(t, s, head, expiryAt))
	if !head.ExecutionResult.HistoryExpiredAt.Equal(expiryAt) {
		t.Fatal("result expiry was not committed")
	}
	if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, firstAt); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("earlier read resurrected expired result", err)
	}
	used, err := s.fsm.collections.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	f := restoreExecutionRetirementAfterExpiry(t, captureSnapshotBytes(t, s.fsm), s.fsm.image.Index, expiryAt.AddDate(0, 0, 3))
	for range 2 {
		got := validationApplyAllowed(t, sourceRetirementReplay(t, f, first, firstAt))
		if !reflect.DeepEqual(got, head) {
			t.Fatal("stale source retry changed original facts after expiry")
		}
		if size, err := f.collections.Bytes(); err != nil || size != used {
			t.Fatal("stale retry refunded quota after expiry", size, err)
		}
	}
	actual := validationApplyAllowed(t, sourceRetirementReplay(t, f, next, nextAt))
	expected := validationApplyAllowed(t, executeStoreCommand(t, s, next, nextAt))
	if !reflect.DeepEqual(actual, expected) || !actual.ExecutionRetirement.Sources.UpdatedAt.Equal(nextAt) {
		t.Fatal("source replay consulted local history or refreshed the command time")
	}
	// Subsequent commands use explicit post-expiry observations. They may finish
	// cleanup with no retained item history, but cannot resurrect either result.
	head = actual
	for step := 0; step < 10; step++ {
		if _, found := f.image.Collections[head.ID]; !found {
			break
		}
		command, at := sourceRetirementCommand(head), expiryAt.Add(time.Duration(step+1)*time.Second)
		head = validationApplyAllowed(t, sourceRetirementReplay(t, f, command, at))
		live := validationApplyAllowed(t, executeStoreCommand(t, s, command, at))
		if !reflect.DeepEqual(head, live) {
			t.Fatal("live and expired-history replay source cleanup diverged")
		}
	}
	if _, found := f.image.Collections[head.ID]; found {
		t.Fatal("expired source header survived complete retirement")
	}
	if size, err := f.collections.Bytes(); err != nil || size != 0 {
		t.Fatal("expired cleanup did not refund exact remaining quota", size, err)
	}
	if result := sourceRetirementReplay(t, f, first, firstAt); result.Err != nil || !result.Allowed || result.Collection != nil {
		t.Fatal("original source retry recreated removed header", result.Err)
	}
	if !f.history.retentionCutoffReached(head.ExecutionResult.Summary.FinalizedAt) {
		t.Fatal("source replay lost durable history expiry")
	}
}
