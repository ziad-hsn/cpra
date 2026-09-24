package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func planLedgerParts(t *testing.T, op string, emit func(*CollectionPlanEncoder) error, count uint64) ([]CollectionPlanLedgerFragment, int64) {
	t.Helper()
	h := planCodecHeader(count)
	h.OperationID = op
	var artifact bytes.Buffer
	if _, err := EncodeCollectionPlan(context.Background(), &artifact, h, emit); err != nil {
		t.Fatal(err)
	}
	var parts []CollectionPlanLedgerFragment
	var total int64
	_, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(artifact.Bytes()), func(f CollectionPlanFragment) error {
		part := CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: f}
		parts = append(parts, part)
		cost, err := collectionPlanLedgerCost(op, part)
		total += cost
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return parts, total
}

func planLedgerSmall(t *testing.T, op string) ([]CollectionPlanLedgerFragment, int64) {
	return planLedgerParts(t, op, func(e *CollectionPlanEncoder) error {
		if err := e.BeginRow(planCodecRow(1, 1, CatalogKey{"Monitor", "one"}, "create")); err != nil {
			return err
		}
		return e.EndRow()
	}, 1)
}

func appendPlanLedgerParts(t *testing.T, l *collectionLedger, op string, parts []CollectionPlanLedgerFragment) {
	t.Helper()
	for start := 0; start < len(parts); {
		end, size := start, int64(0)
		for end < len(parts) && end-start < collectionLedgerBatchLimit {
			cost, err := collectionPlanLedgerCost(op, parts[end])
			if err != nil {
				t.Fatal(err)
			}
			if cost > collectionLedgerBatchBytes-size {
				break
			}
			size += cost
			end++
		}
		if end == start {
			t.Fatal("test cannot fit one fragment")
		}
		if err := l.AppendPlanFragments(op, parts[start:end]); err != nil {
			t.Fatal(err)
		}
		start = end
	}
}

func TestCollectionPlanLedgerAtomicRetryQuotaAndInputStream(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			op := ledgerTestOperation(1)
			parts, planBytes := planLedgerSmall(t, op)
			input := ledgerTestItem(1)
			inputBytes, _ := collectionItemCost(op, input)
			l := newLedgerTest(t, disk, planBytes+inputBytes)
			if err := l.Append(op, input); err != nil {
				t.Fatal(err)
			}
			inputStream := func() []byte {
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				var b bytes.Buffer
				if _, err := v.WriteTo(&b); err != nil {
					t.Fatal(err)
				}
				return b.Bytes()
			}
			before := inputStream()
			if err := l.AppendPlanFragments(op, parts); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, inputStream()) {
				t.Fatal("plan changed legacy input-only stream bytes")
			}
			if err := l.AppendPlanFragments(op, parts); err != nil {
				t.Fatal("exact retry at shared quota", err)
			}
			if size, _ := l.Bytes(); size != planBytes+inputBytes {
				t.Fatal("retry changed global quota")
			}
			if count, size, err := l.PlanStats(op); err != nil || count != uint64(len(parts)) || size != planBytes {
				t.Fatal(count, size, err)
			}
			if err := l.Append(ledgerTestOperation(2), ledgerTestItem(1)); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("input bypassed plan quota", err)
			}
			changed := parts[0]
			h := *changed.Fragment.Header
			h.Actor = "another"
			changed.Fragment.Header = &h
			if err := l.AppendPlanFragments(op, []CollectionPlanLedgerFragment{changed}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("changed retry", err)
			}
			afterFooter := parts[1]
			afterFooter.Ordinal = uint64(len(parts) + 1)
			if err := l.AppendPlanFragments(op, []CollectionPlanLedgerFragment{afterFooter}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("append after footer", err)
			}
			page, err := l.PlanPage(op, 0, 256)
			if err != nil || len(page) != len(parts) {
				t.Fatal(page, err)
			}
			page[0].Fragment.Header.Actor = "caller mutation"
			again, err := l.PlanPage(op, 0, 1)
			if err != nil || again[0].Fragment.Header.Actor != "operator" {
				t.Fatal("returned value borrowed", err)
			}
			result, err := l.DeletePlanPage(op, uint64(len(parts)), planBytes)
			if err != nil || result.Rows != uint64(len(parts)) || result.EncodedBytes != planBytes || result.More {
				t.Fatal(result, err)
			}
			if !bytes.Equal(before, inputStream()) {
				t.Fatal("cleanup changed input rows")
			}
			if size, _ := l.Bytes(); size != inputBytes {
				t.Fatal("cleanup accounting", size)
			}
			if err := l.Append(ledgerTestOperation(2), ledgerTestItem(1)); err != nil {
				t.Fatal("logical quota not reclaimed", err)
			}

			atomic := newLedgerTest(t, disk, 1<<20)
			gap := parts[1]
			gap.Ordinal = 3
			if err := atomic.AppendPlanFragments(op, []CollectionPlanLedgerFragment{parts[0], gap}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("gap", err)
			}
			if count, size, err := atomic.PlanStats(op); err != nil || count != 0 || size != 0 {
				t.Fatal("partial rejected transaction", count, size, err)
			}
			quota := newLedgerTest(t, disk, planBytes-1)
			if err := quota.AppendPlanFragments(op, parts); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("partial quota rejection", err)
			}
			if size, _ := quota.Bytes(); size != 0 {
				t.Fatal("quota retained prefix")
			}
		})
	}
}

