package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func executionLedgerRecords(t *testing.T, parent, ordinal uint64) (collectionExecutionRecord, collectionExecutionRecord, collectionExecutionRecord) {
	t.Helper()
	p, o := executionRecordFixture(t)
	p.Binding.OperationID = ledgerTestOperation(parent)
	p.Ordinal = ordinal
	p.InputOrdinal = ordinal + 17
	p.ID = fmt.Sprintf("55555555-5555-4555-8555-%012d", ordinal)
	p.Record.Revision = fmt.Sprintf("revision-%d", ordinal)
	o.Binding = p.Binding
	o.Ordinal = ordinal
	o.InputOrdinal = p.InputOrdinal
	o.PreparedID = p.ID
	o.Revision = p.Record.Revision
	o.Receipt.ID = ledgerTestOperation(parent*10000 + ordinal)
	o.Receipt.NewVersion = o.Revision
	terminal := executionRecordTerminal(t, o, "completed", "applied", "")
	prepared := collectionExecutionRecord{Version: 1, Prepared: &p}
	accepted := collectionExecutionRecord{Version: 1, Outcome: &o}
	done := collectionExecutionRecord{Version: 1, Terminal: &terminal}
	for _, r := range []collectionExecutionRecord{prepared, accepted, done} {
		if _, err := collectionExecutionEncoding(r); err != nil {
			t.Fatal(err)
		}
	}
	return prepared, accepted, done
}
func executionLedgerCharge(t *testing.T, records ...collectionExecutionRecord) int64 {
	t.Helper()
	var total int64
	for _, r := range records {
		raw, err := collectionExecutionEncoding(r)
		if err != nil {
			t.Fatal(err)
		}
		total += collectionExecutionCharge(r, raw)
	}
	return total
}
func executionLedgerApply(t *testing.T, l *collectionLedger, records ...collectionExecutionRecord) {
	t.Helper()
	if err := l.ApplyExecutionBatch(records); err != nil {
		t.Fatal(err)
	}
}
func executionLedgerView(t *testing.T, l *collectionLedger) *collectionLedgerView {
	t.Helper()
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func TestCollectionExecutionLedgerReservedCompletionAtQuotaAndFrozenReads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			p, o, terminal := executionLedgerRecords(t, 1, 1)
			op := p.Prepared.Binding.OperationID
			input := ledgerTestItem(1)
			inputCost, _ := collectionItemCost(op, input)
			quota := executionLedgerCharge(t, o) + inputCost
			l := newLedgerTest(t, disk, quota)
			if err := l.Append(op, input); err != nil {
				t.Fatal(err)
			}
			beforeInput, beforePlan, beforeValidation := validationLedgerStreams(t, l)
			executionLedgerApply(t, l, p)
			executionLedgerApply(t, l, p)
			frozen := executionLedgerView(t, l)
			original, _ := l.ExecutionStats(op)
			changed := *p.Prepared
			p.Prepared = &changed
			changed.Record = changed.Record.Clone()
			changed.Record.Payload.Ciphertext[0] ^= 1
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{p}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("prepared replacement", err)
			}
			p, _, _ = executionLedgerRecords(t, 1, 1)
			executionLedgerApply(t, l, o)
			executionLedgerApply(t, l, o)
			if used, _ := l.Bytes(); used != quota {
				t.Fatal("acceptance did not reserve full slot", used, quota)
			}
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{p}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("consumed preparation recreated", err)
			}
			if err := l.Append(op, ledgerTestItem(2)); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("fixture not at shared quota", err)
			}
			executionLedgerApply(t, l, terminal)
			executionLedgerApply(t, l, terminal)
			if used, _ := l.Bytes(); used != quota {
				t.Fatal("terminal charged or refunded permanent capacity", used)
			}
			got, err := l.ExecutionStats(op)
			rawTerminal, _ := collectionExecutionEncoding(terminal)
			if err != nil || got.Prepared || got.Outcomes != 1 || got.Terminals != 1 || got.TerminalCapacity != 4096 || got.TerminalBytes != int64(len(rawTerminal)) || got.ChargedBytes != quota-inputCost {
				t.Fatal("stats", got, err)
			}
			prior, err := frozen.ExecutionStats(op)
			if err != nil || prior != original {
				t.Fatal("frozen state changed", err)
			}
			old, exists, err := frozen.ExecutionRecord(op, "prepared")
			if err != nil || !exists || !reflect.DeepEqual(old, p) {
				t.Fatal("snapshot lost consumed preparation", err)
			}
			old.Prepared.Record.Payload.Ciphertext[0] ^= 1
			fresh, _, _ := frozen.ExecutionRecord(op, "prepared")
			if !reflect.DeepEqual(fresh, p) {
				t.Fatal("read aliases frozen ciphertext")
			}
			contradictory := terminal
			copy := *terminal.Terminal
			contradictory.Terminal = &copy
			copy.State, copy.Outcome = "failed", "projection_failed"
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{contradictory}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("terminal rewritten", err)
			}
			afterInput, afterPlan, afterValidation := validationLedgerStreams(t, l)
			if !bytes.Equal(beforeInput, afterInput) || !bytes.Equal(beforePlan, afterPlan) || !bytes.Equal(beforeValidation, afterValidation) {
				t.Fatal("execution charge changed older namespace bytes")
			}
		})
	}
}

