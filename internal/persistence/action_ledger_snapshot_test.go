package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestActionLedgerFrozenSnapshotAndImport(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			row := testActionRow("a")
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 10, Ordinal: 2}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			frozen, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Close()
			row.Action.Review = &ActionReview{Resolution: "accepted", Revision: "review-v1"}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 11}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			var data bytes.Buffer
			if err := writeActionLedger(&data, frozen); err != nil {
				t.Fatal(err)
			}
			restored := newLedgerTest(t, disk, 1<<20)
			progress, err := importActionLedger(bytes.NewReader(data.Bytes()), restored)
			if err != nil || progress.Sequence != 1 || progress.Position != (actionLedgerPosition{Index: 10, Ordinal: 2}) {
				t.Fatalf("import: %+v %v", progress, err)
			}
			view, err := restored.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got, found, err := view.Get(t.Context(), "a")
			if err != nil || !found || got.Action.Review != nil {
				t.Fatalf("snapshot included later update: %+v %v", got, err)
			}
			if _, err := restored.CommitActions(actionLedgerPosition{Index: 11}, []actionLedgerRow{row}); err != nil {
				t.Fatal("replay after snapshot:", err)
			}
			if got, _, err := view.Get(t.Context(), "a"); err != nil || got.Action.Review != nil {
				t.Fatal("restored cursor mutated")
			}
			for _, suffix := range []int{1, 16, 48} {
				broken := newLedgerTest(t, disk, 1<<20)
				if _, err := importActionLedger(bytes.NewReader(data.Bytes()[:data.Len()-suffix]), broken); err == nil {
					t.Fatal("truncated snapshot imported")
				}
			}
		})
	}
}

func TestActionLedgerSnapshotRejectsDamagedCurrentInventory(t *testing.T) {
	for _, damage := range []string{"substituted", "stale", "missing", "bucket"} {
		t.Run(damage, func(t *testing.T) {
			ledger := newLedgerTest(t, true, 1<<20)
			row := testActionRow("a")
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{row, testActionRow("b")}); err != nil {
				t.Fatal(err)
			}
			row.Action.State = Failed
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			if err := ledger.db.Update(func(tx *bolt.Tx) error {
				current := tx.Bucket(actionCurrentBucket)
				switch damage {
				case "stale":
					return current.Put([]byte("a"), bytes.Clone(tx.Bucket(actionRowsBucket).Get(actionVersionKey("a", 1))))
				case "missing":
					return current.Delete([]byte("b"))
				case "bucket":
					return tx.DeleteBucket(actionReviewBucket)
				default:
					row, err := decodeActionRow(current.Get([]byte("b")))
					if err != nil {
						return err
					}
					row.Action.Outcome = "substituted"
					data, err := encodeActionRow(row)
					if err != nil {
						return err
					}
					return current.Put([]byte("b"), data)
				}
			}); err != nil {
				t.Fatal(err)
			}
			frozen, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Close()
			var data bytes.Buffer
			if err := writeActionLedger(&data, frozen); !errors.Is(err, errActionLedger) {
				t.Fatalf("damaged state certified by snapshot: %v", err)
			}
		})
	}
}

func TestActionLedgerStoreCloseInvalidatesPinsBeforeUnlock(t *testing.T) {
	config := testConfig(t)
	store, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, store)
	at := time.Now().UTC()
	submit(t, store, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, Outcome: "failure", At: at})
	ledger := store.fsm.collections
	if ledger == nil {
		t.Fatal("missing action-only ledger")
	}
	pin, err := ledger.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if ledger.viewPins.Load() != 0 || !ledger.closedNative.Load() {
		t.Fatal("Store released ownership with a live generation")
	}
	if _, _, err := pin.Page(t.Context(), "", "", 100); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("old cursor survived Store shutdown")
	}
	for range 3 {
		store, err = Open(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.collectionGenerations.maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionLedgerSnapshotAndCursorReleaseInEitherOrder(t *testing.T) {
	for _, firstSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot-first=%v", firstSnapshot), func(t *testing.T) {
			ledger := newLedgerTest(t, true, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			cursor, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.Close(); err != nil {
				t.Fatal(err)
			}
			if firstSnapshot {
				if err := snapshot.Close(); err != nil {
					t.Fatal(err)
				}
				if _, found, err := cursor.Get(t.Context(), "a"); err != nil || !found {
					t.Fatal("cursor lost generation")
				}
				if err := cursor.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := cursor.Close(); err != nil {
					t.Fatal(err)
				}
				var data bytes.Buffer
				if err := writeActionLedger(&data, snapshot); err != nil {
					t.Fatal("snapshot lost generation:", err)
				}
				if err := snapshot.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if ledger.viewPins.Load() != 0 || !ledger.closedNative.Load() {
				t.Fatal("generation leaked")
			}
		})
	}
}
