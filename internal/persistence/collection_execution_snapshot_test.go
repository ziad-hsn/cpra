package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type executionRecoveryFixture struct {
	image   image
	head    CollectionState
	ledger  *collectionLedger
	records []collectionExecutionRecord
}

// The source/plan/result/admission path is real. Outcomes below are explicit
// acceptance fixtures, not evidence that an item executor or format9 exists.
func executionRecoveryInput(t *testing.T, disk bool) executionRecoveryFixture {
	t.Helper()
	return executionRecoveryInputStore(t, openCatalogMemory(t), disk)
}

func executionRecoveryInputStore(t *testing.T, s *Store, disk bool) executionRecoveryFixture {
	t.Helper()
	head, _ := validationPublishFixture(t, s, 6, true)
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	i := catalogDeltaCloneMachine(t, s.fsm).image
	i.Index += 20
	i.OperationHighWater = 1000
	i.CatalogMutationSequence = 1000
	i.Operations = map[string]OperationReceipt{}
	a, b, c := validationLedgerStreams(t, s.fsm.collections)
	l := newLedgerTest(t, disk, 32<<20)
	if err := importCollectionLedger(bytes.NewReader(a), l); err != nil {
		t.Fatal(err)
	}
	if err := importCollectionPlanLedger(bytes.NewReader(b), l); err != nil {
		t.Fatal(err)
	}
	if err := importCollectionValidationLedger(bytes.NewReader(c), l); err != nil {
		t.Fatal(err)
	}
	view, err := l.Freeze()
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
	progress, err := NewCollectionExecutionProgress(binding, head.ItemCount, at)
	if err != nil {
		t.Fatal(err)
	}
	_, parentSequence, _ := ParseOperationHandle(head.ID)
	var records []collectionExecutionRecord
	var outcomes []CollectionItemOutcome
	for ordinal := uint64(1); ordinal <= head.ItemCount; ordinal++ {
		row, _ := x.row(ordinal)
		r := row.Row
		p := CollectionPreparedItem{Binding: binding, ID: uuid.NewString(), Ordinal: ordinal, InputOrdinal: r.InputOrdinal, RowDigest: row.RowDigest, At: at.Add(time.Duration(ordinal) * time.Second), Record: catalogDeltaRecord(r.Key.Kind, r.Key.ID)}
		p.Record.CreatedAt, p.Record.UpdatedAt = p.At, p.At
		o := CollectionItemOutcome{Binding: binding, Ordinal: ordinal, InputOrdinal: r.InputOrdinal, RowDigest: row.RowDigest, Key: r.Key, Source: r.Source, SourceDocument: r.Document, SourceItem: r.Item, At: p.At.Add(time.Millisecond), CommittedIndex: i.Index, Decision: "accepted", PreparedID: p.ID, UID: p.Record.UID, Revision: p.Record.Revision, Generation: p.Record.Generation, MutationSequence: parentSequence*10 + ordinal}
		o.Receipt = &OperationReceipt{ID: operationHandle(i.OperationEpoch, parentSequence*100+ordinal), Key: o.Key, UID: o.UID, NewVersion: o.Revision, Generation: o.Generation, CommittedIndex: o.CommittedIndex, Actor: head.Actor, At: o.At, UpdatedAt: o.At, State: "committed", Outcome: "committed"}
		if ordinal == 3 || ordinal == 4 {
			o.Decision = "conflict"
			if ordinal == 4 {
				o.Decision = "dependencyBlocked"
			}
			o.PreparedID, o.UID, o.Revision = "", "", ""
			o.Generation, o.MutationSequence = 0, 0
			o.Receipt = nil
		}
		if ordinal == 6 || o.Decision == "accepted" {
			progress, err = progress.withPrepared(p)
			if err != nil {
				t.Fatal(err)
			}
			executionLedgerApply(t, l, collectionExecutionRecord{Version: 1, Prepared: &p})
		}
		if ordinal == 6 {
			records = append(records, collectionExecutionRecord{Version: 1, Prepared: &p})
			break
		}
		progress, err = progress.withOutcome(o)
		if err != nil {
			t.Fatal(err)
		}
		executionLedgerApply(t, l, collectionExecutionRecord{Version: 1, Outcome: &o})
		records = append(records, collectionExecutionRecord{Version: 1, Outcome: &o})
		outcomes = append(outcomes, o)
		if o.Receipt != nil {
			i.Operations[o.Receipt.ID] = *o.Receipt
		}
	}
	tree, _ := newCollectionExecutionTerminalTree(head.ItemCount)
	// Completion order differs from plan order and occurs after the retained
	// prepared slot. The sparse root and LastAt must reconstruct independently.
	for _, ordinal := range []uint64{5, 2} {
		o := outcomes[ordinal-1]
		terminal := executionRecordTerminal(t, o, "completed", "applied", "")
		if ordinal == 5 {
			terminal = executionRecordTerminal(t, o, "partial", "superseded", uuid.NewString())
		}
		terminal.UpdatedAt = at.Add(time.Duration(12-ordinal) * time.Second)
		proof, _ := tree.Proof(ordinal)
		progress, err = progress.withTerminal(o, terminal, proof)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(terminal); err != nil {
			t.Fatal(err)
		}
		record := collectionExecutionRecord{Version: 1, Terminal: &terminal}
		executionLedgerApply(t, l, record)
		records = append(records, record)
		delete(i.Operations, o.Receipt.ID)
	}
	head.Execution = &progress
	i.Collections[head.ID] = head
	if err := head.validate(); err != nil {
		t.Fatal(err)
	}
	return executionRecoveryFixture{i, head, l, records}
}

