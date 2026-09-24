package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"reflect"
	"testing"
	"time"
)

// This fixture supplies only the already-validated image metadata consumed by
// the link builder. Plan/source/authority validation belongs to their existing
// validators; these tests do not claim a public execution path exists.
func childLinksImage(records ...collectionExecutionRecord) image {
	i := image{Collections: make(map[string]CollectionState), Operations: make(map[string]OperationReceipt), OperationReservations: make(map[string]OperationReservation)}
	for _, record := range records {
		b := collectionExecutionBinding(record)
		descriptor := CollectionPlanDescriptor{Digest: b.PlanDigest}
		i.Collections[b.OperationID] = CollectionState{ID: b.OperationID, UploadID: b.UploadID, ItemCount: 10000, Phase: "applying", Plan: &CollectionPlanState{Header: CollectionPlanHeader{PlanID: b.PlanID}, Descriptor: descriptor}, Activation: &CollectionActivation{ID: b.ActivationID, PlanID: b.PlanID, PlanDescriptor: descriptor, At: catalogDeltaAt}}
		if record.Outcome != nil && record.Outcome.Receipt != nil {
			i.Operations[record.Outcome.Receipt.ID] = *record.Outcome.Receipt
		}
	}
	return i
}

func TestCollectionChildLinkExactOrdinaryIdentityAndCapacity(t *testing.T) {
	_, record, _ := executionLedgerRecords(t, 1, 1)
	accepted := *record.Outcome
	pending := *accepted.Receipt
	link, err := collectionChildLinkFor(accepted, pending)
	if err != nil {
		t.Fatal(err)
	}
	zoned := pending
	zoned.At = zoned.At.In(time.FixedZone("same-instant", 7200))
	zoned.UpdatedAt = zoned.UpdatedAt.In(time.FixedZone("same-instant", 7200))
	if got, err := collectionChildLinkFor(accepted, zoned); err != nil || got != link {
		t.Fatal("time representation changed identity", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*OperationReceipt)
	}{
		{"actor", func(r *OperationReceipt) { r.Actor = "another" }}, {"old-version", func(r *OperationReceipt) { r.OldVersion = "other" }},
		{"uid", func(r *OperationReceipt) { r.UID = "other" }}, {"revision", func(r *OperationReceipt) { r.NewVersion = "other" }},
		{"generation", func(r *OperationReceipt) { r.Generation++ }}, {"commit-position", func(r *OperationReceipt) { r.CommittedIndex++ }},
		{"key", func(r *OperationReceipt) { r.Key.ID = "other" }}, {"subject", func(r *OperationReceipt) { r.Subject = "control" }},
		{"updated-at", func(r *OperationReceipt) { r.UpdatedAt = r.UpdatedAt.Add(time.Second) }},
		{"terminal", func(r *OperationReceipt) { r.State = "completed"; r.Outcome = "applied" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := pending
			tc.change(&bad)
			if _, err := collectionChildLinkFor(accepted, bad); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("changed immutable receipt admitted", err)
			}
		})
	}
	links := make(collectionChildLinks)
	for n := 0; n < maxPendingCatalogOperations; n++ {
		links[fmt.Sprint(n)] = link
	}
	before := len(links)
	if _, err := prepareCollectionChildLink(links, accepted, pending); !errors.Is(err, ErrCatalogBusy) || len(links) != before {
		t.Fatal("capacity check mutated or accepted", err)
	}
	delete(links, "0")
	links[link.ChildID] = link
	if got, err := prepareCollectionChildLink(links, accepted, pending); err != nil || got != link || len(links) != before {
		t.Fatal("exact full-index retry failed", err)
	}
	other := accepted.Clone()
	other.Binding.OperationID = ledgerTestOperation(9)
	if _, err := prepareCollectionChildLink(links, other, pending); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("child reassigned across parents", err)
	}
}

