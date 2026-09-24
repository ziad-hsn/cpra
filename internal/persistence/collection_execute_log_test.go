package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func executeStoreCommand(t *testing.T, s *Store, c CollectionExecuteCommand, at time.Time) Result {
	t.Helper()
	r, err := s.Submit(context.Background(), []Command{{Kind: "collection_execute", CollectionExecute: &c, At: at}})
	if err != nil || len(r) != 1 {
		t.Fatal("execution submission", err)
	}
	return r[0]
}

func executeStoreBegin(t *testing.T, s *Store) (CollectionState, CollectionExecuteCommand) {
	t.Helper()
	head, admission := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	c := CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: *admission.ActivationAuthority, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
	r := executeStoreCommand(t, s, c, head.Activation.At.Add(time.Second))
	if r.Err != nil || !r.Allowed || r.Collection.Execution == nil {
		t.Fatal("registered begin", r.Err)
	}
	return *r.Collection, c
}

func TestCollectionExecutionLogStrictFormatAndEnvelope(t *testing.T) {
	s := openCatalogMemory(t)
	head, c := executeStoreBegin(t, s)
	command := Command{Kind: "collection_execute", CollectionExecute: &c, At: head.Activation.At.Add(time.Second)}
	if commandWriteFormat(command) != CollectionExecutionFormatVersion {
		t.Fatal("wrong writer format")
	}
	for version := 1; version <= CollectionExecutionFormatVersion; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == CollectionExecutionFormatVersion) {
			t.Fatal("unexpected execution decoder decision", version, err)
		}
	}
	for _, commands := range [][]Command{{command, command}, {{Kind: "recover", At: command.At}, command}, {command, {Kind: "barrier", At: command.At}}} {
		raw, _ := json.Marshal(envelope{Version: CollectionExecutionFormatVersion, Commands: commands})
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatal("decoder accepted a non-isolated execution entry")
		}
		before := catalogDeltaJSON(t, s.fsm.image)
		if _, err := s.Submit(context.Background(), commands); !errors.Is(err, ErrCollectionInvalid) || !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image)) {
			t.Fatal("mixed submission entered durable admission", err)
		}
		f := catalogDeltaCloneMachine(t, s.fsm)
		if _, failed := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw}).(error); !failed || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
			t.Fatal("malformed committed mixed entry had partial effects")
		}
	}
	wrongUnion := command
	wrongUnion.MonitorID = "unexpected"
	if validateCommand(wrongUnion) == nil {
		t.Fatal("execution ignored a foreign command field")
	}
}