func TestCollectionExecutionLedgerCrossParentAtomicConsumption(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			pa, oa, ta := executionLedgerRecords(t, 1, 1)
			pb, ob, _ := executionLedgerRecords(t, 2, 1)
			l := newLedgerTest(t, disk, 1<<20)
			executionLedgerApply(t, l, pa)
			executionLedgerApply(t, l, oa)
			executionLedgerApply(t, l, pb)
			before, _ := l.Bytes()
			aBefore, _ := l.ExecutionStats(pa.Prepared.Binding.OperationID)
			bBefore, _ := l.ExecutionStats(pb.Prepared.Binding.OperationID)
			wrong := ta
			copy := *ta.Terminal
			wrong.Terminal = &copy
			copy.ChildID = ledgerTestOperation(999)
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{ob, wrong}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("bad second parent admitted", err)
			}
			a, _ := l.ExecutionStats(pa.Prepared.Binding.OperationID)
			b, _ := l.ExecutionStats(pb.Prepared.Binding.OperationID)
			used, _ := l.Bytes()
			if a != aBefore || b != bBefore || used != before {
				t.Fatal("failed batch retained consumption/outcome/counters")
			}
			executionLedgerApply(t, l, ob, ta)
			a, _ = l.ExecutionStats(pa.Prepared.Binding.OperationID)
			b, _ = l.ExecutionStats(pb.Prepared.Binding.OperationID)
			if a.Terminals != 1 || b.Prepared || b.Outcomes != 1 {
				t.Fatal("cross-parent batch incomplete")
			}
			executionLedgerApply(t, l, ob, ta)
			foreign, _, _ := executionLedgerRecords(t, 2, 2)
			foreign.Prepared.Binding.ActivationID = "99999999-9999-4999-8999-999999999999"
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{foreign}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("parent rebound", err)
			}
		})
	}
}

func TestCollectionExecutionLedgerQuotaBoundsAndMissingPrepared(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			p, o, _ := executionLedgerRecords(t, 1, 1)
			op := p.Prepared.Binding.OperationID
			l := newLedgerTest(t, disk, executionLedgerCharge(t, o)-1)
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{o}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("acceptance lacked preparation", err)
			}
			executionLedgerApply(t, l, p)
			before, _ := l.ExecutionStats(op)
			bytesBefore, _ := l.Bytes()
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{o}); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("lower quota", err)
			}
			after, _ := l.ExecutionStats(op)
			bytesAfter, _ := l.Bytes()
			if after != before || bytesAfter != bytesBefore {
				t.Fatal("quota discarded original preparation")
			}
			rejected := o.Outcome.Clone()
			rejected.Decision = "conflict"
			rejected.Receipt = nil
			rejected.UID = ""
			rejected.Revision = ""
			rejected.Generation = 0
			rejected.MutationSequence = 0
			executionLedgerApply(t, l, collectionExecutionRecord{Version: 1, Outcome: &rejected})
			after, _ = l.ExecutionStats(op)
			if after.Prepared || after.Outcomes != 1 || after.TerminalCapacity != 0 {
				t.Fatal("rejection did not retire prepared slot", after)
			}
			oversized := make([]collectionExecutionRecord, 257)
			for i := range oversized {
				oversized[i] = o
			}
			if err := l.ApplyExecutionBatch(oversized); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("batch count unbounded", err)
			}
			if err := l.ApplyExecutionBatch(nil); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("empty batch", err)
			}
		})
	}
}