func TestCollectionChildTerminalPreparationPureAndBounded(t *testing.T) {
	_, row, _ := executionLedgerRecords(t, 1, 1)
	accepted := *row.Outcome
	link, err := collectionChildLinkFor(accepted, *accepted.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ state, outcome, restore string }{{"completed", "applied", ""}, {"failed", "projection_failed", ""}, {"partial", "superseded", ""}, {"partial", "superseded", "explicit-restore"}} {
		t.Run(tc.outcome+tc.restore, func(t *testing.T) {
			receipt := *accepted.Receipt
			receipt.State, receipt.Outcome, receipt.InvalidatedByRestore, receipt.UpdatedAt = tc.state, tc.outcome, tc.restore, accepted.At.Add(time.Second)
			input := collectionChildTerminalInput{Link: link, Accepted: accepted, Terminal: receipt}
			before, _ := json.Marshal(input)
			rows, err := prepareCollectionChildTerminals([]collectionChildTerminalInput{input})
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			exact, err := rows[0].Terminal.receipt(accepted)
			if err != nil || !collectionChildReceiptsEqual(exact, receipt) {
				t.Fatal("terminal reconstruction lost original receipt", err)
			}
			raw, _ := collectionExecutionEncoding(rows[0])
			if len(raw) > collectionChildTerminalReserve || collectionExecutionCharge(rows[0], raw) != 0 {
				t.Fatal("terminal requires unreserved capacity")
			}
			retry, err := prepareCollectionChildTerminals([]collectionChildTerminalInput{input})
			if err != nil || !reflect.DeepEqual(retry, rows) {
				t.Fatal("retry changed compact terminal")
			}
			rows[0].Terminal.ChildID = "mutated"
			after, _ := json.Marshal(input)
			if !bytes.Equal(before, after) {
				t.Fatal("prepared batch aliases original identity")
			}
		})
	}
	terminal := *accepted.Receipt
	terminal.State, terminal.Outcome, terminal.UpdatedAt = "completed", "applied", accepted.At.Add(time.Second)
	input := collectionChildTerminalInput{Link: link, Accepted: accepted, Terminal: terminal}
	if rows, err := prepareCollectionChildTerminals([]collectionChildTerminalInput{input, input}); rows != nil || !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("duplicate child retained partial batch", err)
	}
	_, second, _ := executionLedgerRecords(t, 2, 1)
	secondLink, err := collectionChildLinkFor(*second.Outcome, *second.Outcome.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt := *second.Outcome.Receipt
	secondReceipt.State, secondReceipt.Outcome, secondReceipt.UpdatedAt = "completed", "applied", secondReceipt.At.Add(time.Second)
	bad := collectionChildTerminalInput{Link: secondLink, Accepted: *second.Outcome, Terminal: secondReceipt}
	bad.Terminal.Actor = "wrong"
	if rows, err := prepareCollectionChildTerminals([]collectionChildTerminalInput{input, bad}); rows != nil || err == nil {
		t.Fatal("bad later receipt retained partial preparation")
	}
	if _, err := prepareCollectionChildTerminals(make([]collectionChildTerminalInput, 257)); !errors.Is(err, errCollectionLedgerQuota) {
		t.Fatal("terminal preparation count unbounded", err)
	}
	badLink := link
	badLink.Revision = "replacement"
	if _, err := prepareCollectionChildTerminal(badLink, accepted, terminal); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("link rebound terminal", err)
	}
	if rows, err := prepareCollectionChildTerminals(nil); err != nil || len(rows) != 0 {
		t.Fatal("ordinary unlinked batch must be empty", err)
	}
}