func executionRecoveryCheck(t *testing.T, f executionRecoveryFixture) (*collectionExecutionRecovery, error) {
	t.Helper()
	view, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	return validateCollectionExecutionInventory(context.Background(), f.image, view)
}

func TestCollectionExecutionRecoveryCompleteOriginalInventory(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRecoveryInput(t, disk)
			before, _ := f.ledger.Bytes()
			original := f.head.Clone()
			got, err := executionRecoveryCheck(t, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.children) != 1 || len(got.trees) != 1 || len(got.commitments) != 1 || got.trees[f.head.ID].Root() != f.head.Execution.TerminalRoot {
				t.Fatal("incomplete reconstructed caches")
			}
			cache := got.commitments[f.head.ID]
			if !cache.matches(f.head.Execution.Binding, 5, f.head.Execution.OutcomeDigest) {
				t.Fatal("prefix commitment mismatch")
			}
			for _, r := range f.records {
				if r.Outcome != nil && !cache.matchesRecord(r) {
					t.Fatal("missing original outcome certification")
				}
			}
			after, _ := f.ledger.Bytes()
			if before != after || !reflect.DeepEqual(original, f.image.Collections[f.head.ID]) {
				t.Fatal("recovery mutated source")
			}
			// No transaction is retained by the result: a subsequent write can proceed.
			if err := f.ledger.ApplyExecutionBatch([]collectionExecutionRecord{f.records[len(f.records)-1]}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectionExecutionRecoveryHeaderTampering(t *testing.T) {
	f := executionRecoveryInput(t, false)
	cases := map[string]func(*CollectionState){
		"count":            func(s *CollectionState) { s.Execution.Processed-- },
		"decision-count":   func(s *CollectionState) { s.Execution.Accepted--; s.Execution.Conflicts++ },
		"outcome-digest":   func(s *CollectionState) { s.Execution.OutcomeDigest = strings.Repeat("c", 64) },
		"terminal-root":    func(s *CollectionState) { s.Execution.TerminalRoot = strings.Repeat("d", 64) },
		"last-time":        func(s *CollectionState) { s.Execution.LastAt = s.Execution.LastAt.Add(time.Second) },
		"start-time":       func(s *CollectionState) { s.Execution.StartedAt = s.Execution.StartedAt.Add(time.Nanosecond) },
		"prepared-digest":  func(s *CollectionState) { s.Execution.Prepared.Digest = strings.Repeat("d", 64) },
		"prepared-missing": func(s *CollectionState) { s.Execution.Prepared = nil },
		"bytes": func(s *CollectionState) {
			s.Execution.OutcomeBytes++
			s.Execution.EncodedBytes++
			s.Execution.ChargedBytes++
		},
		"plan-digest":   func(s *CollectionState) { s.Plan.Descriptor.Digest = strings.Repeat("a", 64) },
		"source-prefix": func(s *CollectionState) { s.ProgressDigest = strings.Repeat("b", 64) },
		"unsealed":      func(s *CollectionState) { s.Validation.HistorySealed = false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			i := f.image
			s := f.head.Clone()
			change(&s)
			i.Collections = map[string]CollectionState{s.ID: s}
			candidate := f
			candidate.image = i
			got, err := executionRecoveryCheck(t, candidate)
			if err == nil || got != nil {
				t.Fatal("accepted altered authoritative header")
			}
		})
	}
}

func TestCollectionExecutionRecoveryAuditOnlyTerminalParents(t *testing.T) {
	for _, phase := range []string{"applying", "canceled", "invalidated"} {
		t.Run(phase, func(t *testing.T) {
			f := executionRecoveryInput(t, false)
			head := f.head.Clone()
			if phase != "applying" {
				head.Phase = phase
				head.TerminalAt = head.Activation.At.Add(time.Second)
				if phase == "canceled" {
					head.Cancellation = &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: head.TerminalAt}
				} else {
					head.InvalidatedByRestore = uuid.NewString()
					f.image.OperationEpoch = uuid.NewString()
					f.image.OperationHighWater = 0
				}
			}
			f.image.Collections[head.ID] = head
			view, err := f.ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			x, err := buildCollectionExecutionAuditIndex(context.Background(), head, view, defaultCollectionExecutionIndexLimits())
			if err != nil {
				t.Fatal(err)
			}
			if x.matches(head) {
				t.Fatal("audit index became execution eligible")
			}
			if phase != "applying" {
				if _, err := buildCollectionExecutionIndex(context.Background(), head, view, defaultCollectionExecutionIndexLimits()); err == nil {
					t.Fatal("normal constructor admitted terminal parent")
				}
			}
			got, err := validateCollectionExecutionInventory(context.Background(), f.image, view)
			if err != nil || len(got.children) != 1 {
				t.Fatal("terminal evidence lost", err)
			}
		})
	}
}

