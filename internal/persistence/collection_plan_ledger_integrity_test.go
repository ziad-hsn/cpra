package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestCollectionPlanLedgerCounterCorruptionAndRebuild(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, damage := range []string{"counter", "orphan", "global"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, damage), func(t *testing.T) {
				op := ledgerTestOperation(1)
				parts, total := planLedgerSmall(t, op)
				l := newLedgerTest(t, disk, 1<<20)
				appendPlanLedgerParts(t, l, op, parts)
				queried := op
				if damage == "orphan" {
					queried = ledgerTestOperation(3)
				}
				if disk {
					if err := l.db.Update(func(tx *bolt.Tx) error {
						switch damage {
						case "counter":
							return tx.Bucket(collectionLedgerPlanMeta).Put([]byte(op), collectionOrdinal(uint64(total+1)))
						case "orphan":
							return tx.Bucket(collectionLedgerPlanMeta).Put([]byte(queried), collectionOrdinal(1))
						default:
							return tx.Bucket(collectionLedgerMeta).Put([]byte("plan_bytes"), collectionOrdinal(uint64(total+1)))
						}
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					switch damage {
					case "counter":
						l.planOperationBytes[op]++
					case "orphan":
						l.planOperationBytes[queried] = 1
					default:
						l.planBytes++
					}
				}
				if _, _, err := l.PlanStats(queried); !errors.Is(err, errCollectionLedgerCorrupt) {
					t.Fatal("counter corruption was accepted", err)
				}
				if damage != "orphan" {
					if _, err := l.DeletePlanPage(op, uint64(len(parts)), total); !errors.Is(err, errCollectionLedgerCorrupt) {
						t.Fatal("bad accounting reached deletion", err)
					}
				}
			})
		}
	}
	l := newLedgerTest(t, true, 1<<20)
	op := ledgerTestOperation(1)
	parts, _ := planLedgerSmall(t, op)
	appendPlanLedgerParts(t, l, op, parts)
	if err := l.Publish(); err != nil {
		t.Fatal(err)
	}
	fresh, err := openCollectionLedger(l.directory, l.maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.generation == l.generation {
		t.Fatal("reused selected materialization")
	}
	if count, size, err := fresh.PlanStats(op); err != nil || count != 0 || size != 0 {
		t.Fatal("selected plan materialization became authority", count, size, err)
	}
	if count, _, err := l.PlanStats(op); err != nil || count != uint64(len(parts)) {
		t.Fatal("new generation changed old evidence", count, err)
	}
}

func TestCollectionPlanLedgerPageDoesNotDecodeBeyondByteBoundary(t *testing.T) {
	op := ledgerTestOperation(1)
	parts, _ := planLedgerParts(t, op, planCodecManyGuards(7000), 1)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 16<<20)
			appendPlanLedgerParts(t, l, op, parts)
			first, err := l.PlanPage(op, 0, 256)
			if err != nil || len(first) == 0 || len(first) == len(parts) {
				t.Fatal("missing byte boundary", err)
			}
			next := first[len(first)-1].Ordinal + 1
			raw, _ := collectionPlanLedgerEncoding(op, parts[next-1])
			raw = bytes.Replace(raw, []byte(`"kind":"guards"`), []byte(`"kind":"broken"`), 1)
			// Same byte length so the bad part still falls beyond the first page.
			corruptPlanLedgerPart(t, l, op, next, raw)
			page, err := l.PlanPage(op, 0, 256)
			if err != nil || len(page) != len(first) {
				t.Fatal("decoded beyond requested byte page", err)
			}
			if _, err := l.PlanPage(op, next-1, 1); !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("bad next page accepted", err)
			}
			view, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if err := view.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error { return nil }); !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("corrupt full walk succeeded", err)
			}
		})
	}
}
