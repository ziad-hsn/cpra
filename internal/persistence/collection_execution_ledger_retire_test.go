package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
	bolt "go.etcd.io/bbolt"
)

func executionRetirementRecords(t *testing.T, initial CollectionExecutionProgress, candidates []CollectionPreparedItem, outcomes []CollectionItemOutcome, terminals []*CollectionChildObservation, prepared *CollectionPreparedItem) (CollectionExecutionProgress, []collectionExecutionRecord) {
	t.Helper()
	final := retirementCheckpointFinal(t, initial, candidates, outcomes, terminals)
	rows := make([]collectionExecutionRecord, 0, len(outcomes)*2+1)
	for i := range outcomes {
		rows = append(rows, collectionExecutionRecord{Version: 1, Outcome: &outcomes[i]})
	}
	if prepared != nil {
		var err error
		final, err = final.withPrepared(*prepared)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, collectionExecutionRecord{Version: 1, Prepared: prepared})
	}
	for _, terminal := range terminals {
		if terminal != nil {
			rows = append(rows, collectionExecutionRecord{Version: 1, Terminal: terminal})
		}
	}
	return final, rows
}

func executionRetirementImport(t *testing.T, ledger *collectionLedger, rows []collectionExecutionRecord) {
	t.Helper()
	for len(rows) > 0 {
		count, size := 0, 0
		for count < len(rows) && count < collectionLedgerBatchLimit {
			raw, err := collectionExecutionEncoding(rows[count])
			if err != nil {
				t.Fatal(err)
			}
			if size+len(raw) > collectionLedgerBatchBytes {
				break
			}
			size += len(raw)
			count++
		}
		if count == 0 {
			t.Fatal("fixture record cannot fit one batch")
		}
		if err := ledger.importExecutionBatch(rows[:count]); err != nil {
			t.Fatal(err)
		}
		rows = rows[count:]
	}
}

