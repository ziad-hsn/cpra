package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

func TestCollectionExecutionResultNativeReplaySnapshotAndOfflineInspection(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "logs"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
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
			defer func() {
				if s != nil {
					s.Close()
				}
			}()
			head, begin := executionResultInput(t, s, "create")
			head, child := executionResultDecide(t, s, head, begin, "accepted")
			head = executionResultSettle(t, s, head, child, false)
			c := executionResultFinalizeCommand(t, head)
			at := head.Execution.LastAt.Add(time.Second)
			result := executeStoreCommand(t, s, c, at)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			original := result.Collection.ExecutionResult.Summary.Clone()
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = nil
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("stopped format10 certification", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal("reopen", err)
			}
			retry := executeStoreCommand(t, s, c, at.Add(time.Hour))
			if retry.Err != nil || len(retry.Events) != 0 || !collectionExecutionSummariesEqual(&original, &retry.Collection.ExecutionResult.Summary) {
				t.Fatal("recovery changed original summary", retry.Err)
			}
			page, err := s.History().Page("collection-execution/"+head.ID, "", 10)
			if err != nil || len(page.Events) != 1 || !collectionExecutionSummariesEqual(&original, page.Events[0].CollectionExecution) {
				t.Fatal("recovered anchor", page, err)
			}
		})
	}
}

func TestCollectionExecutionResultOriginalRestoreFence(t *testing.T) {
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
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	head, begin := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, begin, "accepted")
	if child == nil {
		t.Fatal("expected pending child")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	at := head.Execution.LastAt.Add(time.Second)
	if err := MarkRestored(config.Storage.Directory, at); err != nil {
		t.Fatal(err)
	}
	admin, err = OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	state, err := admin.Authentication()
	if err != nil || !state.ResetRequired {
		t.Fatal("restore authority fence", err)
	}
	admin.fsm.mu.RLock()
	stopped := admin.fsm.image.Collections[head.ID].Clone()
	admin.fsm.mu.RUnlock()
	if stopped.Execution.ChildInvalidated != 1 || stopped.Execution.ChildTerminals != 1 || stopped.InvalidatedByRestore == "" {
		t.Fatal("restore lost original child disposition")
	}
	// Reset-required storage must not acknowledge result finalization.
	c := executionResultFinalizeCommand(t, stopped)
	resetMachine := catalogDeltaCloneMachine(t, admin.fsm)
	resetMachine.history, err = openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer resetMachine.history.Close()
	if r := executionResultLog(t, resetMachine, c, at.Add(time.Second)); !errors.Is(r.Err, ErrAuthenticationResetRequired) {
		t.Fatal("reset fence bypassed", r.Err)
	}
	provision := authenticationBootstrap()
	provision.Mode = "provision"
	provision.Epoch = state.Epoch
	provision.ExpectedEpoch = state.Epoch
	provision.ExpectedRevision = state.Revision
	provision.At = at.Add(time.Second)
	provision.Principals[0].ExpiresAt = provision.At.Add(time.Hour)
	if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	result := executeStoreCommand(t, s, c, at.Add(2*time.Second))
	if result.Err != nil || result.Collection.Phase != "invalidated" || len(result.Events) != 1 || result.Collection.InvalidatedByRestore != stopped.InvalidatedByRestore ||
		!result.Collection.TerminalAt.Equal(stopped.TerminalAt) || result.Collection.ExecutionResult.Summary.Fence.Progress.ChildInvalidated != 1 {
		t.Fatal("restore finalization changed original fence", result.Err)
	}
	if _, l, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), ""); err != nil {
		t.Fatal(err)
	} else {
		l.Close()
	}
}

