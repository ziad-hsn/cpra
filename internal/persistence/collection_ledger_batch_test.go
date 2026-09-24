package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestCollectionLedgerPagesBoundBytesAndCoverEveryOrdinal(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 32<<20)
			op := ledgerTestOperation(1)
			for ordinal := uint64(1); ordinal <= 7; ordinal++ {
				item := ledgerTestItem(ordinal)
				item.Payload.Ciphertext = bytes.Repeat([]byte{byte(ordinal)}, 1<<20)
				if err := l.Append(op, item); err != nil {
					t.Fatal(err)
				}
			}
			for _, requested := range []int{1, 256, 500} {
				var after uint64
				var pages int
				for {
					page, err := l.Page(op, after, requested)
					if err != nil {
						t.Fatal(err)
					}
					if len(page) == 0 {
						break
					}
					if len(page) > requested {
						t.Fatal("count limit exceeded")
					}
					var size int64
					for _, item := range page {
						if item.Ordinal != after+1 {
							t.Fatal("pagination skipped or repeated an ordinal:", after, item.Ordinal)
						}
						cost, err := collectionItemCost(op, item)
						if err != nil {
							t.Fatal(err)
						}
						size += cost
						after = item.Ordinal
					}
					if size > collectionLedgerPageBytes {
						t.Fatal("encoded page exceeds 4 MiB:", size)
					}
					pages++
				}
				if after != 7 || requested > 1 && pages != 4 || requested == 1 && pages != 7 {
					t.Fatal("incomplete or unexpectedly unbounded pagination:", requested, after, pages)
				}
			}
		})
	}
}

func TestCollectionLedgerPageDoesNotDecodeBeyondByteBoundary(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 16<<20)
			op := ledgerTestOperation(1)
			for ordinal := uint64(1); ordinal <= 3; ordinal++ {
				item := ledgerTestItem(ordinal)
				item.Payload.Ciphertext = bytes.Repeat([]byte{0xaa}, 1<<20)
				if err := l.Append(op, item); err != nil {
					t.Fatal(err)
				}
			}
			// Corrupt the third row without changing its encoded length. A first
			// byte-bounded page must not decode it; the next page must reject it.
			if disk {
				if err := l.db.Update(func(tx *bolt.Tx) error {
					rows := tx.Bucket(collectionLedgerRecords).Bucket([]byte(op))
					data := bytes.Clone(rows.Get(collectionOrdinal(3)))
					data[0] = '!'
					return rows.Put(collectionOrdinal(3), data)
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				data := bytes.Clone(l.rows[op][3])
				data[0] = '!'
				l.rows[op][3] = data
			}
			page, err := l.Page(op, 0, 500)
			if err != nil || len(page) != 2 || page[1].Ordinal != 2 {
				t.Fatal("out-of-page payload decoded:", len(page), err)
			}
			if _, err = l.Page(op, page[1].Ordinal, 500); !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("corrupt next page accepted:", err)
			}
		})
	}
}

func ledgerTestTransactionID(t *testing.T, l *collectionLedger) int {
	t.Helper()
	var id int
	if err := l.db.View(func(tx *bolt.Tx) error { id = tx.ID(); return nil }); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCollectionLedgerImportBatchesManySmallRows(t *testing.T) {
	rows := make([]collectionLedgerRow, 600)
	for i := range rows {
		rows[i] = collectionLedgerRow{OperationID: ledgerTestOperation(1), Item: ledgerTestItem(uint64(i + 1))}
	}
	stream := ledgerTestStream(t, rows...)
	l := newLedgerTest(t, true, 16<<20)
	before := ledgerTestTransactionID(t, l)
	if err := importCollectionLedger(bytes.NewReader(stream), l); err != nil {
		t.Fatal(err)
	}
	if transactions := ledgerTestTransactionID(t, l) - before; transactions != 3 {
		t.Fatal("600 small rows should use three synchronous batches:", transactions)
	}
	var after uint64
	for {
		page, err := l.Page(ledgerTestOperation(1), after, 500)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			if item.Ordinal != after+1 {
				t.Fatal("import lost ordering")
			}
			after = item.Ordinal
		}
	}
	if after != 600 {
		t.Fatal("import lost rows:", after)
	}
}

func TestCollectionLedgerRejectedImportBatchPreservesOnlyUnpublishedPrefix(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, failure := range []string{"duplicate-key", "quota", "footer", "order"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, failure), func(t *testing.T) {
				rows := make([]collectionLedgerRow, 260)
				var quota int64 = 16 << 20
				var prefixBytes, through257 int64
				for i := range rows {
					rows[i] = collectionLedgerRow{OperationID: ledgerTestOperation(1), Item: ledgerTestItem(uint64(i + 1))}
					cost, _ := collectionItemCost(rows[i].OperationID, rows[i].Item)
					if i < 256 {
						prefixBytes += cost
					}
					if i < 257 {
						through257 += cost
					}
				}
				wantError := errCollectionLedgerCorrupt
				switch failure {
				case "duplicate-key":
					rows[259].Item.Key = rows[0].Item.Key
					wantError = errCollectionLedgerConflict
				case "quota":
					quota = through257
					wantError = errCollectionLedgerQuota
				case "order":
					rows[259].Item.Ordinal++
				}
				stream := ledgerTestStream(t, rows...)
				if failure == "footer" {
					stream[len(stream)-1] ^= 1
				}
				l := newLedgerTest(t, disk, quota)
				if err := importCollectionLedger(bytes.NewReader(stream), l); !errors.Is(err, wantError) {
					t.Fatal("wrong import rejection:", err)
				}
				if size, err := l.Bytes(); err != nil || size != prefixBytes {
					t.Fatal("failed batch left an unaccounted partial prefix:", size, prefixBytes, err)
				}
				last, found, err := l.Item(ledgerTestOperation(1), 256)
				if err != nil || !found || last.Ordinal != 256 {
					t.Fatal("previous successful batch lost:", found, err)
				}
				if _, found, err := l.Item(ledgerTestOperation(1), 257); err != nil || found {
					t.Fatal("failed batch retained an early row:", found, err)
				}
				if _, found, err := l.Find(ledgerTestOperation(1), ledgerTestItem(257).Key); err != nil || found {
					t.Fatal("failed batch retained key index:", found, err)
				}
				if disk {
					if _, err := os.Stat(filepath.Join(l.directory, "current.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("failed import selected a generation")
					}
				}
			})
		}
	}
}
