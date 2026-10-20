package persistence

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestActionLedgerLargeHistoricalRowsRoundTripBothStreams(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, reviews := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%v/reviews=%v", disk, reviews), func(t *testing.T) {
				ledger := newLedgerTest(t, disk, 32<<20)
				row := testActionRow("large-revision")
				row.Action.Revision = strings.Repeat("r", 2095864)
				row.Action.Review = &ActionReview{Revision: "review", Resolution: "inconclusive", Actor: "operator", At: policyTime(), Reason: "Inspected provider", Note: strings.Repeat("n", 2048)}
				row.Sequence = 1
				encoded, err := encodeActionRow(row)
				if err != nil || len(encoded) <= 2<<20 {
					t.Fatal("fixture must cross the previous action-row limit", len(encoded), err)
				}
				var policy []reviewRecord
				if reviews {
					m := testMonitor()
					m.ID, m.Revision = row.MonitorID, row.Action.Revision
					m.Policy.Endpoints = map[string]int{"blue": 1}
					policy = testReviewPolicy(t, ledger, &m)
				}
				if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}, policy); err != nil {
					t.Fatal(err)
				}
				view, err := ledger.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer view.Close()
				var stream bytes.Buffer
				if err := writeActionLedger(&stream, view); err != nil {
					t.Fatal(err)
				}
				magic := actionLedgerMagic
				if reviews {
					magic = actionLedgerMagicV2
				}
				if !bytes.HasPrefix(stream.Bytes(), magic) {
					t.Fatal("wrong stream version")
				}
				restored := newLedgerTest(t, disk, 32<<20)
				if _, err := importActionLedger(bytes.NewReader(stream.Bytes()), restored); err != nil {
					t.Fatal(err)
				}
				pin, err := restored.pinActions()
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				got, ok, err := pin.Get(t.Context(), row.Action.ID)
				if err != nil || !ok || got.Action.Revision != row.Action.Revision || got.Action.Review.Note != row.Action.Review.Note {
					t.Fatal("large original evidence changed", err)
				}
				// Reject oversized lengths before allocating or reading their payload.
				broken := bytes.Clone(stream.Bytes())
				headerLen := int(binary.BigEndian.Uint32(broken[len(magic):]))
				frame := len(magic) + 4 + headerLen
				limit := uint32(actionLedgerMaxFrame)
				if reviews {
					limit++
				} // V2 includes the frame-kind byte.
				binary.BigEndian.PutUint32(broken[frame:], limit+1)
				if _, err := importActionLedger(bytes.NewReader(broken), newLedgerTest(t, disk, 32<<20)); !errors.Is(err, errActionLedger) {
					t.Fatal("oversized frame accepted", err)
				}
			})
		}
	}
}

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