func TestCollectionExecutionResultPreBeginRetentionAndHistoricalCleanup(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	original := catalogDeltaJSON(t, s.fsm.image)
	if err := s.maintainCollections(head.TerminalAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, catalogDeltaJSON(t, s.fsm.image)) {
		t.Fatal("maintenance deleted activated input before zero-decision finalization")
	}
	progress := collectionCleanupFor(head)
	c := Command{Kind: "collection", At: head.TerminalAt.Add(time.Second), Collection: &CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &progress}}
	if commandWriteFormat(c) != CollectionExecutionResultFormatVersion || commandMinimumFormat(c) != CollectionActivationFormatVersion {
		t.Fatal("new cleanup did not preserve historical replay gate")
	}
	r, err := s.Submit(context.Background(), []Command{c})
	if err != nil || len(r) != 1 || !errors.Is(r[0].Err, ErrCollectionConflict) {
		t.Fatal("new cleanup removed activated input", r, err)
	}
	// An existing format8 entry must keep its old cleanup semantics, even though
	// newly submitted commands cannot take that path.
	raw, _ := json.Marshal(envelope{Version: CollectionActivationFormatVersion, Commands: []Command{c}})
	result := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
	rows, ok := result.([]Result)
	if !ok || len(rows) != 1 || rows[0].Err != nil || !rows[0].Allowed {
		t.Fatal("historical cleanup replay changed", result)
	}
}

func TestCollectionExecutionResultHistoryAnchorStrictIndexAndClone(t *testing.T) {
	source := openCatalogMemory(t)
	head, begin := executionResultInput(t, source, "create")
	head, child := executionResultDecide(t, source, head, begin, "accepted")
	head = executionResultSettle(t, source, head, child, true)
	result := executeStoreCommand(t, source, executionResultFinalizeCommand(t, head), head.Execution.LastAt.Add(time.Second))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	page, err := source.History().Page("collection-execution/"+head.ID, "", 10)
	if err != nil || len(page.Events) != 1 {
		t.Fatal(page, err)
	}
	anchor := page.Events[0].Clone()
	raw, _ := json.Marshal(anchor)
	if _, err := decodeCollectionExecutionResultEvent(raw); err != nil {
		t.Fatal(err)
	}
	malformed := append([]byte(`{"unexpected":"private",`), raw[1:]...)
	if _, err := decodeCollectionExecutionResultEvent(malformed); err == nil {
		t.Fatal("unallowlisted anchor field accepted")
	}
	clone := anchor.Clone()
	clone.CollectionExecution.Fence.Progress.ChildApplied = 0
	if anchor.CollectionExecution.Fence.Progress.ChildApplied != 1 {
		t.Fatal("history clone aliases immutable progress")
	}
	for _, disk := range []bool{false, true} {
		dir := ""
		if disk {
			dir = t.TempDir()
		}
		h, err := openHistory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.append(1, []Event{anchor}, anchor.At); err != nil {
			t.Fatal(err)
		}
		changed := anchor.Clone()
		changed.ID = "00000000000000000002:00000000"
		if err := h.append(2, []Event{changed}, anchor.At); !errors.Is(err, ErrHistoryUnavailable) {
			t.Fatal("anchor identity overwritten", err)
		}
		h.Close()
		if !disk {
			continue
		}
		reopened, err := openHistory(dir)
		if err != nil {
			t.Fatal("valid anchor failed reopening", err)
		}
		reopened.Close()
		path := filepath.Join(dir, anchor.At.UTC().Format("2006-01-02")+".db")
		db, err := bolt.Open(path, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(collectionExecutionAnchorBucket) }); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if h, err := openHistory(dir); err == nil {
			h.Close()
			t.Fatal("missing anchor index accepted")
		}
	}
}

func executionResultHistoricalCleanup(t *testing.T, s *Store, head CollectionState, at time.Time) {
	t.Helper()
	for n := 0; n < 20; n++ {
		cleanup := cleanupCommand(head)
		c := Command{Kind: "collection", At: at, Collection: &cleanup}
		raw, _ := json.Marshal(envelope{Version: CollectionActivationFormatVersion, Commands: []Command{c}})
		result := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
		rows, ok := result.([]Result)
		if !ok || len(rows) != 1 || rows[0].Err != nil || !rows[0].Allowed {
			t.Fatal("historical cleanup", result)
		}
		next, exists := s.fsm.image.Collections[head.ID]
		if !exists {
			return
		}
		head = next
	}
	t.Fatal("historical cleanup did not finish")
}