func executionRetirementStream(t *testing.T, view *collectionLedgerView) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := writeCollectionExecutionLedger(&out, view); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCollectionExecutionLedgerRetirePairedQuotaFrozenAndSuffixImport(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 601)
			prepared := candidates[600]
			prepared.At = initial.StartedAt.Add(2000 * time.Second)
			final, rows := executionRetirementRecords(t, initial, candidates[:600], outcomes[:600], terminals[:600], &prepared)
			ledger := newLedgerTest(t, disk, 16<<20)
			op := final.Binding.OperationID
			input := ledgerTestItem(1)
			if err := ledger.Append(op, input); err != nil {
				t.Fatal(err)
			}
			base, _ := ledger.Bytes()
			executionRetirementImport(t, ledger, rows)
			frozen := executionLedgerView(t, ledger)
			original := executionRetirementStream(t, frozen)
			inputBefore, planBefore, validationBefore := validationLedgerStreams(t, ledger)
			ledger.maxBytes, _ = ledger.Bytes()
			if err := ledger.Append(op, ledgerTestItem(2)); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("fixture not exactly at shared quota", err)
			}
			checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
			var removed bool
			var totalRecords int
			var refunded, rawRemoved int64
			for batch := 0; ; batch++ {
				prior := checkpoint.Clone()
				before, err := ledger.ExecutionStats(op)
				if err != nil {
					t.Fatal(err)
				}
				r, err := ledger.RetireExecutionPrefix(checkpoint, final, removed)
				if err != nil || r.Records < 1 || r.Records > 256 || r.EncodedBytes < 1 || r.EncodedBytes > 4<<20 || r.Checkpoint.Progress.Processed < checkpoint.Progress.Processed {
					t.Fatal("bounded retirement", batch, r, err)
				}
				if r.PreparedRemoved && r.Checkpoint.Progress.Processed != final.Processed {
					t.Fatal("prepared slot removed before decided prefix")
				}
				checkpoint, removed = r.Checkpoint, r.PreparedRemoved
				totalRecords += r.Records
				refunded += r.ChargedBytes
				rawRemoved += r.EncodedBytes
				want, err := collectionExecutionRetirementStats(checkpoint, final, removed)
				actual, statErr := ledger.ExecutionStats(op)
				used, usedErr := ledger.Bytes()
				if err != nil || statErr != nil || usedErr != nil || actual != want || used != base+final.ChargedBytes-refunded ||
					before.EncodedBytes-actual.EncodedBytes != r.EncodedBytes || before.ChargedBytes-actual.ChargedBytes != r.ChargedBytes {
					t.Fatal("exact live quota accounting", batch, err, statErr, usedErr, actual, want)
				}
				if !bytes.Equal(original, executionRetirementStream(t, frozen)) {
					t.Fatal("retirement changed frozen native/memory reader")
				}
				if _, found, err := ledger.ExecutionRecord(op, collectionExecutionOutcomeSlot(checkpoint.Progress.Processed)); err != nil || found {
					t.Fatal("retired outcome remained visible", err)
				}
				for ordinal := prior.Progress.Processed + 1; ordinal <= checkpoint.Progress.Processed; ordinal++ {
					if _, found, err := ledger.ExecutionRecord(op, collectionExecutionTerminalSlot(ordinal)); err != nil || found {
						t.Fatal("retired accepted terminal remained or orphaned", ordinal, err)
					}
				}
				if r.Complete {
					if actual != (collectionExecutionStats{}) || !removed || !checkpoint.matchesFinal(final) || totalRecords != len(rows) || rawRemoved != final.EncodedBytes || refunded != final.ChargedBytes {
						t.Fatal("final deletion did not release exact original rows and quotas")
					}
					retry, err := ledger.RetireExecutionPrefix(checkpoint, final, removed)
					if err != nil || !retry.Complete || retry.Records != 0 || retry.ChargedBytes != 0 {
						t.Fatal("completed checkpoint retry", retry, err)
					}
					break
				}
				if actual.RetiredOutcomes != checkpoint.Progress.Processed || actual.Outcomes != final.Processed-checkpoint.Progress.Processed {
					t.Fatal("stats lost original ordinal offset")
				}
				page, err := ledger.ExecutionPage(op, collectionExecutionOutcomeSlot(1), 1)
				if err != nil || len(page) != 1 || page[0].Outcome == nil || page[0].Outcome.Ordinal != checkpoint.Progress.Processed+1 {
					t.Fatal("suffix page treated live count as original ordinal", err)
				}
				if err := ledger.ApplyExecutionBatch(rows[:1]); !errors.Is(err, errCollectionLedgerConflict) {
					t.Fatal("ordinary writer recreated a retired outcome", err)
				}
				current, err := ledger.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				stream := executionRetirementStream(t, current)
				if err := current.Close(); err != nil {
					t.Fatal(err)
				}
				restored := newLedgerTest(t, disk, 16<<20)
				if err := importCollectionExecutionLedgerWithRetirement(bytes.NewReader(stream), restored, map[string]uint64{op: checkpoint.Progress.Processed}); err != nil {
					t.Fatal("retired suffix import", err)
				}
				got, err := restored.ExecutionStats(op)
				if err != nil || got != actual {
					t.Fatal("restored suffix quota or offset", got, actual, err)
				}
				legacy := newLedgerTest(t, false, 16<<20)
				if err := importCollectionExecutionLedger(bytes.NewReader(stream), legacy); err == nil {
					t.Fatal("legacy import inferred a missing prefix")
				}
				if batch > 10 {
					t.Fatal("retirement failed to progress")
				}
			}
			inputAfter, planAfter, validationAfter := validationLedgerStreams(t, ledger)
			if !bytes.Equal(inputBefore, inputAfter) || !bytes.Equal(planBefore, planAfter) || !bytes.Equal(validationBefore, validationAfter) {
				t.Fatal("execution retirement changed source namespaces")
			}
			if err := ledger.Append(op, ledgerTestItem(2)); err != nil {
				t.Fatal("refunded execution quota unavailable to other namespaces", err)
			}
		})
	}
}