func TestCollectionChildLinksRebuildPendingAcrossParentsAndTerminalStates(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 8<<20)
			_, a, ta := executionLedgerRecords(t, 1, 1)
			_, b, tb := executionLedgerRecords(t, 2, 1)
			if err := l.importExecutionBatch([]collectionExecutionRecord{a, b}); err != nil {
				t.Fatal(err)
			}
			i := childLinksImage(a, b)
			parent := i.Collections[a.Outcome.Binding.OperationID]
			parent.Phase = "canceled"
			i.Collections[parent.ID] = parent
			_, ordinary, _ := executionLedgerRecords(t, 8, 1)
			i.Operations[ordinary.Outcome.Receipt.ID] = *ordinary.Outcome.Receipt
			imageBefore, _ := json.Marshal(i)
			bytesBefore, _ := l.Bytes()
			v := executionLedgerView(t, l)
			links, err := buildCollectionChildLinks(context.Background(), i, v)
			if err != nil || len(links) != 2 {
				t.Fatal("canceled parent child was lost", len(links), err)
			}
			imageAfter, _ := json.Marshal(i)
			if !bytes.Equal(imageBefore, imageAfter) {
				t.Fatal("link reconstruction changed supplied image")
			}
			if err := v.Close(); err != nil {
				t.Fatal(err)
			}
			// The returned links retain no read transaction: writes may now proceed.
			executionLedgerApply(t, l, ta)
			delete(i.Operations, a.Outcome.Receipt.ID)
			restore := *tb.Terminal
			restore.State, restore.Outcome, restore.InvalidatedByRestore = "partial", "superseded", "explicit-restore"
			tb.Terminal = &restore
			executionLedgerApply(t, l, tb)
			delete(i.Operations, b.Outcome.Receipt.ID)
			parent = i.Collections[b.Outcome.Binding.OperationID]
			parent.Phase = "invalidated"
			parent.InvalidatedByRestore = "explicit-restore"
			i.Collections[parent.ID] = parent
			i.OperationEpoch = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			fresh := executionLedgerView(t, l)
			completed, err := buildCollectionChildLinks(context.Background(), i, fresh)
			if err != nil || len(completed) != 0 {
				t.Fatal("restored terminal recreated pending child", err)
			}
			if len(links) != 2 || links[a.Outcome.Receipt.ID].Revision != a.Outcome.Revision {
				t.Fatal("returned links changed with ledger")
			}
			if got, _ := l.Bytes(); got != bytesBefore {
				t.Fatal("terminal reconciliation consumed unreserved quota")
			}
			// The original build only observed the image. Compare against its saved
			// reconstruction separately from the deliberate changes above.
			initial := childLinksImage(a, b)
			p := initial.Collections[a.Outcome.Binding.OperationID]
			p.Phase = "canceled"
			initial.Collections[p.ID] = p
			initial.Operations[ordinary.Outcome.Receipt.ID] = *ordinary.Outcome.Receipt
			initialBytes, _ := json.Marshal(initial)
			if !bytes.Equal(initialBytes, imageBefore) {
				t.Fatal("initial image unexpectedly changed")
			}
			if _, err := buildCollectionChildLinks(context.Background(), initial, v); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("builder retained closed view", err)
			}
		})
	}
}

