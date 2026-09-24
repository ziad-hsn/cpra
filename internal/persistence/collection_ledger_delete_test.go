package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCollectionLedgerDeleteTailPreservesSnapshotAndOtherOperations(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 32<<20)
			op, other := ledgerTestOperation(1), ledgerTestOperation(2)
			var remainingBytes int64
			for ordinal := uint64(1); ordinal <= 7; ordinal++ {
				item := ledgerTestItem(ordinal)
				item.Payload.Ciphertext = bytes.Repeat([]byte{byte(ordinal)}, 1<<20)
				if err := l.Append(op, item); err != nil {
					t.Fatal(err)
				}
				cost, _ := collectionItemCost(op, item)
				remainingBytes += cost
			}
			if err := l.Append(other, ledgerTestItem(1)); err != nil {
				t.Fatal(err)
			}
			frozen, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Close()
			beforeBytes, _ := frozen.Bytes()
			remaining := uint64(7)
			for remaining > 0 {
				oldCount := remaining
				result, err := l.DeletePage(op, remaining, remainingBytes)
				if err != nil {
					t.Fatal(err)
				}
				if result.Rows == 0 || result.Rows > 2 || result.EncodedBytes > collectionLedgerBatchBytes || result.More != (result.Rows < remaining) {
					t.Fatal("unbounded or incorrect delete result:", result)
				}
				remaining -= result.Rows
				remainingBytes -= result.EncodedBytes
				page, err := l.Page(op, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				for i, item := range page {
					if item.Ordinal != uint64(i+1) || item.Ordinal > remaining {
						t.Fatal("remaining prefix changed")
					}
				}
				for ordinal := remaining + 1; ordinal <= oldCount; ordinal++ {
					if _, found, err := l.Item(op, ordinal); err != nil || found {
						t.Fatal("deleted tail row retained:", ordinal, err)
					}
					if _, found, err := l.Find(op, ledgerTestItem(ordinal).Key); err != nil || found {
						t.Fatal("deleted tail index retained:", ordinal, err)
					}
				}
				view, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				var exported bytes.Buffer
				_, err = view.WriteTo(&exported)
				_ = view.Close()
				if err != nil {
					t.Fatal(err)
				}
				restored := newLedgerTest(t, disk, 32<<20)
				if err := importCollectionLedger(bytes.NewReader(exported.Bytes()), restored); err != nil {
					t.Fatal("partial-prefix round trip:", err)
				}
				if next, err := restored.DeletePage(op, remaining, remainingBytes); err != nil || next.More != (next.Rows < remaining) {
					t.Fatal("round trip did not rebuild per-operation totals:", next, err)
				}
			}
			if remainingBytes != 0 {
				t.Fatal("deletion retained accounted bytes")
			}
			if _, found, err := l.Item(other, 1); err != nil || !found {
				t.Fatal("other operation was changed:", err)
			}
			if current, _ := frozen.Bytes(); current != beforeBytes {
				t.Fatal("frozen byte total changed")
			}
			var oldRows int
			if err := frozen.Walk(func(_ string, _ CollectionItem) error { oldRows++; return nil }); err != nil || oldRows != 8 {
				t.Fatal("frozen rows or totals changed:", oldRows, err)
			}
			if result, err := l.DeletePage(op, 0, 0); err != nil || result != (collectionLedgerDeletion{}) {
				t.Fatal("empty delete is not idempotent:", result, err)
			}
		})
	}
}

func TestCollectionLedgerDeleteCountBoundaryAndReusableQuota(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, count := range []uint64{256, 257} {
			t.Run(fmt.Sprintf("disk=%t/count=%d", disk, count), func(t *testing.T) {
				op := ledgerTestOperation(1)
				items := make([]CollectionItem, count)
				var total int64
				for i := range items {
					items[i] = ledgerTestItem(uint64(i + 1))
					cost, _ := collectionItemCost(op, items[i])
					total += cost
				}
				l := newLedgerTest(t, disk, total)
				if err := l.AppendBatch(op, items); err != nil {
					t.Fatal(err)
				}
				if err := l.Append(ledgerTestOperation(2), ledgerTestItem(1)); !errors.Is(err, errCollectionLedgerQuota) {
					t.Fatal("fixture is not at quota:", err)
				}
				for _, bad := range []struct {
					rows  uint64
					bytes int64
				}{{count - 1, total}, {count, total - 1}} {
					if _, err := l.DeletePage(op, bad.rows, bad.bytes); !errors.Is(err, errCollectionLedgerConflict) {
						t.Fatal("stale expected totals accepted:", err)
					}
					if got, _ := l.Bytes(); got != total {
						t.Fatal("conflict changed global accounting")
					}
				}
				result, err := l.DeletePage(op, count, total)
				if err != nil || result.Rows != 256 || result.More != (count == 257) {
					t.Fatal("count boundary:", result, err)
				}
				if err := l.Append(ledgerTestOperation(2), ledgerTestItem(1)); err != nil {
					t.Fatal("released logical quota not reusable:", err)
				}
				if count == 257 {
					last, err := l.DeletePage(op, 1, total-result.EncodedBytes)
					if err != nil || last.Rows != 1 || last.More {
						t.Fatal("last row:", last, err)
					}
				}
				before, _ := l.Bytes()
				var txID int
				if disk {
					txID = ledgerTestTransactionID(t, l)
				}
				if result, err := l.DeletePage(ledgerTestOperation(99), 0, 0); err != nil || result != (collectionLedgerDeletion{}) {
					t.Fatal("unknown empty delete:", result, err)
				}
				if disk && ledgerTestTransactionID(t, l) != txID {
					t.Fatal("unknown operation allocated a write transaction")
				}
				if after, _ := l.Bytes(); after != before {
					t.Fatal("unknown operation changed accounting")
				}
			})
		}
	}
}