func TestCollectionExecutionLogBatcherPreservesIsolationAndOrdering(t *testing.T) {
	fixture := openCatalogMemory(t)
	head, c := executeStoreBegin(t, fixture)
	s, start := dormantCatalogStore(t)
	at := head.Activation.At.Add(time.Second)
	commands := make([]Command, 3)
	for _, n := range []int{0, 2} {
		r := catalogDeltaRecord("Credential", []string{"before", "", "after"}[n])
		r.CreatedAt, r.UpdatedAt = at, at
		commands[n] = Command{Kind: "catalog", At: at, Catalog: &CatalogMutation{Record: r, Create: true}}
	}
	commands[1] = Command{Kind: "collection_execute", CollectionExecute: &c, At: at}
	finished := make([]chan reply, len(commands))
	for n, command := range commands {
		finished[n] = make(chan reply, 1)
		go func(ch chan reply, command Command) {
			r, err := s.Submit(context.Background(), []Command{command})
			ch <- reply{results: r, err: err}
		}(finished[n], command)
		waitBudgetCondition(t, "queued ordinary/isolated submission", func() bool { return len(s.requests) == n+1 })
	}
	start()
	for n, ch := range finished {
		select {
		case r := <-ch:
			if r.err != nil || len(r.results) != 1 {
				t.Fatal("batch failed", n, r.err)
			}
			if n == 1 {
				if !errors.Is(r.results[0].Err, ErrOperationExpired) {
					t.Fatal("isolated entry did not run at its own position", r.results[0].Err)
				}
			} else if r.results[0].Err != nil || r.results[0].Catalog.CommittedIndex != uint64(n+1) {
				t.Fatal("ordinary entries merged across execution", n, r.results[0].Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("batcher stalled")
		}
	}
	if s.Status().CommittedIndex != 3 || budgetUsed(&s.budget) != 0 {
		t.Fatal("execution boundary lost a commit or byte reservation")
	}
}

func TestCollectionExecutionSnapshotRecoversCachesAndPendingCompletion(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executeStoreBegin(t, s)
	at := head.Activation.At.Add(2 * time.Second)
	prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at)
	if r := executeStoreCommand(t, s, prepared, at); r.Err != nil {
		t.Fatal(r.Err)
	}
	preparedBytes := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(preparedBytes, []byte(collectionExecutionSnapshotMagic)) {
		t.Fatal("execution snapshot has wrong format")
	}
	accepted := executeStoreCommand(t, s, executeDecision(prepared), at.Add(time.Second))
	if accepted.Err != nil || accepted.Operation == nil {
		t.Fatal("registered acceptance", accepted.Err)
	}
	acceptedBytes := captureSnapshotBytes(t, s.fsm)
	for name, blob := range map[string][]byte{"prepared": preparedBytes, "accepted": acceptedBytes} {
		t.Run(name, func(t *testing.T) {
			f := &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(blob))); err != nil {
				t.Fatal("restore complete execution snapshot", err)
			}
			defer f.collections.Close()
			i := f.image.Collections[head.ID]
			if !f.collectionOutcomeCommitments[head.ID].matches(i.Execution.Binding, i.Execution.Processed, i.Execution.OutcomeDigest) || f.collectionExecutionIndex != nil {
				t.Fatal("restore lost certifications or retained an old original-plan index")
			}
			if name == "prepared" {
				r, ok, err := f.collections.ExecutionRecord(head.ID, "prepared")
				if err != nil || !ok || !reflect.DeepEqual(r.Prepared, prepared.Prepared) || len(f.image.Catalog) != 0 || len(f.collectionChildren) != 0 {
					t.Fatal("prepared snapshot executed or changed candidate", err)
				}
				return
			}
			if len(f.collectionChildren) != 1 {
				t.Fatal("pending child link was not rebuilt")
			}
			u := OperationUpdate{ID: accepted.Operation.ID, Key: accepted.Operation.Key, UID: accepted.Operation.UID, Revision: accepted.Operation.NewVersion, Applied: true}
			raw, _ := json.Marshal(envelope{Version: CatalogFormatVersion, Commands: []Command{{Kind: "operation", Operation: &u, At: at.Add(2 * time.Second)}}})
			r, ok := f.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw}).([]Result)
			if !ok || len(r) != 1 || r[0].Err != nil || f.image.Collections[head.ID].Execution.ChildApplied != 1 || len(f.collectionChildren) != 0 {
				t.Fatal("ordinary completion after restore lost child evidence", r)
			}
			_, verified, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, f)), "")
			if err != nil {
				t.Fatal("completed child snapshot did not validate", err)
			}
			_ = verified.Close()
		})
	}
	for _, malformed := range [][]byte{acceptedBytes[:len(acceptedBytes)-1], append(bytes.Clone(acceptedBytes), 'x')} {
		if _, ledger, err := decodeSnapshot(bytes.NewReader(malformed), ""); err == nil {
			_ = ledger.Close()
			t.Fatal("incomplete/trailing execution ledger accepted")
		}
	}
}

func TestCollectionExecutionLogColdHotAndEvictedRetry(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executeStoreBegin(t, s)
	firstIndex := s.fsm.collectionExecutionIndex
	at := head.Activation.At.Add(2 * time.Second)
	candidate := executeCandidate(t, begin, firstIndex, 1, at)
	if r := executeStoreCommand(t, s, candidate, at); r.Err != nil || s.fsm.collectionExecutionIndex != firstIndex {
		t.Fatal("hot original-plan path failed", r.Err)
	}
	s.fsm.mu.Lock()
	s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger = nil, nil
	s.fsm.mu.Unlock()
	r := executeStoreCommand(t, s, executeDecision(candidate), at.Add(time.Second))
	if r.Err != nil || r.Operation == nil || s.fsm.collectionExecutionIndex == nil || s.fsm.collectionExecutionIndex == firstIndex {
		t.Fatal("evicted index could not replay original plan", r.Err)
	}
	before := r.Collection.Clone()
	high, token := s.fsm.image.OperationHighWater, s.fsm.image.CatalogMutationSequence
	s.fsm.mu.Lock()
	s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger = nil, nil
	s.fsm.mu.Unlock()
	retry := executeStoreCommand(t, s, executeDecision(candidate), at.Add(2*time.Second))
	if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(*retry.Collection, before) || s.fsm.image.OperationHighWater != high || s.fsm.image.CatalogMutationSequence != token {
		t.Fatal("cold exact retry minted new work", retry.Err)
	}
}
