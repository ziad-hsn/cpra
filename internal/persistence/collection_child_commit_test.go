package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

// Construct a real admitted parent and then install explicitly supplied
// acceptance fixtures. This qualifies child retirement, not item execution or
// format-9 snapshot recovery; no public activation operation is introduced.
func childCommitFixture(t *testing.T, disk bool) (*machine, CollectionState, []CollectionItemOutcome) {
	t.Helper()
	s := openCatalogMemory(t)
	head, authority := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	f := catalogDeltaCloneMachine(t, s.fsm)
	f.history = s.fsm.history
	f.collections = newLedgerTest(t, disk, 8<<20)
	f.collectionChildren = make(collectionChildLinks)
	f.image.Operations = make(map[string]OperationReceipt)
	f.image.Catalog = make(map[string]CatalogRecord)
	f.image.OperationHighWater = 100
	f.image.Index += 10
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := NewCollectionExecutionProgress(binding, head.ItemCount, at)
	if err != nil {
		t.Fatal(err)
	}
	certified, err := newCollectionExecutionOutcomeCommitments(binding)
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []CollectionItemOutcome
	for ordinal := uint64(1); ordinal <= 2; ordinal++ {
		p, o := executionRecordFixture(t)
		p.Binding, p.Ordinal, p.InputOrdinal = binding, ordinal, ordinal
		p.ID = uuid.NewString()
		p.Record = catalogDeltaRecord("Monitor", fmt.Sprintf("child-%d", ordinal))
		p.At = at.Add(time.Second)
		p.Record.CreatedAt, p.Record.UpdatedAt = p.At, p.At
		o.Binding, o.Ordinal, o.InputOrdinal, o.PreparedID, o.RowDigest = binding, ordinal, ordinal, p.ID, p.RowDigest
		o.Key, o.UID, o.Revision, o.Generation = p.Record.Key, p.Record.UID, p.Record.Revision, p.Record.Generation
		o.At, o.CommittedIndex, o.MutationSequence = p.At, f.image.Index, ordinal
		o.Receipt = &OperationReceipt{ID: operationHandle(f.image.OperationEpoch, 50+ordinal), Key: o.Key, UID: o.UID,
			NewVersion: o.Revision, Generation: o.Generation, CommittedIndex: o.CommittedIndex, Actor: head.Actor,
			At: o.At, UpdatedAt: o.At, State: "committed", Outcome: "committed"}
		progress, err = progress.withPrepared(p)
		if err != nil {
			t.Fatal(err)
		}
		progress, err = progress.withOutcome(o)
		if err != nil {
			t.Fatal(err)
		}
		certified, err = certified.append(o)
		if err != nil {
			t.Fatal(err)
		}
		executionLedgerApply(t, f.collections, collectionExecutionRecord{Version: 1, Prepared: &p})
		executionLedgerApply(t, f.collections, collectionExecutionRecord{Version: 1, Outcome: &o})
		f.image.Operations[o.Receipt.ID] = *o.Receipt
		record := p.Record.Clone()
		record.CommittedIndex = o.CommittedIndex
		f.image.Catalog[record.Key.indexKey()] = record
		link, err := collectionChildLinkFor(o, *o.Receipt)
		if err != nil {
			t.Fatal(err)
		}
		f.collectionChildren[link.ChildID] = link
		outcomes = append(outcomes, o)
	}
	head.Execution = &progress
	f.collectionOutcomeCommitments = map[string]*collectionExecutionOutcomeCommitments{head.ID: certified}
	if head.validate() != nil {
		t.Fatal("invalid child lifecycle parent fixture")
	}
	f.image.Collections[head.ID] = head
	f.image.CatalogMutationSequence = 2
	f.rebuildCatalogIndexes()
	f.rebuildOperationIndex()
	return f, head, outcomes
}

func childCommitUpdate(o CollectionItemOutcome, applied bool) OperationUpdate {
	return OperationUpdate{ID: o.Receipt.ID, Key: o.Key, UID: o.UID, Revision: o.Revision, Applied: applied}
}