func TestCollectionLedgerDeleteExactByteBoundary(t *testing.T) {
	op := ledgerTestOperation(1)
	items := []CollectionItem{ledgerTestItem(1), ledgerTestItem(2), ledgerTestItem(3)}
	for i := 0; i < 2; i++ {
		items[i].Payload.Ciphertext = make([]byte, 1<<20)
	}
	cost1, _ := collectionItemCost(op, items[0])
	cost2, _ := collectionItemCost(op, items[1])
	target := int64(collectionLedgerBatchBytes) - cost1 - cost2
	items[2].Payload.Ciphertext = make([]byte, 18) // Exactly 24 base64 characters.
	base, _ := collectionItemCost(op, items[2])
	items[2].Payload.Ciphertext = make([]byte, ((target-base+24)/4)*3)
	cost3, _ := collectionItemCost(op, items[2])
	if target-cost3 < 0 || target-cost3 > 3 {
		t.Fatal("invalid exact-byte fixture")
	}
	items[2].Key.ID += strings.Repeat("x", int(target-cost3))
	cost3, _ = collectionItemCost(op, items[2])
	if cost1+cost2+cost3 != collectionLedgerBatchBytes {
		t.Fatal("fixture does not exactly fill bound")
	}
	for _, disk := range []bool{false, true} {
		l := newLedgerTest(t, disk, 8<<20)
		if err := l.AppendBatch(op, items); err != nil {
			t.Fatal(err)
		}
		result, err := l.DeletePage(op, 3, collectionLedgerBatchBytes)
		if err != nil || result.Rows != 3 || result.More || result.EncodedBytes != collectionLedgerBatchBytes {
			t.Fatal("exact byte boundary:", result, err)
		}
	}
}

func TestCollectionLedgerDeleteCorruptionRollsBackWholePage(t *testing.T) {
	for _, disk := range []bool{false, true} {
		l := newLedgerTest(t, disk, 1<<20)
		op := ledgerTestOperation(1)
		if err := l.AppendBatch(op, []CollectionItem{ledgerTestItem(1), ledgerTestItem(2), ledgerTestItem(3)}); err != nil {
			t.Fatal(err)
		}
		total, _ := l.Bytes()
		if disk {
			if err := l.db.Update(func(tx *bolt.Tx) error {
				return tx.Bucket(collectionLedgerKeys).Bucket([]byte(op)).Put([]byte(ledgerTestItem(2).Key.indexKey()), collectionOrdinal(1))
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			l.keys[op][ledgerTestItem(2).Key] = 1
		}
		if _, err := l.DeletePage(op, 3, total); !errors.Is(err, errCollectionLedgerCorrupt) {
			t.Fatal("index corruption accepted:", err)
		}
		if used, _ := l.Bytes(); used != total {
			t.Fatal("corruption partially changed accounting")
		}
		for ordinal := uint64(1); ordinal <= 3; ordinal++ {
			if _, found, err := l.Item(op, ordinal); err != nil || !found {
				t.Fatal("corruption partially removed rows:", ordinal, err)
			}
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := l.DeletePage(op, 3, total); !errors.Is(err, errCollectionLedgerClosed) {
			t.Fatal("closed delete:", err)
		}
	}
}

func TestCollectionLedgerDeleteWriteFailureRetainsOriginalRows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file descriptor write-failure fixture is qualified on Unix")
	}
	l := newLedgerTest(t, true, 1<<20)
	op := ledgerTestOperation(1)
	if err := l.AppendBatch(op, []CollectionItem{ledgerTestItem(1), ledgerTestItem(2)}); err != nil {
		t.Fatal(err)
	}
	total, _ := l.Bytes()
	path := l.db.Path()
	if err := l.db.Close(); err != nil {
		t.Fatal(err)
	}
	// Keep bbolt's writer path enabled while the OS denies its WriteAt calls.
	// This exercises a real failed commit, not a pre-closed ledger or test hook.
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
	if err != nil {
		t.Fatal(err)
	}
	l.db = db
	result, writeErr := l.DeletePage(op, 2, total)
	var pathError *os.PathError
	if writeErr == nil || result != (collectionLedgerDeletion{}) || !errors.As(writeErr, &pathError) || pathError.Path != path {
		t.Fatal("expected an actual filesystem write rejection with no committed result:", result, writeErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	l.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Bytes(); got != total {
		t.Fatal("failed commit changed durable accounting")
	}
	for ordinal := uint64(1); ordinal <= 2; ordinal++ {
		if _, found, err := l.Item(op, ordinal); err != nil || !found {
			t.Fatal("failed commit removed durable row:", ordinal, err)
		}
	}
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.WriteTo(io.Discard); err != nil {
		t.Fatal("failed commit corrupted ledger:", err)
	}
}