func TestCollectionChildLinksRejectMissingAndContradictoryEvidence(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, mode := range []string{"missing-ordinary", "wrong-receipt", "orphan-parent", "wrong-activation", "wrong-plan", "past-item-count", "terminal-still-pending", "reserved-child", "duplicate-child", "missing-terminal", "extra-terminal"} {
			t.Run(fmt.Sprintf("%t/%s", disk, mode), func(t *testing.T) {
				l := newLedgerTest(t, disk, 8<<20)
				_, a, terminal := executionLedgerRecords(t, 1, 1)
				i := childLinksImage(a)
				rows := []collectionExecutionRecord{a}
				switch mode {
				case "missing-ordinary", "missing-terminal":
					delete(i.Operations, a.Outcome.Receipt.ID)
				case "wrong-receipt":
					r := i.Operations[a.Outcome.Receipt.ID]
					r.Actor = "foreign"
					i.Operations[r.ID] = r
				case "orphan-parent":
					delete(i.Collections, a.Outcome.Binding.OperationID)
				case "wrong-activation":
					h := i.Collections[a.Outcome.Binding.OperationID]
					h.Activation.ID = "99999999-9999-4999-8999-999999999999"
					i.Collections[h.ID] = h
				case "wrong-plan":
					h := i.Collections[a.Outcome.Binding.OperationID]
					h.Plan.Descriptor.Digest = "different"
					i.Collections[h.ID] = h
				case "past-item-count":
					h := i.Collections[a.Outcome.Binding.OperationID]
					h.ItemCount = 0
					i.Collections[h.ID] = h
				case "terminal-still-pending":
					rows = append(rows, terminal)
				case "reserved-child":
					i.OperationReservations[a.Outcome.Receipt.ID] = OperationReservation{}
				case "duplicate-child":
					_, b, _ := executionLedgerRecords(t, 2, 1)
					b.Outcome.Receipt.ID = a.Outcome.Receipt.ID
					rows = append(rows, b)
					other := childLinksImage(b)
					i.Collections[b.Outcome.Binding.OperationID] = other.Collections[b.Outcome.Binding.OperationID]
				case "extra-terminal":
					rows = append(rows, terminal)
					delete(i.Operations, a.Outcome.Receipt.ID)
				}
				if err := l.importExecutionBatch(rows); err != nil {
					t.Fatal(err)
				}
				if mode == "missing-terminal" { // A child lost after ordinary completion is indistinguishable from corruption until terminal evidence is committed.
					// No terminal is added intentionally.
				}
				if mode == "extra-terminal" {
					v := executionLedgerView(t, l)
					_ = v.Close()
					if disk {
						if err := l.db.Update(func(tx *bolt.Tx) error {
							return tx.Bucket(collectionLedgerExecution).Bucket([]byte(a.Outcome.Binding.OperationID)).Delete([]byte(collectionExecutionOutcomeSlot(1)))
						}); err != nil {
							t.Fatal(err)
						}
					} else {
						delete(l.executionRows[a.Outcome.Binding.OperationID], collectionExecutionOutcomeSlot(1))
					}
				}
				v := executionLedgerView(t, l)
				before, _ := json.Marshal(i)
				if links, err := buildCollectionChildLinks(context.Background(), i, v); links != nil || !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("invalid evidence produced pending links", links, err)
				}
				after, _ := json.Marshal(i)
				if !bytes.Equal(before, after) {
					t.Fatal("failed reconstruction mutated image")
				}
			})
		}
	}
}

func TestCollectionChildLinksBoundPendingNotHistoricalRows(t *testing.T) {
	l := newLedgerTest(t, false, 32<<20)
	i := image{Collections: make(map[string]CollectionState), Operations: make(map[string]OperationReceipt)}
	var batch []collectionExecutionRecord
	var terminals []collectionExecutionRecord
	for ordinal := uint64(1); ordinal <= maxPendingCatalogOperations+1; ordinal++ {
		_, o, terminal := executionLedgerRecords(t, 1, ordinal)
		batch = append(batch, o)
		terminals = append(terminals, terminal)
		if len(batch) == 256 {
			if err := l.importExecutionBatch(batch); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if err := l.importExecutionBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < len(terminals); offset += 256 {
		if err := l.ApplyExecutionBatch(terminals[offset:min(offset+256, len(terminals))]); err != nil {
			t.Fatal(err)
		}
	}
	b := terminals[0].Terminal.Binding
	model := childLinksImage(collectionExecutionRecord{Version: 1, Terminal: terminals[0].Terminal})
	i.Collections[b.OperationID] = model.Collections[b.OperationID]
	if links, err := buildCollectionChildLinks(context.Background(), i, executionLedgerView(t, l)); err != nil || len(links) != 0 {
		t.Fatal("historical completions exhausted pending index", err)
	}
}

func TestCollectionChildLinksCancellationWaitsAndNoRetainedView(t *testing.T) {
	l := newLedgerTest(t, false, 1<<20)
	_, o, _ := executionLedgerRecords(t, 1, 1)
	if err := l.importExecutionBatch([]collectionExecutionRecord{o}); err != nil {
		t.Fatal(err)
	}
	i := childLinksImage(o)
	v := executionLedgerView(t, l)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if links, err := buildCollectionChildLinks(ctx, i, v); links != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled builder", err)
	}
	v.mu.Lock()
	base, stop := context.WithCancel(context.Background())
	waiting := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := buildCollectionChildLinks(waiting, i, v); done <- err }()
	select {
	case <-waiting.waiting:
	case <-time.After(time.Second):
		v.mu.Unlock()
		stop()
		t.Fatal("builder did not wait for held lock")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("canceled lock wait stuck")
	}
	v.mu.Unlock()
}