func TestCollectionExecutionRecoveryMissingOrOrphanEvidence(t *testing.T) {
	for _, mode := range []string{"missing-outcome", "missing-terminal", "orphan-parent", "missing-progress", "pending-terminal", "missing-pending", "reserved-child", "index-limit", "token-limit", "highwater"} {
		t.Run(mode, func(t *testing.T) {
			f := executionRecoveryInput(t, false)
			switch mode {
			case "missing-outcome":
				delete(f.ledger.executionRows[f.head.ID], collectionExecutionOutcomeSlot(2))
			case "missing-terminal":
				delete(f.ledger.executionRows[f.head.ID], collectionExecutionTerminalSlot(2))
			case "orphan-parent":
				delete(f.image.Collections, f.head.ID)
			case "missing-progress":
				s := f.head.Clone()
				s.Execution = nil
				f.image.Collections[s.ID] = s
			case "pending-terminal":
				o := f.records[1].Outcome
				f.image.Operations[o.Receipt.ID] = *o.Receipt
			case "missing-pending":
				delete(f.image.Operations, f.records[0].Outcome.Receipt.ID)
			case "reserved-child":
				f.image.OperationReservations = map[string]OperationReservation{f.records[0].Outcome.Receipt.ID: {}}
			case "index-limit":
				f.image.Index--
			case "token-limit":
				f.image.CatalogMutationSequence = 1
			case "highwater":
				f.image.OperationHighWater = 100
			}
			got, err := executionRecoveryCheck(t, f)
			if err == nil || got != nil {
				t.Fatal("accepted missing/orphan/future evidence")
			}
		})
	}
}