func childCommitTerminal(t *testing.T, f *machine, o CollectionItemOutcome) CollectionChildObservation {
	t.Helper()
	r, found, err := f.collections.ExecutionRecord(o.Binding.OperationID, fmt.Sprintf("terminal/%016x", o.Ordinal))
	if err != nil || !found || r.Terminal == nil {
		t.Fatal("missing committed child terminal", err)
	}
	return *r.Terminal
}

func childCommitDenyWrites(t *testing.T, l *collectionLedger) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("read-only descriptor fixture requires Unix")
	}
	path := l.db.Path()
	if err := l.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20,
		OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
	if err != nil {
		t.Fatal(err)
	}
	l.db = db
}

func TestCollectionChildCommitCompletionAndCancellation(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, disk)
			at := outcomes[0].At.Add(time.Second)
			cancel := head.Clone()
			cancel.Phase, cancel.TerminalAt = "canceled", at
			cancel.Cancellation = &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}
			if cancel.validate() != nil {
				t.Fatal("invalid cancellation")
			}
			f.image.Collections[head.ID] = cancel
			beforeBytes, _ := f.collections.Bytes()
			for n, o := range outcomes {
				at = at.Add(time.Second)
				result := f.applyOperation(childCommitUpdate(o, n == 0), at)
				if result.Err != nil || !result.Allowed || len(result.Events) != 1 {
					t.Fatal("completion", result.Err)
				}
				terminal := childCommitTerminal(t, f, o)
				if !terminal.UpdatedAt.Equal(at) || n == 0 && terminal.Outcome != "applied" || n == 1 && terminal.Outcome != "projection_failed" {
					t.Fatal("wrong retained controller result")
				}
				if _, ok := f.image.Operations[o.Receipt.ID]; ok || len(f.collectionChildren) != 1-n {
					t.Fatal("completed child remained pending")
				}
			}
			p := f.image.Collections[head.ID].Execution
			if p.ChildTerminals != 2 || p.ChildApplied != 1 || p.ChildFailed != 1 || !p.LastAt.Equal(at) ||
				f.image.Collections[head.ID].Phase != "canceled" || !f.image.Collections[head.ID].TerminalAt.Equal(cancel.TerminalAt) {
				t.Fatal("completion changed cancellation or lost progress")
			}
			if after, _ := f.collections.Bytes(); after != beforeBytes {
				t.Fatal("terminal consumed unreserved quota")
			}
			before := catalogDeltaJSON(t, f.image)
			if r := f.applyOperation(childCommitUpdate(outcomes[0], true), at); !errors.Is(r.Err, ErrOperationNotFound) || len(r.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("duplicate completion recreated work")
			}
			cleanup := collectionCleanupFor(f.image.Collections[head.ID])
			if r := f.cleanupCollection(CollectionCommand{OperationID: head.ID, UploadID: head.UploadID, Cleanup: &cleanup}, at); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("cleanup erased unpublished execution evidence", r.Err)
			}
		})
	}
}

func TestCollectionChildCommitSupersessionRetainsReservationOnWriteFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, true)
			o := outcomes[0]
			m := catalogDeltaUpdate(f, o.Key)
			m.Actor, m.Record.UpdatedAt = head.Actor, o.At.Add(time.Second)
			c := Command{Kind: "catalog", At: m.Record.UpdatedAt, Catalog: &m}
			r, _, err := operationDigest(c)
			if err != nil {
				t.Fatal(err)
			}
			r.ID = operationHandle(f.image.OperationEpoch, f.image.OperationHighWater)
			m.OperationID = r.ID
			f.image.OperationReservations = map[string]OperationReservation{r.ID: r}
			before := catalogDeltaJSON(t, f.image)
			if fail {
				childCommitDenyWrites(t, f.collections)
			}
			result := f.applyOperationTarget(c, f.image.Index+1, CollectionActivationFormatVersion)
			if fail {
				var path *os.PathError
				if result.Allowed || !errors.As(result.Err, &path) || f.err == nil || len(result.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
					t.Fatal("failed completion changed catalog, reservation or audit", result.Err)
				}
				return
			}
			if result.Err != nil || !result.Allowed || len(result.Events) != 2 || childCommitTerminal(t, f, o).Outcome != "superseded" {
				t.Fatal("replacement lost child outcome", result.Err)
			}
			if _, ok := f.image.OperationReservations[r.ID]; ok || f.image.Catalog[o.Key.indexKey()].Revision != m.Record.Revision || f.image.Collections[head.ID].Execution.ChildSuperseded != 1 {
				t.Fatal("replacement was not installed after evidence")
			}
		})
	}
}

func TestCollectionChildCommitRestorePageAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, true)
			marker := RestoreMarker{Version: 1, NodeID: uuid.NewString(), ID: uuid.NewString(), AuthenticationEpoch: uuid.NewString(), AuthenticationRevision: uuid.NewString(), OperationEpoch: uuid.NewString(), At: outcomes[0].At.Add(time.Second)}
			f.image.Restore = &RestoreState{Marker: marker, Phase: "receipts"}
			before := catalogDeltaJSON(t, f.image)
			if fail {
				childCommitDenyWrites(t, f.collections)
			}
			result := f.applyRestore(RestoreCommand{Marker: marker, Phase: "receipts"})
			if fail {
				var path *os.PathError
				if result.Allowed || !errors.As(result.Err, &path) || f.err == nil || len(result.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || len(f.collectionChildren) != 2 {
					t.Fatal("failed restore page published invalidation/cursor/deletion", result.Err)
				}
				return
			}
			if !result.Allowed || result.Err != nil || len(result.Events) != 3 || f.image.Restore.Phase != "complete" || len(f.image.Operations) != 0 || len(f.collectionChildren) != 0 {
				t.Fatal("restore page did not retire committed children", result.Err)
			}
			state := f.image.Collections[head.ID]
			if state.Phase != "invalidated" || state.Execution.ChildInvalidated != 2 || state.InvalidatedByRestore != marker.ID {
				t.Fatal("restore overwrote terminal progress")
			}
			for _, o := range outcomes {
				if childCommitTerminal(t, f, o).InvalidatedByRestore != marker.ID {
					t.Fatal("restore marker lost")
				}
			}
		})
	}
}

func TestCollectionChildCommitFailureStopsRemainingEnvelope(t *testing.T) {
	for _, kind := range []string{"operation", "catalog", "restore_reset"} {
		t.Run(kind, func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, true)
			o := outcomes[0]
			at := o.At.Add(time.Second)
			first := Command{Kind: kind, At: at}
			switch kind {
			case "operation":
				update := childCommitUpdate(o, true)
				first.Operation = &update
			case "catalog":
				m := catalogDeltaUpdate(f, o.Key)
				m.Actor, m.OperationID, m.Record.UpdatedAt = head.Actor, m.Record.Revision, at
				first.Catalog = &m
			case "restore_reset":
				marker := RestoreMarker{Version: 1, NodeID: uuid.NewString(), ID: uuid.NewString(), AuthenticationEpoch: uuid.NewString(), AuthenticationRevision: uuid.NewString(), OperationEpoch: uuid.NewString(), At: at}
				f.image.Restore = &RestoreState{Marker: marker, Phase: "receipts"}
				first.Restore = &RestoreCommand{Marker: marker, Phase: "receipts"}
			}
			before := catalogDeltaJSON(t, f.image)
			historyIndex := f.history.catalog.Index
			childCommitDenyWrites(t, f.collections)
			m := CatalogMutation{Create: true, Record: catalogDeltaRecord("Monitor", "must-not-appear")}
			m.Record.CreatedAt, m.Record.UpdatedAt = at, at
			commands := []Command{first, {Kind: "catalog", At: at, Catalog: &m}}
			raw, err := json.Marshal(envelope{Version: CollectionActivationFormatVersion, Commands: commands})
			if err != nil {
				t.Fatal(err)
			}
			// Validation success prevents false evidence from decode rejection.
			if _, err := decodeEnvelope(raw); err != nil {
				t.Fatal("invalid failure-stop fixture", err)
			}
			result := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw})
			var path *os.PathError
			if err, ok := result.(error); !ok || !errors.As(err, &path) || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || f.history.catalog.Index != historyIndex {
				t.Fatal("failed terminal allowed later mutation or history advance", result)
			}
		})
	}
}