func TestCollectionExecutionLedgerImportSparsePagingAndStream(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 32<<20)
			op := ledgerTestOperation(1)
			var records []collectionExecutionRecord
			for ordinal := uint64(1); ordinal <= 600; ordinal++ {
				_, o, _ := executionLedgerRecords(t, 1, ordinal)
				o.Outcome.UID = strings.Repeat("<", 256)
				o.Outcome.Revision = strings.Repeat(">", 256)
				o.Outcome.OldVersion = strings.Repeat("&", 256)
				o.Outcome.Receipt.UID = o.Outcome.UID
				o.Outcome.Receipt.NewVersion = o.Outcome.Revision
				o.Outcome.Receipt.OldVersion = o.Outcome.OldVersion
				records = append(records, o)
			}
			for start := 0; start < len(records); start += 256 {
				if err := l.importExecutionBatch(records[start:min(start+256, len(records))]); err != nil {
					t.Fatal(err)
				}
			}
			for _, ordinal := range []uint64{600, 1, 255} {
				terminal := executionRecordTerminal(t, *records[ordinal-1].Outcome, "completed", "applied", "")
				executionLedgerApply(t, l, collectionExecutionRecord{Version: 1, Terminal: &terminal})
			}
			v := executionLedgerView(t, l)
			after := ""
			count := 0
			short := false
			for {
				page, err := v.ExecutionPage(op, after, 500)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				var encoded int
				for _, r := range page {
					raw, _ := collectionExecutionEncoding(r)
					encoded += len(raw)
					_, after, _ = r.identity()
				}
				if len(page) > 500 || encoded > 4<<20 {
					t.Fatal("unbounded page")
				}
				if len(page) < 500 && count == 0 {
					short = true
				}
				count += len(page)
			}
			if count != 603 || !short {
				t.Fatal("paging skipped/duplicated entries or did not exercise byte ceiling", count, short)
			}
			var exported []collectionExecutionRecord
			if err := v.WalkExecution(context.Background(), func(r collectionExecutionRecord) error { exported = append(exported, r); return nil }); err != nil || len(exported) != 603 {
				t.Fatal("walk", len(exported), err)
			}
			restored := newLedgerTest(t, disk, 32<<20)
			for start := 0; start < len(exported); start += 256 {
				if err := restored.importExecutionBatch(exported[start:min(start+256, len(exported))]); err != nil {
					t.Fatal(err)
				}
			}
			a, _ := l.ExecutionStats(op)
			b, _ := restored.ExecutionStats(op)
			if a != b {
				t.Fatal("import changed reservation or raw accounting")
			}
			ctx, cancel := context.WithCancel(context.Background())
			visits := 0
			if err := v.WalkExecution(ctx, func(collectionExecutionRecord) error { visits++; cancel(); return nil }); !errors.Is(err, context.Canceled) || visits != 1 {
				t.Fatal("walk cancellation", err)
			}
		})
	}
}