func TestCollectionExecutionRecoveryCancellationAndClosedView(t *testing.T) {
	f := executionRecoveryInput(t, false)
	v, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validateCollectionExecutionInventory(ctx, f.image, v); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = v.Close()
	if _, err := validateCollectionExecutionInventory(context.Background(), f.image, v); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal(err)
	}
}

// Rebuild forged-but-canonical evidence through the normal record/progress
// helpers so rejection cannot be explained by a stale byte count or hash alone.
func executionRecoveryRewrite(t *testing.T, f executionRecoveryFixture, mutate func([]collectionExecutionRecord)) executionRecoveryFixture {
	t.Helper()
	rows := make([]collectionExecutionRecord, len(f.records))
	for j, r := range f.records {
		raw, err := collectionExecutionEncoding(r)
		if err != nil {
			t.Fatal(err)
		}
		rows[j], err = decodeCollectionExecutionRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	mutate(rows)
	a, b, c := validationLedgerStreams(t, f.ledger)
	l := newLedgerTest(t, false, 32<<20)
	for _, input := range []struct {
		raw  []byte
		load func(*bytes.Reader, *collectionLedger) error
	}{
		{a, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionLedger(r, l) }},
		{b, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionPlanLedger(r, l) }},
		{c, func(r *bytes.Reader, l *collectionLedger) error { return importCollectionValidationLedger(r, l) }},
	} {
		if err := input.load(bytes.NewReader(input.raw), l); err != nil {
			t.Fatal(err)
		}
	}
	head := f.head.Clone()
	p, err := NewCollectionExecutionProgress(head.Execution.Binding, head.ItemCount, head.Activation.At)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]OperationReceipt{}
	for _, r := range rows {
		if o := r.Outcome; o != nil {
			if o.PreparedID != "" {
				prepared := CollectionPreparedItem{Binding: o.Binding, ID: o.PreparedID, Ordinal: o.Ordinal, InputOrdinal: o.InputOrdinal, RowDigest: o.RowDigest, At: o.At, Record: catalogDeltaRecord(o.Key.Kind, o.Key.ID)}
				prepared.Record.UID, prepared.Record.Revision, prepared.Record.Generation = o.UID, o.Revision, o.Generation
				prepared.Record.CreatedAt, prepared.Record.UpdatedAt = o.At, o.At
				p, err = p.withPrepared(prepared)
				if err != nil {
					t.Fatal("forge preparation", err)
				}
			}
			p, err = p.withOutcome(*o)
			if err != nil {
				t.Fatal("forge outcome", err)
			}
			if o.Receipt != nil {
				operations[o.Receipt.ID] = *o.Receipt
			}
		}
	}
	for _, r := range rows {
		if r.Prepared != nil {
			p, err = p.withPrepared(*r.Prepared)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	tree, _ := newCollectionExecutionTerminalTree(head.ItemCount)
	for _, r := range rows {
		if z := r.Terminal; z != nil {
			var accepted *CollectionItemOutcome
			for _, candidate := range rows {
				if candidate.Outcome != nil && candidate.Outcome.Ordinal == z.Ordinal {
					accepted = candidate.Outcome
					break
				}
			}
			proof, _ := tree.Proof(z.Ordinal)
			p, err = p.withTerminal(*accepted, *z, proof)
			if err != nil {
				t.Fatal("forge terminal", err)
			}
			if err := tree.Insert(*z); err != nil {
				t.Fatal(err)
			}
			delete(operations, z.ChildID)
		}
	}
	// import supports consumed historical prepared slots but still validates
	// contiguous canonical outcomes, terminal linkage and shared quota.
	if len(rows) > 0 {
		if err := l.importExecutionBatch(rows); err != nil {
			t.Fatal("forge ledger", err)
		}
	}
	head.Execution = &p
	f.image.Collections = map[string]CollectionState{head.ID: head}
	f.image.Operations = operations
	f.ledger, f.head, f.records = l, head, rows
	return f
}

func TestCollectionExecutionRecoveryCanonicalForgery(t *testing.T) {
	cases := map[string]func([]collectionExecutionRecord){
		"wrong-key": func(r []collectionExecutionRecord) {
			o := r[0].Outcome
			o.Key.ID = "wrong-original-key"
			o.Receipt.Key = o.Key
		},
		"wrong-source":      func(r []collectionExecutionRecord) { r[0].Outcome.Source = "source.00000000000000000009" },
		"wrong-document":    func(r []collectionExecutionRecord) { r[0].Outcome.SourceDocument++ },
		"wrong-source-item": func(r []collectionExecutionRecord) { r[0].Outcome.SourceItem++ },
		"wrong-input":       func(r []collectionExecutionRecord) { r[0].Outcome.InputOrdinal = 2 },
		"wrong-row-digest":  func(r []collectionExecutionRecord) { r[0].Outcome.RowDigest = strings.Repeat("e", 64) },
		"wrong-actor":       func(r []collectionExecutionRecord) { r[0].Outcome.Receipt.Actor = "other-operator" },
		"create-generation": func(r []collectionExecutionRecord) { o := r[0].Outcome; o.Generation = 2; o.Receipt.Generation = 2 },
		"old-version": func(r []collectionExecutionRecord) {
			o := r[0].Outcome
			o.OldVersion = "invented"
			o.Receipt.OldVersion = o.OldVersion
		},
		"duplicate-terminal-child": func(r []collectionExecutionRecord) {
			id := r[1].Outcome.Receipt.ID
			r[4].Outcome.Receipt.ID = id
			for j := range r {
				if r[j].Terminal != nil && r[j].Terminal.Ordinal == 5 {
					r[j].Terminal.ChildID = id
				}
			}
		},
		"duplicate-token": func(r []collectionExecutionRecord) { r[4].Outcome.MutationSequence = r[1].Outcome.MutationSequence },
		"prepared-wrong-key": func(r []collectionExecutionRecord) {
			for j := range r {
				if r[j].Prepared != nil {
					r[j].Prepared.Record.Key.ID = "other-prepared-key"
				}
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := executionRecoveryInput(t, false)
			f = executionRecoveryRewrite(t, f, change)
			if got, err := executionRecoveryCheck(t, f); err == nil || got != nil {
				t.Fatal("accepted fully rehashed wrong original evidence")
			}
		})
	}
}

func TestCollectionExecutionRecoveryParentCollisionAndBounds(t *testing.T) {
	f := executionRecoveryInput(t, false)
	// Every retained collection handle is reserved, even if it has no execution
	// state and is not this child's own parent. No history lookup is needed.
	child := f.records[0].Outcome.Receipt.ID
	other := f.head.Clone()
	other.ID = child
	other.Execution = nil
	f.image.Collections[child] = other
	if got, err := executionRecoveryCheck(t, f); err == nil || got != nil {
		t.Fatal("child impersonated another retained parent")
	}
	// Explicit counts are checked before allocating audit buffers or touching rows.
	delete(f.image.Collections, child)
	invalid := f.head.Clone()
	invalid.Execution.ItemCount = CollectionValidationMaxItems + 1
	f.image.Collections[invalid.ID] = invalid
	if got, err := executionRecoveryCheck(t, f); err == nil || got != nil {
		t.Fatal("unbounded declared inventory")
	}
}

func TestCollectionExecutionRecoveryContendedLockCancellation(t *testing.T) {
	f := executionRecoveryInput(t, false)
	v, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	v.mu.Lock()
	defer v.mu.Unlock()
	base, stop := context.WithCancel(context.Background())
	defer stop()
	waiting := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := validateCollectionExecutionInventory(waiting, f.image, v); done <- err }()
	select {
	case <-waiting.waiting:
	case <-time.After(time.Second):
		t.Fatal("validator did not reach contended lock")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("validator ignored cancellation")
	}
}

