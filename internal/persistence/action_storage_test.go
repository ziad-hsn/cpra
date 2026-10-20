package persistence

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestActionLedgerSnapshotRejectsInvalidArchivedEvidence(t *testing.T) {
	for _, damage := range []string{"retry", "executor", "review", "diagnostic", "late", "kind"} {
		t.Run(damage, func(t *testing.T) {
			store := openCatalogMemory(t)
			configure(t, store)
			i := store.fsm.image
			i.Index = 20
			m := i.Monitors["stable-one"]
			at := time.Now().UTC()
			a := Action{ID: "archived", Revision: m.Revision, FileIncarnation: m.FileIncarnation,
				Kind: "intervention", Attempt: 1, State: Failed, Outcome: "rejected", NotBefore: at,
				StartedAt: at, FinishedAt: at.Add(time.Second)}
			valid := a
			switch damage {
			case "retry":
				a.State, a.Retryable = Succeeded, true
			case "executor":
				a.ExecutorSession = "unbound-executor"
			case "review":
				a.Review = &ActionReview{Resolution: "accepted"}
			case "diagnostic":
				a.Failure.Code = "arbitrary-provider-text"
			case "late":
				a.LateEvidence = &LateActionEvidence{Outcome: "accepted"}
			case "kind":
				a.Kind = "invalid"
			}
			ledger := newLedgerTest(t, false, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 10}, []actionLedgerRow{{MonitorID: m.ID, Action: a}}); err != nil {
				t.Fatal(err)
			}
			valid.ID = "later-valid"
			position := actionLedgerPosition{Index: 11}
			sequence, err := ledger.CommitActions(position, []actionLedgerRow{{MonitorID: m.ID, Action: valid}})
			if err != nil {
				t.Fatal(err)
			}
			i.ActionStorage = &actionStorageState{Sequence: sequence, Position: position}
			view, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			// Generate complete canonical framing and checksums without the export
			// semantic gate, to exercise import rather than an earlier checksum error.
			var encoded bytes.Buffer
			encoded.WriteString(actionSnapshotMagic)
			if err := json.NewEncoder(&encoded).Encode(i); err != nil {
				t.Fatal(err)
			}
			if err := writeCollectionSnapshotRows(&encoded, i.Version, view); err != nil {
				t.Fatal(err)
			}
			_, restored, err := decodeSnapshot(bytes.NewReader(encoded.Bytes()), "")
			if restored != nil {
				_ = restored.Close()
			}
			if err == nil {
				t.Fatal("invalid archived evidence passed snapshot import")
			}
		})
	}
}

func TestActionLedgerRetainsCompletedRetryAttempt(t *testing.T) {
	config := testConfig(t)
	store, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configure(t, store)
	at := time.Now().UTC()
	submit(t, store, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, Outcome: "failure", At: at})
	m, _ := store.Get("stable-one")
	id := sortedActions(m.Actions)[0]
	first := m.Actions[id]
	execution, err := store.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at})
	if err != nil {
		t.Fatal("start denied", err)
	}
	finished := at.Add(time.Second)
	failure := NewFailureDiagnostic("rejected")
	submit(t, store, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: finished, Outcome: "failure", Retryable: true, Failure: failure})
	if err := execution.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = store.Get(m.ID)
	if _, exists := m.Actions[id]; exists {
		t.Fatal("replaced attempt still in execution projection")
	}
	var retry Action
	for _, a := range m.Actions {
		if a.DeliveryID == first.DeliveryID {
			retry = a
		}
	}
	if retry.Attempt != 2 || retry.State != Queued || retry.Deadline != first.Deadline {
		t.Fatalf("retry changed logical delivery or budget: %+v", retry)
	}
	assertRecords := func() {
		t.Helper()
		view, err := store.fsm.collections.pinActions()
		if err != nil {
			t.Fatal(err)
		}
		defer view.Close()
		old, found, err := view.Get(t.Context(), id)
		if err != nil || !found || old.Action.State != Failed || old.Action.Failure != failure || !old.Action.Retryable || !old.Action.FinishedAt.Equal(finished) || old.Action.ExecutorFinishedAt.IsZero() {
			t.Fatalf("final attempt evidence lost: %+v %v", old, err)
		}
		observed, found, err := store.ActionContext(t.Context(), id)
		if err != nil || !found || observed.LocalExecutorActive || !observed.ExecutorFenced {
			t.Fatalf("retired attempt lost executor completion: %+v %v", observed, err)
		}
		next, found, err := view.Get(t.Context(), retry.ID)
		if err != nil || !found || next.Action.DeliveryID != first.DeliveryID || next.Action.State != Queued {
			t.Fatalf("retry missing: %+v %v", next, err)
		}
	}
	assertRecords()
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	assertRecords()
}