func TestCollectionPlanLedgerFrozenViewsAndBoundedLargePages(t *testing.T) {
	op := ledgerTestOperation(1)
	parts, total := planLedgerParts(t, op, planCodecManyGuards(7000), 1)
	if total <= collectionLedgerPageBytes {
		t.Fatal("large fixture too small")
	}
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 16<<20)
			if err := l.AppendPlanFragments(op, parts); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("oversized batch", err)
			}
			if size, _ := l.Bytes(); size != 0 {
				t.Fatal("oversized batch changed state")
			}
			if err := l.AppendPlanFragments(op, parts[:2]); err != nil {
				t.Fatal(err)
			}
			frozen, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Close()
			appendPlanLedgerParts(t, l, op, parts[2:])
			if count, _, err := frozen.PlanStats(op); err != nil || count != 2 {
				t.Fatal("frozen count changed", count, err)
			}
			other, otherBytes := planLedgerSmall(t, ledgerTestOperation(2))
			appendPlanLedgerParts(t, l, ledgerTestOperation(2), other)
			var after uint64
			var seen, pages int
			for after < uint64(len(parts)) {
				page, err := l.PlanPage(op, after, 256)
				if err != nil || len(page) == 0 {
					t.Fatal("page", err)
				}
				var size int64
				for _, part := range page {
					if part.Ordinal != after+1 {
						t.Fatal("page gap")
					}
					after = part.Ordinal
					cost, _ := collectionPlanLedgerCost(op, part)
					size += cost
					seen++
				}
				if size > collectionLedgerPageBytes {
					t.Fatal("unbounded page")
				}
				pages++
			}
			if seen != len(parts) || pages < 2 {
				t.Fatal("large row was not paged", seen, pages)
			}
			view, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			var walked int
			last := ""
			err = view.WalkPlans(context.Background(), func(id string, part CollectionPlanLedgerFragment) error {
				if id < last {
					t.Fatal("unordered operation")
				}
				last = id
				walked++
				if part.Fragment.Header != nil {
					part.Fragment.Header.Actor = "changed"
				}
				return nil
			})
			if err != nil || walked != len(parts)+len(other) {
				t.Fatal(walked, err)
			}
			_ = view.Close()
			remaining, size := uint64(len(parts)), total
			for remaining > 0 {
				result, err := l.DeletePlanPage(op, remaining, size)
				if err != nil {
					t.Fatal(err)
				}
				if result.Rows == 0 || result.Rows > 256 || result.EncodedBytes > collectionLedgerBatchBytes {
					t.Fatal("unbounded delete", result)
				}
				remaining -= result.Rows
				size -= result.EncodedBytes
				if count, bytes, err := l.PlanStats(op); err != nil || count != remaining || bytes != size {
					t.Fatal(count, bytes, err)
				}
			}
			if count, size, err := l.PlanStats(ledgerTestOperation(2)); err != nil || count != uint64(len(other)) || size != otherBytes {
				t.Fatal("other namespace operation changed", count, size, err)
			}
			var frozenCount int
			if err := frozen.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error { frozenCount++; return nil }); err != nil || frozenCount != 2 {
				t.Fatal("frozen prefix changed after cleanup", frozenCount, err)
			}
			if err := frozen.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := frozen.PlanStats(op); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("closed stats", err)
			}
		})
	}
}

func corruptPlanLedgerPart(t *testing.T, l *collectionLedger, op string, ordinal uint64, raw []byte) {
	t.Helper()
	if l.db == nil {
		l.planRows[op][ordinal] = raw
		return
	}
	if err := l.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(collectionLedgerPlans).Bucket([]byte(op)).Put(collectionOrdinal(ordinal), raw)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionPlanLedgerCorruptionFailsClosed(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, damage := range []string{"oversized", "identity", "noncanonical", "missing"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, damage), func(t *testing.T) {
				op := ledgerTestOperation(1)
				parts, total := planLedgerSmall(t, op)
				l := newLedgerTest(t, disk, 8<<20)
				appendPlanLedgerParts(t, l, op, parts)
				var raw []byte
				switch damage {
				case "oversized":
					raw = make([]byte, collectionPlanLedgerMaxFrame+1)
				case "identity":
					raw, _ = collectionPlanLedgerEncoding(ledgerTestOperation(2), parts[2])
				case "noncanonical":
					raw, _ = collectionPlanLedgerEncoding(op, parts[2])
					raw = append([]byte(" "), raw...)
				case "missing":
					raw = []byte{}
				}
				corruptPlanLedgerPart(t, l, op, 3, raw)
				if page, err := l.PlanPage(op, 2, 1); !errors.Is(err, errCollectionLedgerCorrupt) || page != nil {
					t.Fatal("bad row was empty success", page, err)
				}
				before, _ := l.Bytes()
				if result, err := l.DeletePlanPage(op, uint64(len(parts)), total); !errors.Is(err, errCollectionLedgerCorrupt) || result != (collectionLedgerDeletion{}) {
					t.Fatal("corrupt deletion", result, err)
				}
				if after, _ := l.Bytes(); after != before {
					t.Fatal("rejected delete changed quota")
				}
				view, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer view.Close()
				if err := view.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error { return nil }); !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("corrupt walk", err)
				}
			})
		}
	}
}