func TestCollectionExecutionRecoveryEmptyAndPreparedOnly(t *testing.T) {
	f := executionRecoveryInput(t, false)
	f.records = nil
	f = executionRecoveryRewrite(t, f, func([]collectionExecutionRecord) {})
	if got, err := executionRecoveryCheck(t, f); err != nil || len(got.children) != 0 || got.commitments[f.head.ID].Count != 0 {
		t.Fatal("empty admitted progress rejected", err)
	}
	v, _ := f.ledger.Freeze()
	x, err := buildCollectionExecutionAuditIndex(context.Background(), f.head, v, defaultCollectionExecutionIndexLimits())
	_ = v.Close()
	if err != nil {
		t.Fatal(err)
	}
	row, _ := x.row(1)
	p := CollectionPreparedItem{Binding: f.head.Execution.Binding, ID: uuid.NewString(), Ordinal: 1, InputOrdinal: row.Row.InputOrdinal, RowDigest: row.RowDigest, At: f.head.Activation.At, Record: catalogDeltaRecord(row.Row.Key.Kind, row.Row.Key.ID)}
	p.Record.CreatedAt, p.Record.UpdatedAt = p.At, p.At
	f.records = []collectionExecutionRecord{{Version: 1, Prepared: &p}}
	f = executionRecoveryRewrite(t, f, func([]collectionExecutionRecord) {})
	if got, err := executionRecoveryCheck(t, f); err != nil || len(got.children) != 0 || got.commitments[f.head.ID].Count != 0 {
		t.Fatal("prepared-only progress rejected", err)
	}
}