func TestCollectionExecutionLedgerRejectsCorruptionAndClosed(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, mode := range []string{"record", "oversize", "counter", "missing-outcome", "terminal-binding"} {
			t.Run(fmt.Sprintf("%t/%s", disk, mode), func(t *testing.T) {
				l := newLedgerTest(t, disk, 1<<20)
				p, o, terminal := executionLedgerRecords(t, 1, 1)
				op := p.Prepared.Binding.OperationID
				executionLedgerApply(t, l, p)
				executionLedgerApply(t, l, o, terminal)
				slot := collectionExecutionOutcomeSlot(1)
				raw := []byte("corrupt")
				if mode == "oversize" {
					raw = bytes.Repeat([]byte("x"), collectionExecutionMaxFrame+1)
				}
				if mode == "terminal-binding" {
					terminal.Terminal.ChildID = ledgerTestOperation(333)
					raw, _ = collectionExecutionEncoding(terminal)
					_, slot, _ = terminal.identity()
				}
				if disk {
					if err := l.db.Update(func(tx *bolt.Tx) error {
						rows := tx.Bucket(collectionLedgerExecution).Bucket([]byte(op))
						switch mode {
						case "counter":
							return tx.Bucket(collectionLedgerExecutionMeta).Put([]byte(op), []byte("{}"))
						case "missing-outcome":
							return rows.Delete([]byte(slot))
						default:
							return rows.Put([]byte(slot), raw)
						}
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					switch mode {
					case "counter":
						stat := l.executionStats[op]
						stat.ChargedBytes++
						l.executionStats[op] = stat
					case "missing-outcome":
						delete(l.executionRows[op], slot)
					default:
						l.executionRows[op][slot] = raw
					}
				}
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				if err := v.WalkExecution(context.Background(), func(collectionExecutionRecord) error { return nil }); !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("corruption exported", err)
				}
				if err := v.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := v.ExecutionPage(op, "", 100); !errors.Is(err, errCollectionLedgerClosed) {
					t.Fatal("closed read", err)
				}
			})
		}
	}
}

func TestCollectionExecutionLedgerRealWriteFailureAtomic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file-descriptor failure is a Unix fixture")
	}
	l := newLedgerTest(t, true, 1<<20)
	pa, oa, ta := executionLedgerRecords(t, 1, 1)
	pb, ob, _ := executionLedgerRecords(t, 2, 1)
	executionLedgerApply(t, l, pa)
	executionLedgerApply(t, l, oa)
	executionLedgerApply(t, l, pb)
	before, _ := l.Bytes()
	aBefore, _ := l.ExecutionStats(pa.Prepared.Binding.OperationID)
	bBefore, _ := l.ExecutionStats(pb.Prepared.Binding.OperationID)
	path := l.db.Path()
	if err := l.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
	if err != nil {
		t.Fatal(err)
	}
	l.db = db
	err = l.ApplyExecutionBatch([]collectionExecutionRecord{ob, ta})
	var pathErr *os.PathError
	if err == nil || !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatal("expected real OS write rejection", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	l.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := l.ExecutionStats(pa.Prepared.Binding.OperationID)
	b, _ := l.ExecutionStats(pb.Prepared.Binding.OperationID)
	used, _ := l.Bytes()
	if used != before || a != aBefore || b != bBefore {
		t.Fatal("failed disk commit consumed preparation or partial outcome/terminal")
	}
}

func TestCollectionExecutionLedgerInteriorOutcomeCorruption(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 1<<20)
			op := ledgerTestOperation(1)
			for ordinal := uint64(1); ordinal <= 3; ordinal++ {
				p, o, _ := executionLedgerRecords(t, 1, ordinal)
				executionLedgerApply(t, l, p)
				executionLedgerApply(t, l, o)
			}
			slot := collectionExecutionOutcomeSlot(2)
			if disk {
				if err := l.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerExecution).Bucket([]byte(op)).Delete([]byte(slot))
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				delete(l.executionRows[op], slot)
			}
			if _, found, err := l.ExecutionRecord(op, slot); !errors.Is(err, errCollectionLedgerCorrupt) || found {
				t.Fatal("missing expected interior outcome reported absence", found, err)
			}
			for _, cursor := range []string{"", collectionExecutionOutcomeSlot(1)} {
				if _, err := l.ExecutionPage(op, cursor, 100); !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("page skipped interior outcome", cursor, err)
				}
			}
		})
	}
}

