package management

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func executionProjectionPublish(t *testing.T, f collectionSourceFixture, head persistence.CollectionState, at time.Time) persistence.CollectionState {
	t.Helper()
	for range 64 {
		if head.ExecutionResult.HistorySealed {
			return head
		}
		result, err := f.store.Submit(t.Context(), []persistence.Command{{Kind: "collection_execute", At: at,
			CollectionExecute: &persistence.CollectionExecuteCommand{Action: "publish", Binding: head.ExecutionResult.Summary.Binding,
				Publication: &persistence.CollectionExecutionPublication{Published: head.ExecutionResult.Published}}}})
		if err != nil || len(result) != 1 || result[0].Err != nil {
			t.Fatal("publish original execution result", result, err)
		}
		head, _, err = f.store.CollectionGet(head.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("publication did not seal")
	return head
}

func executionProjectionListed(t *testing.T, f collectionSourceFixture, head persistence.CollectionState, at time.Time) api.Operation {
	t.Helper()
	v, err := f.catalog.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rows, next, err := v.Page(t.Context(), "", "", 100)
	if err != nil || next != "" {
		t.Fatal("list collection execution metadata", next, err)
	}
	var found []api.Operation
	for _, row := range rows {
		if row.ID == head.ID {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatal("collection missing or duplicated by terminal history", len(found))
	}
	return found[0]
}

func TestCollectionExecutionProjectionAcceptedAndAppliedRemainIndependent(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "projection-failed", true: "applied"}[applied], func(t *testing.T) {
			f, head, worker := executionFinalizationFixture(t, applied)
			at := worker.now()
			check := func(state, availability string, summary bool) api.Operation {
				t.Helper()
				got, err := f.catalog.OperationAs(t.Context(), head.ID, head.Actor, at, true)
				wantApplied := int64(0)
				if applied {
					wantApplied = 1
				}
				if err != nil || got.State != state || got.ExecutionResult == nil || got.ExecutionResult.State != availability ||
					got.Committed == nil || *got.Committed != 1 || got.Applied == nil || *got.Applied != wantApplied ||
					got.ExecutionResult.Counts == nil || got.ExecutionResult.Counts.Accepted != 1 || got.ExecutionResult.Counts.ChildApplied != wantApplied ||
					(got.ExecutionResult.Summary != nil) != summary || got.ID != head.ID || got.ContentDigest != head.ContentDigest ||
					!reflect.DeepEqual(executionProjectionListed(t, f, head, at), got) {
					t.Fatal("authoritative projection or list/detail parity", got, err)
				}
				return got
			}
			check("applying", "pending", false)
			if handled, err := worker.finalizeNext(t.Context()); err != nil || !handled {
				t.Fatal("finalize settled original execution", err)
			}
			head, _, _ = f.store.CollectionGet(head.ID)
			state := map[bool]string{false: "partial", true: "completed"}[applied]
			check(state, "pending", false)
			head = executionProjectionPublish(t, f, head, at)
			got := check(state, "ready", true)
			view, status, err := f.catalog.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil || status.State != "ready" || view == nil {
				t.Fatal(status, err)
			}
			page, err := view.Page(t.Context(), 0, 100, at)
			if err != nil || len(page.Items) != 1 {
				t.Fatal(err)
			}
			got.Items = []api.ApplyResult{CollectionExecutionItemResult(page.Items[0])}
			availability := CollectionExecutionReceiptResult(page.Receipt)
			if !reflect.DeepEqual(availability, *got.ExecutionResult) || api.ValidateExecutionResult(got) != nil ||
				!*got.Items[0].Committed || got.Items[0].Applied == nil || *got.Items[0].Applied != applied ||
				got.Items[0].CatalogDecision != "accepted" || got.Items[0].Outcome != "accepted" || got.Items[0].ChildDisposition == nil {
				t.Fatal("wire projection conflated catalog decision and child disposition", got)
			}
			got.ExecutionResult.Summary.Accepted = 0
			got.Items[0].ChildDisposition.State = "changed"
			check(state, "ready", true)
			at = head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
			check(state, "expired", true)
			if _, err := f.catalog.OperationAs(t.Context(), head.ID, "foreign", at, true); !errors.Is(err, persistence.ErrOperationNotFound) {
				t.Fatal("result metadata crossed original ownership", err)
			}
		})
	}
}