func TestCollectionExecutionLedgerRetireRecordBoundaryAndPreparedOnlySuffix(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, pairedLast := range []bool{false, true} {
			t.Run(fmt.Sprint(disk, pairedLast), func(t *testing.T) {
				initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 257)
				for i := 0; i < 256; i++ {
					if pairedLast && i == 255 {
						_, accepted, terminal := executionStreamFixture(t, 1, 256)
						outcomes[i], terminals[i] = *accepted.Outcome, terminal.Terminal
						continue
					}
					o := &outcomes[i]
					o.Decision, o.PreparedID, o.MutationSequence, o.Receipt = "conflict", "", 0, nil
					o.UID, o.Revision, o.Generation, o.OldVersion = "", "", 0, ""
					terminals[i] = nil
				}
				prepared := candidates[256]
				prepared.At = initial.StartedAt.Add(2000 * time.Second)
				final, rows := executionRetirementRecords(t, initial, candidates[:256], outcomes[:256], terminals[:256], &prepared)
				ledger := newLedgerTest(t, disk, 8<<20)
				executionRetirementImport(t, ledger, rows)
				checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
				first, err := ledger.RetireExecutionPrefix(checkpoint, final, false)
				want := 256
				if pairedLast {
					want = 255
				}
				if err != nil || first.Records != want || first.Checkpoint.Progress.Processed != uint64(want) || first.PreparedRemoved || first.Complete {
					t.Fatal("pair or prepared split across record bound", first, err)
				}
				op := final.Binding.OperationID
				view := executionLedgerView(t, ledger)
				stream := executionRetirementStream(t, view)
				restored := newLedgerTest(t, disk, 8<<20)
				if err := importCollectionExecutionLedgerWithRetirement(bytes.NewReader(stream), restored, map[string]uint64{op: uint64(want)}); err != nil {
					t.Fatal("prepared-only or paired suffix import", err)
				}
				second, err := restored.RetireExecutionPrefix(first.Checkpoint, final, false)
				remaining := len(rows) - want
				if err != nil || !second.Complete || !second.PreparedRemoved || second.Records != remaining {
					t.Fatal("last batch failed paired/prepared disposal", second, err)
				}
				if used, err := restored.Bytes(); err != nil || used != 0 {
					t.Fatal("final batch retained quota", used, err)
				}
			})
		}
	}
}

func TestCollectionExecutionLedgerRetireByteBoundaryDefersPrepared(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 251)
	for i := 0; i < 250; i++ {
		o := &outcomes[i]
		o.Key.ID = fmt.Sprintf("%05d", i) + strings.Repeat("&", 251)
		o.Decision, o.PreparedID, o.MutationSequence, o.Receipt = "unchanged", "", 0, nil
		o.UID, o.Revision, o.OldVersion, o.Generation = strings.Repeat("<", 256), strings.Repeat(">", 256), strings.Repeat(">", 256), 1
		terminals[i] = nil
	}
	prepared := candidates[250].Clone()
	prepared.At = initial.StartedAt.Add(time.Hour)
	prepared.Record.Payload.Ciphertext = bytes.Repeat([]byte{0xa4}, secureconfig.MaxPlaintext+16)
	for i := 0; i < 5000; i++ {
		prepared.Record.References = append(prepared.Record.References, CatalogKey{Kind: "NotificationEndpoint", ID: fmt.Sprintf("endpoint-%05d-", i) + strings.Repeat("x", 241)})
	}
	final, rows := executionRetirementRecords(t, initial, candidates[:250], outcomes[:250], terminals[:250], &prepared)
	if final.EncodedBytes <= collectionLedgerBatchBytes || final.Prepared.EncodedBytes > collectionExecutionMaxFrame {
		t.Fatal("fixture does not cross batch byte bound within valid record bounds", final.EncodedBytes, final.Prepared.EncodedBytes)
	}
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 16<<20)
			executionRetirementImport(t, ledger, rows)
			checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
			first, err := ledger.RetireExecutionPrefix(checkpoint, final, false)
			if err != nil || first.Records != 250 || first.PreparedRemoved || first.Complete || first.EncodedBytes > collectionLedgerBatchBytes {
				t.Fatal("byte bound did not defer entire prepared disposal", first, err)
			}
			stats, err := ledger.ExecutionStats(final.Binding.OperationID)
			if err != nil || !stats.Prepared || stats.Outcomes != 0 || stats.RetiredOutcomes != 250 || stats.EncodedBytes != final.Prepared.EncodedBytes || stats.ChargedBytes != final.Prepared.EncodedBytes {
				t.Fatal("prepared-only suffix lost exact charge", stats, err)
			}
			second, err := ledger.RetireExecutionPrefix(first.Checkpoint, final, false)
			if err != nil || !second.Complete || !second.PreparedRemoved || second.Records != 1 || second.EncodedBytes != final.Prepared.EncodedBytes || second.ChargedBytes != final.Prepared.EncodedBytes {
				t.Fatal("separate bounded prepared disposal", second, err)
			}
		})
	}
}

