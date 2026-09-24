package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func validationLedgerItem(n uint64) CollectionValidationItem {
	return CollectionValidationItem{Ordinal: n, Key: CatalogKey{Kind: "Monitor", ID: fmt.Sprintf("monitor-%d", n)}, Source: "source.00000000000000000001", Document: 1, Item: n, Change: "create"}
}
func validationLedgerItems(n int) []CollectionValidationItem {
	items := make([]CollectionValidationItem, n)
	for i := range items {
		items[i] = validationLedgerItem(uint64(i + 1))
	}
	return items
}
func appendValidationLedger(t *testing.T, l *collectionLedger, op string, items []CollectionValidationItem) {
	t.Helper()
	for start := 0; start < len(items); start += collectionLedgerBatchLimit {
		if err := l.AppendValidationItems(op, items[start:min(start+collectionLedgerBatchLimit, len(items))]); err != nil {
			t.Fatal(err)
		}
	}
}
func validationLedgerCost(t *testing.T, op string, items []CollectionValidationItem) int64 {
	t.Helper()
	var total int64
	for _, item := range items {
		n, err := collectionValidationLedgerCost(op, item)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return total
}
func validationLedgerStreams(t *testing.T, l *collectionLedger) ([]byte, []byte, []byte) {
	t.Helper()
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var input, plans, validation bytes.Buffer
	if _, err := v.WriteTo(&input); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionPlanLedger(&plans, v); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionValidationLedger(&validation, v); err != nil {
		t.Fatal(err)
	}
	return input.Bytes(), plans.Bytes(), validation.Bytes()
}
func TestCollectionValidationLedgerAtomicQuotaAndLegacyStreams(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			op := ledgerTestOperation(1)
			items := validationLedgerItems(3)
			total := validationLedgerCost(t, op, items)
			input := ledgerTestItem(1)
			inputBytes, _ := collectionItemCost(op, input)
			plans, planBytes := planLedgerSmall(t, op)
			l := newLedgerTest(t, disk, total+inputBytes+planBytes)
			if err := l.Append(op, input); err != nil {
				t.Fatal(err)
			}
			appendPlanLedgerParts(t, l, op, plans)
			beforeInput, beforePlans, _ := validationLedgerStreams(t, l)
			// A later conflict rolls back earlier new rows in this same batch.
			if err := l.AppendValidationItems(op, []CollectionValidationItem{items[0], items[2]}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal(err)
			}
			if count, size, err := l.ValidationStats(op); err != nil || count != 0 || size != 0 {
				t.Fatal(count, size, err)
			}
			if err := l.AppendValidationItems(op, items); err != nil {
				t.Fatal(err)
			}
			if err := l.AppendValidationItems(op, items); err != nil {
				t.Fatal("exact retry at quota", err)
			}
			if count, size, err := l.ValidationStats(op); err != nil || count != 3 || size != total {
				t.Fatal(count, size, err)
			}
			if size, _ := l.Bytes(); size != total+inputBytes+planBytes {
				t.Fatal("combined quota changed")
			}
			if err := l.Append(ledgerTestOperation(2), input); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("input bypassed validation quota", err)
			}
			otherPlans, _ := planLedgerSmall(t, ledgerTestOperation(2))
			if err := l.AppendPlanFragments(ledgerTestOperation(2), otherPlans); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("plan bypassed validation quota", err)
			}
			altered := items[0]
			altered.Issue = "conflict"
			if err := l.AppendValidationItems(op, []CollectionValidationItem{altered}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("changed retry", err)
			}
			afterInput, afterPlans, _ := validationLedgerStreams(t, l)
			if !bytes.Equal(beforeInput, afterInput) || !bytes.Equal(beforePlans, afterPlans) {
				t.Fatal("validation namespace changed previous stream bytes")
			}
			page, err := l.ValidationPage(op, 0, 500)
			if err != nil || !reflect.DeepEqual(page, items) {
				t.Fatal("page mismatch", err)
			}
			page[0].Issue = "conflict"
			again, _ := l.ValidationPage(op, 0, 1)
			if again[0] != items[0] {
				t.Fatal("caller modified encoded state")
			}
			if _, err := l.ValidationPage(op, 0, 501); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("page count limit", err)
			}
			if err := l.AppendValidationItems(op, validationLedgerItems(257)); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("batch count limit", err)
			}
			if _, err := l.DeleteValidationPage(op, 3, total-1); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("stale cleanup bytes", err)
			}
			result, err := l.DeleteValidationPage(op, 3, total)
			if err != nil || result.Rows != 3 || result.EncodedBytes != total || result.More {
				t.Fatal(result, err)
			}
			if err := l.AppendValidationItems(op, items); err != nil {
				t.Fatal("quota was not reusable", err)
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			if err := l.AppendValidationItems(op, items); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal(err)
			}
			if _, _, err := l.ValidationStats(op); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal(err)
			}
			if _, err := l.DeleteValidationPage(op, 3, total); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal(err)
			}
		})
	}
}
func TestCollectionValidationLedgerQuotaRollsBackLaterItem(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			op := ledgerTestOperation(1)
			items := validationLedgerItems(3)
			quota := validationLedgerCost(t, op, items[:2])
			l := newLedgerTest(t, disk, quota)
			if err := l.AppendValidationItems(op, items[:1]); err != nil {
				t.Fatal(err)
			}
			before, _ := l.Bytes()
			if err := l.AppendValidationItems(op, items[1:]); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal(err)
			}
			if count, size, err := l.ValidationStats(op); err != nil || count != 1 || size != before {
				t.Fatal("failed batch retained a partial row", count, size, err)
			}
		})
	}
}
func TestCollectionValidationLedgerFrozenPagingAndTailDeletion(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, count := range []int{256, 257, 601} {
			t.Run(fmt.Sprintf("disk=%t/count=%d", disk, count), func(t *testing.T) {
				op := ledgerTestOperation(1)
				items := validationLedgerItems(count)
				total := validationLedgerCost(t, op, items)
				l := newLedgerTest(t, disk, 4<<20)
				appendValidationLedger(t, l, op, items)
				other := ledgerTestOperation(2)
				appendValidationLedger(t, l, other, items[:1])
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				after := uint64(0)
				var all []CollectionValidationItem
				for after < uint64(count) {
					page, err := l.ValidationPage(op, after, 500)
					if err != nil || len(page) == 0 {
						t.Fatal(err)
					}
					if validationLedgerCost(t, op, page) > collectionLedgerPageBytes {
						t.Fatal("page overflow")
					}
					all = append(all, page...)
					after = page[len(page)-1].Ordinal
				}
				if !reflect.DeepEqual(all, items) {
					t.Fatal("paging duplicated or lost items")
				}
				remaining, used := uint64(count), total
				for remaining > 0 {
					result, err := l.DeleteValidationPage(op, remaining, used)
					if err != nil || result.Rows != min(remaining, uint64(256)) || result.More != (remaining > 256) || result.EncodedBytes > collectionLedgerBatchBytes {
						t.Fatal(result, err)
					}
					remaining -= result.Rows
					used -= result.EncodedBytes
				}
				if count, size, err := l.ValidationStats(other); err != nil || count != 1 || size <= 0 {
					t.Fatal("removed another operation", err)
				}
				var txID int
				if disk {
					txID = ledgerTestTransactionID(t, l)
				}
				if result, err := l.DeleteValidationPage(op, 0, 0); err != nil || result != (collectionLedgerDeletion{}) {
					t.Fatal(result, err)
				}
				if result, err := l.DeleteValidationPage(ledgerTestOperation(99), 0, 0); err != nil || result != (collectionLedgerDeletion{}) {
					t.Fatal(result, err)
				}
				if disk && ledgerTestTransactionID(t, l) != txID {
					t.Fatal("empty deletion allocated or wrote")
				}
				if frozenCount, size, err := v.ValidationStats(op); err != nil || frozenCount != uint64(count) || size != total {
					t.Fatal("frozen counters changed", err)
				}
				var seen []CollectionValidationItem
				if err := v.WalkValidation(context.Background(), func(operation string, item CollectionValidationItem) error {
					if operation == op {
						seen = append(seen, item)
					}
					return nil
				}); err != nil || !reflect.DeepEqual(seen, items) {
					t.Fatal("frozen rows changed", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := v.WalkValidation(ctx, func(string, CollectionValidationItem) error { t.Fatal("canceled walk visited"); return nil }); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				var walkContext context.Context
				walkContext, cancel = context.WithCancel(context.Background())
				visits := 0
				if err := v.WalkValidation(walkContext, func(string, CollectionValidationItem) error { visits++; cancel(); return nil }); !errors.Is(err, context.Canceled) || visits != 1 {
					t.Fatal("walk missed callback cancellation", err)
				}
			})
		}
	}
}
func TestCollectionValidationLedgerRejectsCorruptMetadataAndRows(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, mode := range []string{"row", "oversize", "counter", "orphan"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, mode), func(t *testing.T) {
				op := ledgerTestOperation(1)
				items := validationLedgerItems(3)
				l := newLedgerTest(t, disk, 1<<20)
				appendValidationLedger(t, l, op, items)
				total, _ := l.Bytes()
				queried := op
				if mode == "orphan" {
					queried = ledgerTestOperation(2)
				}
				badRaw := []byte(`{"secret":"must-not-decode"}`)
				if mode == "oversize" {
					badRaw = bytes.Repeat([]byte("x"), collectionValidationLedgerMaxFrame+1)
				}
				if disk {
					if err := l.db.Update(func(tx *bolt.Tx) error {
						switch mode {
						case "row", "oversize":
							return tx.Bucket(collectionLedgerValidation).Bucket([]byte(op)).Put(collectionOrdinal(2), badRaw)
						case "counter":
							return tx.Bucket(collectionLedgerValidationMeta).Put([]byte(op), collectionOrdinal(uint64(total+1)))
						default:
							return tx.Bucket(collectionLedgerValidationMeta).Put([]byte(queried), collectionOrdinal(1))
						}
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					switch mode {
					case "row", "oversize":
						l.validationRows[op][2] = badRaw
					case "counter":
						l.validationOperationBytes[op]++
					case "orphan":
						l.validationOperationBytes[queried] = 1
					}
				}
				if mode == "row" || mode == "oversize" {
					if _, err := l.ValidationPage(op, 0, 500); !errors.Is(err, errCollectionLedgerCorrupt) {
						t.Fatal(err)
					}
					if _, err := l.DeleteValidationPage(op, 3, total); !errors.Is(err, errCollectionLedgerCorrupt) {
						t.Fatal("corrupt tail committed", err)
					}
					if used, _ := l.Bytes(); used != total {
						t.Fatal("corrupt deletion changed quota")
					}
				} else {
					if _, _, err := l.ValidationStats(queried); !errors.Is(err, errCollectionLedgerCorrupt) {
						t.Fatal(err)
					}
				}
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				if err := v.WalkValidation(context.Background(), func(string, CollectionValidationItem) error { return nil }); !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("corrupt inventory exported", err)
				}
			})
		}
	}
	for _, mutate := range []func(*CollectionValidationItem){func(i *CollectionValidationItem) { i.Issue = "private-provider-diagnostic" }, func(i *CollectionValidationItem) { i.Source = "/private/source/path.yaml" }, func(i *CollectionValidationItem) { i.UID = strings.Repeat("x", 257) }} {
		item := validationLedgerItem(1)
		mutate(&item)
		if _, err := collectionValidationLedgerEncoding(ledgerTestOperation(1), item); err == nil {
			t.Fatal("private or invalid result accepted")
		}
	}
}
func TestCollectionValidationLedgerRealWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only descriptor write failure is a Unix fixture")
	}
	for _, action := range []string{"append", "delete"} {
		t.Run(action, func(t *testing.T) {
			op := ledgerTestOperation(1)
			items := validationLedgerItems(3)
			l := newLedgerTest(t, true, 1<<20)
			appendValidationLedger(t, l, op, items[:2])
			total, _ := l.Bytes()
			path := l.db.Path()
			if err := l.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
			if err != nil {
				t.Fatal(err)
			}
			l.db = db
			var writeErr error
			if action == "append" {
				writeErr = l.AppendValidationItems(op, items[2:])
			} else {
				var result collectionLedgerDeletion
				result, writeErr = l.DeleteValidationPage(op, 2, total)
				if result != (collectionLedgerDeletion{}) {
					t.Fatal("failed deletion returned committed progress")
				}
			}
			var pathError *os.PathError
			if !errors.As(writeErr, &pathError) || pathError.Path != path {
				t.Fatal("did not exercise real OS write failure", writeErr)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			l.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if count, size, err := l.ValidationStats(op); err != nil || count != 2 || size != total {
				t.Fatal("failed commit changed stored prefix", count, size, err)
			}
			page, err := l.ValidationPage(op, 0, 500)
			if err != nil || !reflect.DeepEqual(page, items[:2]) {
				t.Fatal("failed commit changed rows", err)
			}
		})
	}
}
