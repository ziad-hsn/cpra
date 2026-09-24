package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func executionStreamFixture(t *testing.T, parent, ordinal uint64) (collectionExecutionRecord, collectionExecutionRecord, collectionExecutionRecord) {
	t.Helper()
	b := CollectionExecutionBinding{OperationID: ledgerTestOperation(parent), UploadID: "22222222-2222-4222-8222-222222222222",
		ActivationID: "33333333-3333-4333-8333-333333333333", PlanID: "44444444-4444-4444-8444-444444444444", PlanDigest: strings.Repeat("a", 64)}
	p := CollectionPreparedItem{Binding: b, ID: "55555555-5555-4555-8555-555555555555", Ordinal: ordinal, InputOrdinal: ordinal,
		RowDigest: strings.Repeat("b", 64), At: catalogDeltaAt, Record: catalogDeltaRecord("Monitor", fmt.Sprintf("monitor-%d-%d", parent, ordinal))}
	r := OperationReceipt{ID: ledgerTestOperation(parent*10_000 + ordinal), Key: p.Record.Key, UID: p.Record.UID,
		NewVersion: p.Record.Revision, Generation: p.Record.Generation, CommittedIndex: 100 + ordinal, Actor: "operator",
		At: p.At, UpdatedAt: p.At, State: "committed", Outcome: "committed"}
	o := CollectionItemOutcome{Binding: b, Ordinal: ordinal, InputOrdinal: ordinal, RowDigest: p.RowDigest, Key: p.Record.Key,
		Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: ordinal, Decision: "accepted", PreparedID: p.ID,
		UID: r.UID, Revision: r.NewVersion, Generation: r.Generation, MutationSequence: 200 + ordinal, Receipt: &r, CommittedIndex: r.CommittedIndex, At: p.At}
	completed := r
	completed.State, completed.Outcome, completed.UpdatedAt = "completed", "applied", completed.At.Add(time.Second)
	terminal, err := collectionChildObservationFor(o, completed)
	if err != nil {
		t.Fatal(err)
	}
	return collectionExecutionRecord{Version: 1, Prepared: &p}, collectionExecutionRecord{Version: 1, Outcome: &o}, collectionExecutionRecord{Version: 1, Terminal: &terminal}
}

func executionStreamRaw(t *testing.T, records []collectionExecutionRecord) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(collectionExecutionLedgerMagic)
	digest := sha256.New()
	var encoded, charged uint64
	for _, record := range records {
		raw, err := collectionExecutionEncoding(record)
		if err != nil {
			t.Fatal(err)
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
		b.Write(size[:])
		b.Write(raw)
		digest.Write(size[:])
		digest.Write(raw)
		encoded += uint64(len(raw))
		charged += uint64(collectionExecutionCharge(record, raw))
	}
	var footer [4 + 8 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], uint64(len(records)))
	binary.BigEndian.PutUint64(footer[12:20], encoded)
	binary.BigEndian.PutUint64(footer[20:28], charged)
	copy(footer[28:], digest.Sum(nil))
	b.Write(footer[:])
	return b.Bytes()
}

func executionStreamRecords(t *testing.T, l *collectionLedger) []collectionExecutionRecord {
	t.Helper()
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var records []collectionExecutionRecord
	if err := v.WalkExecution(context.Background(), func(r collectionExecutionRecord) error { records = append(records, r); return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCollectionExecutionStreamFrozenAndNamespaceAccounting(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			source := newLedgerTest(t, disk, 8<<20)
			p, outcome, terminal := executionStreamFixture(t, 1, 1)
			if err := source.ApplyExecutionBatch([]collectionExecutionRecord{p}); err != nil {
				t.Fatal(err)
			}
			if err := source.ApplyExecutionBatch([]collectionExecutionRecord{outcome}); err != nil {
				t.Fatal(err)
			}
			v, err := source.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if err := source.ApplyExecutionBatch([]collectionExecutionRecord{terminal}); err != nil {
				t.Fatal(err)
			}
			var frozen bytes.Buffer
			if n, err := writeCollectionExecutionLedger(&frozen, v); err != nil || n != int64(frozen.Len()) {
				t.Fatal(n, err)
			}
			if !bytes.Equal(frozen.Bytes(), executionStreamRaw(t, []collectionExecutionRecord{outcome})) {
				t.Fatal("frozen execution stream observed a later terminal")
			}
			target := newLedgerTest(t, disk, 8<<20)
			input := ledgerTestItem(1)
			if err := target.Append(ledgerTestOperation(1), input); err != nil {
				t.Fatal(err)
			}
			beforeInput, _, _ := validationLedgerStreams(t, target)
			trailing := []byte("next-snapshot-field")
			r := bytes.NewReader(append(bytes.Clone(frozen.Bytes()), trailing...))
			if err := importCollectionExecutionLedger(r, target); err != nil {
				t.Fatal(err)
			}
			if got, _ := io.ReadAll(r); !bytes.Equal(got, trailing) {
				t.Fatal("import consumed following snapshot namespace")
			}
			if got := executionStreamRecords(t, target); !reflect.DeepEqual(got, []collectionExecutionRecord{outcome}) {
				t.Fatal("snapshot resurrected prepared data or lost original outcome")
			}
			afterInput, _, _ := validationLedgerStreams(t, target)
			if !bytes.Equal(beforeInput, afterInput) {
				t.Fatal("charged terminal capacity changed input snapshot accounting")
			}
			if err := importCollectionExecutionLedger(bytes.NewReader(frozen.Bytes()), target); !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("accepted merge into existing execution state", err)
			}
			if err := target.ApplyExecutionBatch([]collectionExecutionRecord{terminal}); err != nil {
				t.Fatal("restored acceptance lost reserved completion capacity", err)
			}
			if stats, err := target.ExecutionStats(ledgerTestOperation(1)); err != nil || stats.Outcomes != 1 || stats.Terminals != 1 || stats.TerminalCapacity != collectionChildTerminalReserve {
				t.Fatal(stats, err)
			}
		})
	}
}

