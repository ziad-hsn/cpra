package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The source, sealed plan, result and activation are admitted through existing
// Store commands. Execution calls the new private transaction directly until
// its outer snapshot format and isolated log registration land together.
func executeFixture(t *testing.T, disk bool) (*machine, CollectionState, CollectionExecuteCommand, *collectionExecutionIndex) {
	t.Helper()
	s := openCatalogMemory(t)
	head, authority := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	f := catalogDeltaCloneMachine(t, s.fsm)
	f.collections = newLedgerTest(t, disk, 16<<20)
	a, b, c := validationLedgerStreams(t, s.fsm.collections)
	for _, load := range []struct {
		data []byte
		read func(*bytes.Reader, *collectionLedger) error
	}{{a, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionLedger(r, l) }},
		{b, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionPlanLedger(r, l) }},
		{c, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionValidationLedger(r, l) }}} {
		if err := load.read(bytes.NewReader(load.data), f.collections); err != nil {
			t.Fatal(err)
		}
	}
	view, err := f.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	x, err := buildCollectionExecutionIndex(context.Background(), head, view, defaultCollectionExecutionIndexLimits())
	_ = view.Close()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	command := CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: authority, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
	return f, head, command, x
}

func executeApply(t *testing.T, f *machine, c CollectionExecuteCommand, at time.Time, x *collectionExecutionIndex) Result {
	t.Helper()
	r := f.applyCollectionExecution(c, f.image.Index+1, at, x)
	if r.Err == nil && r.Allowed {
		// The private transaction deliberately leaves the normal Apply history
		// barrier/index step to its caller. This harness models that last step.
		f.image.Index++
	}
	return r
}

func executeCandidate(t *testing.T, c CollectionExecuteCommand, x *collectionExecutionIndex, ordinal uint64, at time.Time) CollectionExecuteCommand {
	t.Helper()
	row, ok := x.row(ordinal)
	if !ok || row.Row.Change != "create" {
		t.Fatal("expected original create row")
	}
	r := catalogDeltaRecord(row.Row.Key.Kind, row.Row.Key.ID)
	r.CreatedAt, r.UpdatedAt = at, at
	c.Action, c.Ordinal = "prepare", ordinal
	c.Prepared = &CollectionPreparedItem{Binding: c.Binding, ID: uuid.NewString(), Ordinal: ordinal, InputOrdinal: row.Row.InputOrdinal,
		RowDigest: row.RowDigest, At: at, Record: r}
	return c
}

func executeDecision(c CollectionExecuteCommand) CollectionExecuteCommand {
	c.Action, c.PreparedID = "decide", c.Prepared.ID
	c.Prepared = nil
	return c
}

func TestCollectionExecuteAtomicCreateAndExactRetries(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f, head, begin, x := executeFixture(t, disk)
			at := head.Activation.At.Add(time.Second)
			if r := executeApply(t, f, begin, at, x); r.Err != nil || !r.Allowed || r.Collection.Execution == nil {
				t.Fatal("begin", r.Err)
			}
			prepared := executeCandidate(t, begin, x, 1, at.Add(time.Second))
			for n := 0; n < 2; n++ {
				if r := executeApply(t, f, prepared, prepared.Prepared.At, x); r.Err != nil || !r.Allowed || len(f.image.Catalog) != 0 || f.pendingOperationCount() != 0 {
					t.Fatal("preparation mutated catalog or lost exact candidate", r.Err)
				}
			}
			highWater := f.image.OperationHighWater
			decision := executeDecision(prepared)
			r := executeApply(t, f, decision, prepared.Prepared.At.Add(time.Second), x)
			if r.Err != nil || !r.Allowed || r.Operation == nil || r.Operation.ID == head.ID || f.image.OperationHighWater != highWater+1 ||
				r.Collection.Execution.Processed != 1 || r.Collection.Execution.Accepted != 1 || r.Collection.Execution.Prepared != nil || len(f.collectionChildren) != 1 {
				t.Fatal("atomic acceptance", r.Err)
			}
			stored := f.image.Catalog[prepared.Prepared.Record.Key.indexKey()]
			if stored.Revision != prepared.Prepared.Record.Revision || stored.CommittedIndex != r.Operation.CommittedIndex {
				t.Fatal("accepted different candidate")
			}
			before := catalogDeltaJSON(t, f.image)
			// Reconcile the old ordinal without a new child, catalog token or event.
			retry := f.applyCollectionExecution(decision, f.image.Index+1, prepared.Prepared.At.Add(2*time.Second), x)
			if retry.Err != nil || !retry.Allowed || len(retry.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("retry changed committed decision", retry.Err)
			}
			progress := f.image.Collections[head.ID].Execution
			if !f.collectionOutcomeCommitments[head.ID].matches(progress.Binding, progress.Processed, progress.OutcomeDigest) {
				t.Fatal("acceptance failed to install committed predecessor identity")
			}
		})
	}
}

func TestCollectionExecuteConflictConsumesOnlyItsPreparedItem(t *testing.T) {
	f, head, begin, x := executeFixture(t, false)
	at := head.Activation.At.Add(time.Second)
	if r := executeApply(t, f, begin, at, x); r.Err != nil {
		t.Fatal(r.Err)
	}
	prepared := executeCandidate(t, begin, x, 1, at.Add(time.Second))
	if r := executeApply(t, f, prepared, prepared.Prepared.At, x); r.Err != nil {
		t.Fatal(r.Err)
	}
	out := prepared.Prepared.Record.Clone()
	out.UID, out.Revision = "outside-uid", "outside-revision"
	if r := f.applyCatalog(CatalogMutation{Record: out, Create: true}, f.image.Index+1, prepared.Prepared.At, CatalogMutationFormatVersion); r.Err != nil || !r.Allowed {
		t.Fatal("outside write fixture", r.Err)
	}
	f.image.Index++
	highWater, token := f.image.OperationHighWater, f.image.CatalogMutationSequence
	r := executeApply(t, f, executeDecision(prepared), prepared.Prepared.At.Add(time.Second), x)
	if r.Err != nil || !r.Allowed || r.Operation != nil || r.Collection.Execution.Conflicts != 1 || r.Collection.Execution.Prepared != nil ||
		f.image.OperationHighWater != highWater || f.image.CatalogMutationSequence != token || f.image.Catalog[out.Key.indexKey()].UID != out.UID || len(f.collectionChildren) != 0 {
		t.Fatal("conflict executed or refreshed a candidate", r.Err)
	}
}

func TestCollectionExecuteNativeFailureLeavesPreparedAndCatalogUnchanged(t *testing.T) {
	f, head, begin, x := executeFixture(t, true)
	at := head.Activation.At.Add(time.Second)
	if r := executeApply(t, f, begin, at, x); r.Err != nil {
		t.Fatal(r.Err)
	}
	prepared := executeCandidate(t, begin, x, 1, at.Add(time.Second))
	if r := executeApply(t, f, prepared, prepared.Prepared.At, x); r.Err != nil {
		t.Fatal(r.Err)
	}
	before := catalogDeltaJSON(t, f.image)
	certified := f.collectionOutcomeCommitments[head.ID]
	childCommitDenyWrites(t, f.collections)
	r := f.applyCollectionExecution(executeDecision(prepared), f.image.Index+1, prepared.Prepared.At.Add(time.Second), x)
	var path *os.PathError
	if r.Allowed || !errors.As(r.Err, &path) || f.err == nil || len(r.Events) != 0 || !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || f.collectionOutcomeCommitments[head.ID] != certified {
		t.Fatal("failed item transaction advanced active state", r.Err)
	}
}