func TestCollectionExecutionProjectionCanceledPendingAndLateFinalization(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("late", "http://original.example/health"), nil)
	w := executionTestWorker(t, f, head)
	for range 3 {
		executionAdvance(t, w, head.ID)
	}
	head, _, _ = f.store.CollectionGet(head.ID)
	canceledAt := head.Execution.LastAt.Add(time.Second)
	got, err := f.catalog.CancelCollection(t.Context(), head.ID, head.Actor, collectionClock(canceledAt), allowCollectionCommit)
	if err != nil || got.State != "canceled" || *got.Committed != 1 || *got.Applied != 0 || got.ExecutionResult.Counts.ChildPending != 1 {
		t.Fatal("cancellation erased committed or pending child facts", got, err)
	}
	at := canceledAt.AddDate(0, 0, 35)
	got, err = f.catalog.OperationAs(t.Context(), head.ID, head.Actor, at, true)
	if err != nil || got.State != "canceled" || got.ExecutionResult.State != "pending" || got.ExecutionResult.Counts.ChildPending != 1 ||
		!reflect.DeepEqual(executionProjectionListed(t, f, head, at), got) {
		t.Fatal("old cancellation deadline hid unresolved accepted child", got, err)
	}
	children, err := f.store.PendingOperationsContext(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatal(children, err)
	}
	child := children[0]
	results, err := f.store.Submit(t.Context(), []persistence.Command{{Kind: "operation", At: at,
		Operation: &persistence.OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion, Applied: false}}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal(results, err)
	}
	w.now = collectionClock(at.Add(time.Second))
	if handled, err := w.finalizeNext(t.Context()); err != nil || !handled {
		t.Fatal(err)
	}
	head, _, _ = f.store.CollectionGet(head.ID)
	head = executionProjectionPublish(t, f, head, w.now())
	got, err = f.catalog.OperationAs(t.Context(), head.ID, head.Actor, w.now(), true)
	if err != nil || got.State != "canceled" || got.ExecutionResult.State != "ready" || got.ExecutionResult.Summary.Outcome != "canceled" ||
		got.ExecutionResult.Counts.Accepted != 1 || got.ExecutionResult.Counts.ChildFailed != 1 || !head.TerminalAt.Equal(canceledAt) ||
		!reflect.DeepEqual(executionProjectionListed(t, f, head, w.now()), got) {
		t.Fatal("late execution result replaced original canceled disposition", got, err)
	}
	before := f.store.Status().CommittedIndex
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.catalog.CollectionExecutionResultView(canceled, head.ID, head.Actor, w.now()); !errors.Is(err, context.Canceled) || f.store.Status().CommittedIndex != before {
		t.Fatal("canceled result read mutated state or lost cancellation", err)
	}
}

func TestCollectionExecutionProjectionItemDecisionsAndPresence(t *testing.T) {
	at := time.Now().UTC()
	for _, decision := range []string{"accepted", "unchanged", "conflict", "dependencyBlocked", "unattempted"} {
		item := persistence.CollectionExecutionItem{Key: persistence.CatalogKey{Kind: "Monitor", ID: "one"}, InputOrdinal: 2, PlanOrdinal: 1,
			Source: "source.00000000000000000001", SourceDocument: 3, SourceItem: 4, OriginalUID: "original", OldVersion: "old", Decision: decision}
		if decision != "unattempted" {
			item.CommittedIndex, item.DecidedAt = 7, at
		}
		if decision == "accepted" || decision == "unchanged" {
			item.UID, item.NewVersion, item.Generation = "original", "new", 2
		}
		if decision == "accepted" {
			item.Child = &persistence.CollectionExecutionItemChild{ID: "child", State: "partial", Outcome: "superseded", UpdatedAt: at, InvalidatedByRestore: "restore"}
		}
		got := CollectionExecutionItemResult(item)
		if got.ID != "Monitor/one" || *got.InputOrdinal != 2 || *got.PlanOrdinal != 1 || got.Source != item.Source || *got.SourceDocument != 3 || *got.SourceItem != 4 ||
			got.CatalogDecision != decision || got.Outcome != decision || *got.Committed != (decision == "accepted") || (got.Applied != nil) != (decision == "accepted") ||
			(got.CommittedIndex == nil) != (decision == "unattempted") || (got.DecidedAt == nil) != (decision == "unattempted") {
			t.Fatal("item identity/availability changed", got)
		}
		raw, err := json.Marshal(got)
		if err != nil || len(raw) == 0 || decision == "accepted" && (got.ChildDisposition.InvalidatedByRestore != "restore" || *got.Applied) {
			t.Fatal(got, err)
		}
	}
}