func TestCollectionExecutionStreamRejectsCorruptionAndUnlinkedTerminal(t *testing.T) {
	_, outcome, terminal := executionStreamFixture(t, 1, 1)
	valid := executionStreamRaw(t, []collectionExecutionRecord{outcome, terminal})
	changedTerminal := *terminal.Terminal
	changedTerminal.ChildID = ledgerTestOperation(90000)
	badLink := collectionExecutionRecord{Version: 1, Terminal: &changedTerminal}
	cases := map[string][]byte{
		"truncated": valid[:len(valid)-1], "absent": {}, "orphan-terminal": executionStreamRaw(t, []collectionExecutionRecord{terminal}),
		"wrong-child":    executionStreamRaw(t, []collectionExecutionRecord{outcome, badLink}),
		"duplicate-slot": executionStreamRaw(t, []collectionExecutionRecord{outcome, outcome}),
		"reordered":      executionStreamRaw(t, []collectionExecutionRecord{terminal, outcome}),
	}
	for _, field := range []struct {
		name   string
		offset int
	}{{"count", 49}, {"raw-bytes", 41}, {"charged-bytes", 33}, {"digest", 1}} {
		b := bytes.Clone(valid)
		b[len(b)-field.offset] ^= 1
		cases[field.name] = b
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			target := newLedgerTest(t, false, 8<<20)
			if err := importCollectionExecutionLedger(bytes.NewReader(data), target); err == nil {
				t.Fatal("accepted invalid execution stream")
			}
		})
	}
	empty := executionStreamRaw(t, nil)
	if err := importCollectionExecutionLedger(bytes.NewReader(empty), newLedgerTest(t, false, 8<<20)); err != nil {
		t.Fatal("mandatory empty namespace rejected", err)
	}
	tooSmall := newLedgerTest(t, false, 1)
	if err := importCollectionExecutionLedger(bytes.NewReader(valid), tooSmall); !errors.Is(err, errCollectionLedgerQuota) {
		t.Fatal("terminal capacity escaped restored shared quota", err)
	}
}

func TestCollectionExecutionStreamBoundedImportTransactions(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprintf("large=%t", large), func(t *testing.T) {
			var records []collectionExecutionRecord
			if large {
				for parent := uint64(1); parent <= 3; parent++ {
					p, _, _ := executionStreamFixture(t, parent, 1)
					p.Prepared.Record.Payload.Ciphertext = bytes.Repeat([]byte{0x80}, secureconfig.MaxPlaintext+16)
					records = append(records, p)
				}
			} else {
				for ordinal := uint64(1); ordinal <= 300; ordinal++ {
					_, o, _ := executionStreamFixture(t, 1, ordinal)
					records = append(records, o)
				}
			}
			sort.Slice(records, func(i, j int) bool {
				ai, bi, _ := records[i].identity()
				aj, bj, _ := records[j].identity()
				return ai < aj || ai == aj && bi < bj
			})
			raw := executionStreamRaw(t, records)
			if large && len(raw) <= 4<<20 {
				t.Fatal("fixture does not cross byte transaction bound")
			}
			target := newLedgerTest(t, true, 16<<20)
			before := ledgerTestTransactionID(t, target)
			if err := importCollectionExecutionLedger(bytes.NewReader(raw), target); err != nil {
				t.Fatal(err)
			}
			if got := ledgerTestTransactionID(t, target) - before; got != 2 {
				t.Fatalf("import transaction count %d, want2", got)
			}
			if got := executionStreamRecords(t, target); !reflect.DeepEqual(got, records) {
				t.Fatal("bounded import changed logical rows")
			}
		})
	}
}

func TestCollectionExecutionStreamCannotBeOmittedByOlderSnapshotFormats(t *testing.T) {
	for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion,
		CollectionValidationFormatVersion, CollectionValidationRequestFormatVersion, CollectionActivationFormatVersion} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			l := newLedgerTest(t, false, 8<<20)
			p, _, _ := executionStreamFixture(t, 1, 1)
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{p}); err != nil {
				t.Fatal(err)
			}
			v, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			snapshot := frozenSnapshot{image: image{Version: version, Index: 100}, collections: v}
			sink := &collectionTestSink{}
			if err := snapshot.persistCollections(sink); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatalf("format %d silently omitted execution rows: %v", version, err)
			}
			if sink.Len() != 0 {
				t.Fatal("wrote an incomplete older snapshot")
			}
		})
	}
}