func executionRetirementReplace(t *testing.T, ledger *collectionLedger, op, slot string, raw []byte) {
	t.Helper()
	if ledger.db != nil {
		if err := ledger.db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(collectionLedgerExecution).Bucket([]byte(op))
			if raw == nil {
				return b.Delete([]byte(slot))
			}
			return b.Put([]byte(slot), raw)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if raw == nil {
		delete(ledger.executionRows[op], slot)
		ledger.executionOrder[op].Delete(slot)
	} else {
		ledger.executionRows[op][slot] = raw
		ledger.executionOrder[op].ReplaceOrInsert(slot)
	}
}

func TestCollectionExecutionLedgerRetireCorruptionRollsBackBatch(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, corruption := range []string{"missing-terminal", "wrong-terminal", "malformed-outcome", "hidden-extra-row", "wrong-final-digest", "wrong-final-root", "wrong-prepared"} {
			t.Run(fmt.Sprint(disk, corruption), func(t *testing.T) {
				initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 22)
				prepared := candidates[21]
				prepared.At = initial.StartedAt.Add(time.Hour)
				final, rows := executionRetirementRecords(t, initial, candidates[:21], outcomes[:21], terminals[:21], &prepared)
				ledger := newLedgerTest(t, disk, 8<<20)
				executionRetirementImport(t, ledger, rows)
				op := final.Binding.OperationID
				checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
				before, _ := ledger.Bytes()
				beforeStat, _ := ledger.ExecutionStats(op)
				switch corruption {
				case "missing-terminal":
					executionRetirementReplace(t, ledger, op, collectionExecutionTerminalSlot(21), nil)
				case "wrong-terminal":
					changed := *terminals[20]
					changed.ChildID = ledgerTestOperation(99999)
					raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Terminal: &changed})
					if err != nil {
						t.Fatal(err)
					}
					executionRetirementReplace(t, ledger, op, collectionExecutionTerminalSlot(21), raw)
				case "malformed-outcome":
					executionRetirementReplace(t, ledger, op, collectionExecutionOutcomeSlot(20), []byte(`{"version":1}`))
				case "hidden-extra-row":
					executionRetirementReplace(t, ledger, op, "unknown", []byte(`{}`))
				case "wrong-final-digest":
					final.OutcomeDigest = strings.Repeat("e", 64)
				case "wrong-final-root":
					final.TerminalRoot = strings.Repeat("e", 64)
				case "wrong-prepared":
					changed := prepared.Clone()
					changed.Record.Payload.Ciphertext[0] ^= 1
					raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Prepared: &changed})
					if err != nil {
						t.Fatal(err)
					}
					executionRetirementReplace(t, ledger, op, "prepared", raw)
				}
				result, err := ledger.RetireExecutionPrefix(checkpoint, final, false)
				if err == nil || !reflect.DeepEqual(result, collectionExecutionRetirementBatch{}) {
					t.Fatal("corrupt batch returned an advanced checkpoint", result, err)
				}
				if used, err := ledger.Bytes(); err != nil || used != before {
					t.Fatal("failed batch refunded quota", used, before, err)
				}
				if err := ledger.read(func(v *collectionLedgerView) error {
					for _, slot := range []string{collectionExecutionOutcomeSlot(1), collectionExecutionTerminalSlot(1), "prepared"} {
						raw, err := v.encodedExecution(op, slot)
						if err != nil || raw == nil {
							return fmt.Errorf("failed batch removed %s: %w", slot, err)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !disk && ledger.executionStats[op] != beforeStat {
					t.Fatal("failed memory batch published changed stats")
				}
			})
		}
	}
}