func TestCollectionPlanLedgerDeleteCountFencesAndEmptyNoWrite(t *testing.T) {
	op := ledgerTestOperation(1)
	parts, _ := planLedgerParts(t, op, func(e *CollectionPlanEncoder) error {
		for ordinal := uint64(1); ordinal <= 128; ordinal++ {
			if err := e.BeginRow(planCodecRow(ordinal, ordinal, CatalogKey{"Monitor", fmt.Sprintf("monitor-%d", ordinal)}, "create")); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
		}
		return nil
	}, 128)
	for _, disk := range []bool{false, true} {
		for _, count := range []int{256, 257} {
			t.Run(fmt.Sprintf("disk=%t/count=%d", disk, count), func(t *testing.T) {
				l := newLedgerTest(t, disk, 1<<20)
				appendPlanLedgerParts(t, l, op, parts[:count])
				_, total, _ := l.PlanStats(op)
				if _, err := l.DeletePlanPage(op, uint64(count-1), total); !errors.Is(err, errCollectionLedgerConflict) {
					t.Fatal("stale count", err)
				}
				if _, err := l.DeletePlanPage(op, uint64(count), total-1); !errors.Is(err, errCollectionLedgerConflict) {
					t.Fatal("stale bytes", err)
				}
				result, err := l.DeletePlanPage(op, uint64(count), total)
				if err != nil || result.Rows != 256 || result.More != (count == 257) {
					t.Fatal(result, err)
				}
				if count == 257 {
					if _, err := l.DeletePlanPage(op, 1, total-result.EncodedBytes); err != nil {
						t.Fatal(err)
					}
				}
				var txID int
				if disk {
					txID = ledgerTestTransactionID(t, l)
				}
				if result, err := l.DeletePlanPage(op, 0, 0); err != nil || result != (collectionLedgerDeletion{}) {
					t.Fatal(result, err)
				}
				if disk && ledgerTestTransactionID(t, l) != txID {
					t.Fatal("empty cleanup wrote transaction")
				}
			})
		}
	}
}

func TestCollectionPlanLedgerWriteFailurePreservesCommittedPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only descriptor write failure is a native Unix fixture")
	}
	for _, action := range []string{"append", "delete"} {
		t.Run(action, func(t *testing.T) {
			op := ledgerTestOperation(1)
			parts, _ := planLedgerSmall(t, op)
			l := newLedgerTest(t, true, 1<<20)
			appendPlanLedgerParts(t, l, op, parts[:2])
			_, total, _ := l.PlanStats(op)
			path := l.db.Path()
			if err := l.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
			if err != nil {
				t.Fatal(err)
			}
			l.db = db
			var result collectionLedgerDeletion
			if action == "append" {
				err = l.AppendPlanFragments(op, parts[2:])
			} else {
				result, err = l.DeletePlanPage(op, 2, total)
			}
			var pathErr *os.PathError
			if err == nil || !errors.As(err, &pathErr) || pathErr.Path != path || result != (collectionLedgerDeletion{}) {
				t.Fatal("expected OS write rejection", result, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			l.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if count, size, err := l.PlanStats(op); err != nil || count != 2 || size != total {
				t.Fatal("failed write changed durable prefix", count, size, err)
			}
			view, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			var count int
			if err := view.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error { count++; return nil }); err != nil || count != 2 {
				t.Fatal(count, err)
			}
		})
	}
}

func TestCollectionPlanLedgerConcurrentReadsAndCancellation(t *testing.T) {
	op := ledgerTestOperation(1)
	parts, _ := planLedgerParts(t, op, planCodecFixture, 3)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 1<<20)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, part := range parts {
					if err := l.AppendPlanFragments(op, []CollectionPlanLedgerFragment{part}); err != nil {
						t.Error(err)
						return
					}
				}
			}()
			for i := 0; i < 30; i++ {
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = v.Close() })
				count, _, err := v.PlanStats(op)
				if err != nil {
					t.Fatal(err)
				}
				page, err := v.PlanPage(op, 0, 256)
				if err != nil || uint64(len(page)) != count {
					t.Fatal("inconsistent frozen read", len(page), count, err)
				}
				_ = v.Close()
			}
			wg.Wait()
			v, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			err = v.WalkPlans(ctx, func(string, CollectionPlanLedgerFragment) error { calls++; cancel(); return nil })
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatal("cancellation", calls, err)
			}
			if err := v.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error { return io.ErrClosedPipe }); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal("callback error lost", err)
			}
		})
	}
}