func TestCollectionExecutionRecoveryOriginalUpdateAndUnchangedTuples(t *testing.T) {
	f := executionRecoveryInput(t, false)
	o := f.records[0].Outcome.Clone()
	row := collectionExecutionRow{Row: CollectionPlanRow{Ordinal: o.Ordinal, InputOrdinal: o.InputOrdinal, Key: o.Key, Source: o.Source, Document: o.SourceDocument, Item: o.SourceItem, Change: "update", Target: CollectionPlanGuard{Key: o.Key, OriginalUID: o.UID, OriginalRevision: "before", OriginalGeneration: 1}}, RowDigest: o.RowDigest}
	o.OldVersion = "before"
	o.Receipt.OldVersion = "before"
	if !collectionExecutionOutcomeOriginal(f.image, f.head, row, o) {
		t.Fatal("valid update rejected")
	}
	for _, mutate := range []func(*CollectionItemOutcome){
		func(o *CollectionItemOutcome) { o.UID = "other"; o.Receipt.UID = o.UID },
		func(o *CollectionItemOutcome) { o.Revision = "before"; o.Receipt.NewVersion = o.Revision },
		func(o *CollectionItemOutcome) { o.Generation = 3; o.Receipt.Generation = 3 },
	} {
		bad := o.Clone()
		mutate(&bad)
		if collectionExecutionOutcomeOriginal(f.image, f.head, row, bad) {
			t.Fatal("invalid update tuple accepted")
		}
	}
	unchanged := o.Clone()
	unchanged.Decision = "unchanged"
	unchanged.Revision = "before"
	unchanged.OldVersion = "before"
	unchanged.PreparedID = ""
	unchanged.MutationSequence = 0
	unchanged.Receipt = nil
	row.Row.Change = "unchanged"
	if !collectionExecutionOutcomeOriginal(f.image, f.head, row, unchanged) {
		t.Fatal("original unchanged tuple rejected")
	}
	unchanged.Revision = "new"
	unchanged.OldVersion = "new"
	if collectionExecutionOutcomeOriginal(f.image, f.head, row, unchanged) {
		t.Fatal("changed original version certified as unchanged")
	}
	// The record codec enforces same epoch; recovery adds issued sequence order.
	row.Row.Change = "update"
	epoch, _, _ := ParseOperationHandle(f.head.ID)
	head := f.head.Clone()
	head.ID = operationHandle(epoch, 50)
	head.Execution.Binding.OperationID = head.ID
	o.Binding.OperationID = head.ID
	o.Receipt.ID = operationHandle(epoch, 49)
	if collectionExecutionOutcomeOriginal(f.image, head, row, o) {
		t.Fatal("child allocated before its parent")
	}
}