func TestCollectionExecutionLedgerRetireNativeCommitFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file-descriptor failure is a Unix fixture")
	}
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 10)
	final, rows := executionRetirementRecords(t, initial, candidates, outcomes, terminals, nil)
	ledger := newLedgerTest(t, true, 8<<20)
	executionRetirementImport(t, ledger, rows)
	op := final.Binding.OperationID
	before, _ := ledger.Bytes()
	beforeStat, _ := ledger.ExecutionStats(op)
	path := ledger.db.Path()
	if err := ledger.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
	if err != nil {
		t.Fatal(err)
	}
	ledger.db = db
	checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
	result, err := ledger.RetireExecutionPrefix(checkpoint, final, false)
	var pathErr *os.PathError
	if err == nil || !errors.As(err, &pathErr) || pathErr.Path != path || !reflect.DeepEqual(result, collectionExecutionRetirementBatch{}) {
		t.Fatal("native commit did not fail without publishing checkpoint", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ledger.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.ExecutionStats(op)
	used, usedErr := ledger.Bytes()
	if err != nil || usedErr != nil || after != beforeStat || used != before || !reflect.DeepEqual(executionStreamRecords(t, ledger), rows) {
		t.Fatal("failed native commit removed rows or quota", err, usedErr)
	}
	result, err = ledger.RetireExecutionPrefix(checkpoint, final, false)
	if err != nil || !result.Complete {
		t.Fatal("retry after actual storage failure", result, err)
	}
}

func TestCollectionExecutionLedgerRetireConcurrentReaders(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 400)
			final, rows := executionRetirementRecords(t, initial, candidates, outcomes, terminals, nil)
			ledger := newLedgerTest(t, disk, 8<<20)
			executionRetirementImport(t, ledger, rows)
			frozen := executionLedgerView(t, ledger)
			op := final.Binding.OperationID
			reader := make(chan error, 1)
			go func() {
				for range 20 {
					old, err := frozen.ExecutionPage(op, "", 25)
					if err != nil || len(old) != 25 || old[0].Outcome == nil || old[0].Outcome.Ordinal != 1 {
						reader <- fmt.Errorf("frozen original prefix changed: %v", err)
						return
					}
					live, err := ledger.ExecutionPage(op, "", 25)
					if err != nil {
						reader <- err
						return
					}
					for i := 1; i < len(live); i++ {
						if live[i-1].Outcome != nil && live[i].Outcome != nil && live[i].Outcome.Ordinal != live[i-1].Outcome.Ordinal+1 {
							reader <- errors.New("concurrent live read observed a partially deleted prefix")
							return
						}
					}
				}
				reader <- nil
			}()
			checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
			for {
				r, err := ledger.RetireExecutionPrefix(checkpoint, final, false)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint = r.Checkpoint
				if r.Complete {
					break
				}
			}
			if err := <-reader; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectionExecutionLedgerRetireEmptyAndInvalidProgress(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 3)
	checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
	for _, disk := range []bool{false, true} {
		ledger := newLedgerTest(t, disk, 8<<20)
		empty, err := ledger.RetireExecutionPrefix(checkpoint, initial, false)
		if err != nil || !empty.Complete || empty.Records != 0 || empty.PreparedRemoved {
			t.Fatal("empty original execution", empty, err)
		}
		final, rows := executionRetirementRecords(t, initial, candidates[:2], outcomes[:2], terminals[:2], &candidates[2])
		executionRetirementImport(t, ledger, rows)
		if _, err := ledger.RetireExecutionPrefix(checkpoint, final, true); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("early prepared removal accepted", err)
		}
		unsettled := final.Clone()
		unsettled.ChildTerminals--
		if _, err := ledger.RetireExecutionPrefix(checkpoint, unsettled, false); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("unsettled original execution accepted", err)
		}
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.RetireExecutionPrefix(checkpoint, final, false); !errors.Is(err, errCollectionLedgerClosed) {
			t.Fatal("closed ledger retirement", err)
		}
	}
	// Zero offsets do not alter the existing canonical metadata JSON.
	s, err := collectionExecutionRetirementStats(checkpoint, retirementCheckpointFinal(t, initial, candidates, outcomes, terminals), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil || bytes.Contains(raw, []byte("retired_outcomes")) {
		t.Fatal("legacy execution stats gained a serialized offset", err)
	}
}