func TestCollectionExecutionLedgerBatchBytesAndPendingIdentity(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 8<<20)
			var large []collectionExecutionRecord
			for parent := uint64(1); parent <= 3; parent++ {
				p, _, _ := executionLedgerRecords(t, parent, 1)
				p.Prepared.Record.Payload.Ciphertext = bytes.Repeat([]byte{7}, secureconfig.MaxPlaintext+16)
				large = append(large, p)
			}
			if err := l.ApplyExecutionBatch(large); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("encoded batch ceiling", err)
			}
			if used, _ := l.Bytes(); used != 0 {
				t.Fatal("oversized batch partially applied")
			}
			p, o, _ := executionLedgerRecords(t, 1, 1)
			executionLedgerApply(t, l, p)
			unchanged := o.Outcome.Clone()
			unchanged.Decision = "unchanged"
			unchanged.PreparedID = ""
			unchanged.Receipt = nil
			unchanged.MutationSequence = 0
			unchanged.OldVersion = unchanged.Revision
			if err := l.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Outcome: &unchanged}}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("unrelated no-write outcome consumed prepared identity", err)
			}
			empty := newLedgerTest(t, disk, 1<<20)
			executionLedgerApply(t, empty, collectionExecutionRecord{Version: 1, Outcome: &unchanged})
			if _, exists, err := empty.ExecutionRecord(o.Outcome.Binding.OperationID, "terminal/0000000000000001"); err != nil || exists {
				t.Fatal("legitimately absent terminal", exists, err)
			}
			if _, exists, err := empty.ExecutionRecord(o.Outcome.Binding.OperationID, collectionExecutionOutcomeSlot(2)); err != nil || exists {
				t.Fatal("future outcome absence", exists, err)
			}
		})
	}
}

func TestCollectionExecutionLedgerConcurrentMemoryFreezeAndWrites(t *testing.T) {
	l := newLedgerTest(t, false, 8<<20)
	op := ledgerTestOperation(1)
	records := make([][]collectionExecutionRecord, 100)
	for i := range records {
		p, o, terminal := executionLedgerRecords(t, 1, uint64(i+1))
		records[i] = []collectionExecutionRecord{p, o, terminal}
	}
	start := make(chan struct{})
	failures := make(chan error, 5)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for _, set := range records {
			for _, r := range set {
				if err := l.ApplyExecutionBatch([]collectionExecutionRecord{r}); err != nil {
					failures <- err
					return
				}
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 100 {
				v, err := l.Freeze()
				if err != nil {
					failures <- err
					return
				}
				before, err := v.ExecutionStats(op)
				if err == nil {
					err = v.WalkExecution(context.Background(), func(collectionExecutionRecord) error { return nil })
				}
				after, readErr := v.ExecutionStats(op)
				closeErr := v.Close()
				if err != nil {
					failures <- err
					return
				}
				if readErr != nil {
					failures <- readErr
					return
				}
				if closeErr != nil {
					failures <- closeErr
					return
				}
				if before != after {
					failures <- errors.New("frozen index changed while source wrote")
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	stat, err := l.ExecutionStats(op)
	if err != nil || stat.Outcomes != 100 || stat.Terminals != 100 || stat.Prepared {
		t.Fatal("concurrent source lost rows", stat, err)
	}
}

func TestCollectionExecutionLedgerStatsRequireEachTerminalReservation(t *testing.T) {
	l := newLedgerTest(t, false, 1<<20)
	p, o, terminal := executionLedgerRecords(t, 1, 1)
	op := o.Outcome.Binding.OperationID
	executionLedgerApply(t, l, p)
	executionLedgerApply(t, l, o, terminal)
	s := l.executionStats[op]
	s.Terminals = 2
	s.Outcomes = 2
	if err := s.validate(op); !errors.Is(err, errCollectionLedgerCorrupt) {
		t.Fatal("two terminals share one reserved slot", err)
	}
}