func TestCollectionExecutionRecoveryCrossParentHistoricalUniqueness(t *testing.T) {
	for _, mode := range []string{"valid", "child", "mutation-token"} {
		t.Run(mode, func(t *testing.T) {
			s := openCatalogMemory(t)
			first := executionRecoveryInputStore(t, s, false)
			second := executionRecoveryInputStore(t, s, false)
			// Both affected children are already terminal, so the <=4096 unresolved
			// index cannot detect their duplicate. The global audit must do so.
			second = executionRecoveryRewrite(t, second, func(rows []collectionExecutionRecord) {
				if mode == "child" {
					rows[1].Outcome.Receipt.ID = first.records[1].Outcome.Receipt.ID
					for j := range rows {
						if rows[j].Terminal != nil && rows[j].Terminal.Ordinal == 2 {
							rows[j].Terminal.ChildID = rows[1].Outcome.Receipt.ID
						}
					}
				}
				if mode == "mutation-token" {
					rows[1].Outcome.MutationSequence = first.records[1].Outcome.MutationSequence
				}
			})
			second.image.Collections[first.head.ID] = first.head
			for id, pending := range first.image.Operations {
				second.image.Operations[id] = pending
			}
			if err := second.ledger.importExecutionBatch(first.records); err != nil {
				t.Fatal(err)
			}
			got, err := executionRecoveryCheck(t, second)
			if mode == "valid" {
				if err != nil || len(got.children) != 2 || len(got.commitments) != 2 {
					t.Fatal("valid multi-parent inventory", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("duplicate historical identity accepted")
			}
		})
	}
}

func TestCollectionExecutionRecoveryMissingOriginalSources(t *testing.T) {
	for _, mode := range []string{"input", "plan-row", "plan-footer"} {
		t.Run(mode, func(t *testing.T) {
			f := executionRecoveryInput(t, false)
			switch mode {
			case "input":
				delete(f.ledger.rows[f.head.ID], 1)
			case "plan-row":
				delete(f.ledger.planRows[f.head.ID], 2)
			case "plan-footer":
				delete(f.ledger.planRows[f.head.ID], f.head.Plan.UploadedFragments)
			}
			if got, err := executionRecoveryCheck(t, f); err == nil || got != nil {
				t.Fatal("missing source/plan accepted")
			}
		})
	}
}

func TestCollectionExecutionRecoveryReuseVerifiedNormalIndex(t *testing.T) {
	f := executionRecoveryInput(t, false)
	view, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	normal, err := buildCollectionExecutionIndex(context.Background(), f.head, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := validateCollectionExecutionParentWithIndex(context.Background(), f.image, f.head, view, nil, normal)
	if err != nil || !collectionExecutionProgressEqual(got.progress, *f.head.Execution) {
		t.Fatal("reused original index failed", err)
	}
	audit, err := buildCollectionExecutionAuditIndex(context.Background(), f.head, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateCollectionExecutionParentWithIndex(context.Background(), f.image, f.head, view, nil, audit); err == nil {
		t.Fatal("audit-only index entered live reuse path")
	}
	bad := f.head.Clone()
	bad.Execution.OutcomeDigest = strings.Repeat("b", 64)
	if _, err := validateCollectionExecutionParentWithIndex(context.Background(), f.image, bad, view, nil, normal); err == nil {
		t.Fatal("reuse skipped outcome verification")
	}
	bad = f.head.Clone()
	bad.Activation.ID = uuid.NewString()
	bad.Execution.Binding.ActivationID = bad.Activation.ID
	if _, err := validateCollectionExecutionParentWithIndex(context.Background(), f.image, bad, view, nil, normal); err == nil {
		t.Fatal("reuse accepted different activation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validateCollectionExecutionParentWithIndex(ctx, f.image, f.head, view, nil, normal); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