func TestCollectionChildCommitDirectCatalogSupersession(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, true)
			o := outcomes[0]
			m := catalogDeltaUpdate(f, o.Key)
			m.Actor, m.OperationID, m.Record.UpdatedAt = head.Actor, m.Record.Revision, o.At.Add(time.Second)
			before := catalogDeltaJSON(t, f.image)
			if fail {
				childCommitDenyWrites(t, f.collections)
			}
			r := f.applyCatalog(m, f.image.Index+1, m.Record.UpdatedAt, CollectionActivationFormatVersion)
			if fail {
				var path *os.PathError
				if r.Allowed || !errors.As(r.Err, &path) || f.err == nil || len(r.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
					t.Fatal("direct catalog failure discarded pending child", r.Err)
				}
				return
			}
			if !r.Allowed || r.Err != nil || childCommitTerminal(t, f, o).Outcome != "superseded" || len(f.collectionChildren) != 1 || f.image.Catalog[o.Key.indexKey()].Revision != m.Record.Revision {
				t.Fatal("direct catalog replacement omitted child evidence", r.Err)
			}
		})
	}
}

func TestCollectionChildCommitRejectsUnbuiltCacheAndAheadEvidence(t *testing.T) {
	for _, mode := range []string{"empty-cache", "wrong-root", "ahead-terminal", "wrong-child", "wrong-count", "missing-certificates", "forged-outcome"} {
		t.Run(mode, func(t *testing.T) {
			f, head, outcomes := childCommitFixture(t, false)
			o := outcomes[0]
			switch mode {
			case "empty-cache":
				f.collectionChildren = nil
			case "wrong-root":
				h := f.image.Collections[head.ID].Clone()
				// A nonempty claimed root with zero terminals fails validation;
				// a wrong cache root exercises the independent cache comparison.
				tree, err := newCollectionExecutionTerminalTree(h.ItemCount)
				if err != nil {
					t.Fatal(err)
				}
				terminal := executionRecordTerminal(t, o, "completed", "applied", "")
				if err := tree.Insert(terminal); err != nil {
					t.Fatal(err)
				}
				f.collectionTerminalTrees = map[string]*collectionExecutionTerminalTree{head.ID: tree}
			case "ahead-terminal":
				terminal := executionRecordTerminal(t, o, "completed", "applied", "")
				executionLedgerApply(t, f.collections, collectionExecutionRecord{Version: 1, Terminal: &terminal})
			case "wrong-child":
				link := f.collectionChildren[o.Receipt.ID]
				link.RowDigest = strings.Repeat("f", 64)
				f.collectionChildren[o.Receipt.ID] = link
			case "wrong-count":
				stat := f.collections.executionStats[head.ID]
				stat.ChargedBytes++
				f.collections.executionStats[head.ID] = stat
			case "missing-certificates":
				f.collectionOutcomeCommitments = nil
			case "forged-outcome":
				forged := o.Clone()
				forged.MutationSequence = 9 // Same encoded length; receipt is unchanged.
				raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Outcome: &forged})
				if err != nil {
					t.Fatal(err)
				}
				slot := collectionExecutionOutcomeSlot(o.Ordinal)
				if len(raw) != len(f.collections.executionRows[head.ID][slot]) {
					t.Fatal("corruption fixture changed ledger byte counters")
				}
				f.collections.executionRows[head.ID][slot] = raw
			}
			before := catalogDeltaJSON(t, f.image)
			result := f.applyOperation(childCommitUpdate(o, true), o.At.Add(time.Second))
			if result.Allowed || result.Err == nil || f.err == nil || len(result.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("uncertified evidence retired pending work", result.Err)
			}
		})
	}
}

func TestCollectionChildCommitExistingFormatsRejectExecutionHeader(t *testing.T) {
	f, head, _ := childCommitFixture(t, false)
	control := head.Clone()
	control.Execution = nil
	f.image.Collections[head.ID] = control
	if err := validateCollectionHeaders(f.image); err != nil {
		t.Fatal("format-8 control failed for an unrelated reason", err)
	}
	f.image.Collections[head.ID] = head
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion,
		CollectionValidationFormatVersion, CollectionValidationRequestFormatVersion, CollectionActivationFormatVersion} {
		f.image.Version = version
		if !errors.Is(validateCollectionHeaders(f.image), ErrCollectionInvalid) {
			t.Fatal("old format accepted unsupported execution header", version)
		}
	}
}
