package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func testActionRow(id string) actionLedgerRow {
	return actionLedgerRow{MonitorID: "monitor", Action: Action{ID: id, Kind: "intervention", Revision: "configuration", State: Unknown, CatalogUID: "incarnation"}}
}

func TestActionLedgerRejectsMissingMetadataAndIndexes(t *testing.T) {
	for _, target := range []string{"progress", "metadata", "monitor-index"} {
		t.Run(target, func(t *testing.T) {
			ledger := newLedgerTest(t, true, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
				t.Fatal(err)
			}
			var original []byte
			if err := ledger.db.Update(func(tx *bolt.Tx) error {
				original = bytes.Clone(tx.Bucket(actionRowsBucket).Get(actionVersionKey("a", 1)))
				switch target {
				case "progress":
					return tx.Bucket(actionMetaBucket).Delete([]byte("progress"))
				case "metadata":
					return tx.DeleteBucket(actionMetaBucket)
				default:
					return tx.DeleteBucket(actionMonitorBucket)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.pinActions(); !errors.Is(err, errActionLedger) {
				t.Fatalf("damaged namespace presented as empty: %v", err)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{testActionRow("a")}); !errors.Is(err, errActionLedger) {
				t.Fatalf("damaged namespace accepted write: %v", err)
			}
			if err := ledger.db.View(func(tx *bolt.Tx) error {
				if !bytes.Equal(original, tx.Bucket(actionRowsBucket).Get(actionVersionKey("a", 1))) {
					t.Error("failure replaced committed evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestActionLedgerRejectsMissingOrSubstitutedVersions(t *testing.T) {
	for _, damage := range []string{"latest", "historical", "substituted", "all", "stale-head", "missing-head"} {
		t.Run(damage, func(t *testing.T) {
			ledger := newLedgerTest(t, true, 1<<20)
			row := testActionRow("a")
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			old, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			row.Action.Review = &ActionReview{Resolution: "inconclusive", Revision: "review-v1"}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			current, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if err := ledger.db.Update(func(tx *bolt.Tx) error {
				bucket := tx.Bucket(actionRowsBucket)
				switch damage {
				case "stale-head":
					return tx.Bucket(actionCurrentBucket).Put([]byte("a"), bytes.Clone(bucket.Get(actionVersionKey("a", 1))))
				case "missing-head":
					return tx.Bucket(actionCurrentBucket).Delete([]byte("a"))
				case "latest":
					return bucket.Delete(actionVersionKey("a", 2))
				case "historical":
					return bucket.Delete(actionVersionKey("a", 1))
				case "substituted":
					return bucket.Put(actionVersionKey("a", 2), bytes.Clone(bucket.Get(actionVersionKey("a", 1))))
				default:
					if err := bucket.Delete(actionVersionKey("a", 1)); err != nil {
						return err
					}
					return bucket.Delete(actionVersionKey("a", 2))
				}
			}); err != nil {
				t.Fatal(err)
			}
			view := current
			if damage == "historical" {
				view = old
			}
			if _, _, err := view.Get(t.Context(), "a"); !errors.Is(err, errActionLedger) {
				t.Fatalf("damaged version became old/absent evidence: %v", err)
			}
			if _, _, err := view.Page(t.Context(), "monitor", "", 100); !errors.Is(err, errActionLedger) {
				t.Fatalf("page concealed damaged version: %v", err)
			}
			if damage == "stale-head" || damage == "missing-head" {
				if _, err := ledger.CommitActions(actionLedgerPosition{Index: 3}, []actionLedgerRow{row}); !errors.Is(err, errActionLedger) {
					t.Fatalf("damaged head accepted write: %v", err)
				}
			}
		})
	}
}

func TestActionLedgerKeepsNotificationEpisodeDistinct(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	for i, color := range []string{"red", "gray", "blue"} {
		row := testActionRow(color)
		row.Action.Kind, row.Action.Color = "code", color
		row.Action.IncidentID, row.Action.NotificationEpisode = "outage", "episode-"+color
		if _, err := ledger.CommitActions(actionLedgerPosition{Index: uint64(i + 1)}, []actionLedgerRow{row}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.db.View(func(tx *bolt.Tx) error {
			owner := actionOwnerKey(row.MonitorID, row.Action)
			if string(tx.Bucket(actionEpisodeBucket).Get([]byte(owner+row.Action.NotificationEpisode+"\x00"+color))) != color {
				t.Error("missing notification episode")
			}
			if string(tx.Bucket(actionIncidentBucket).Get([]byte(owner+"outage\x00"+color))) != color {
				t.Error("missing incident index")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionLedgerOldPagesIgnoreFutureSuffix(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
				t.Fatal(err)
			}
			view, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{testActionRow("z")}); err != nil {
				t.Fatal(err)
			}
			if disk {
				if err := ledger.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(actionCurrentBucket).Put([]byte("z"), []byte("corrupt future"))
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				ledger.actions.current["z"] = []byte("corrupt future")
			}
			rows, next, err := view.Page(t.Context(), "monitor", "", 1)
			if err != nil || len(rows) != 1 || next != "" || rows[0].Action.ID != "a" {
				t.Fatalf("future suffix inspected: %+v %q %v", rows, next, err)
			}
		})
	}
}

func TestActionLedgerVersionsAndCommitAtomicity(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			first := testActionRow("action-a")
			position := actionLedgerPosition{Index: 5, Ordinal: 0}
			if sequence, err := ledger.CommitActions(position, []actionLedgerRow{first}); err != nil || sequence != 1 {
				t.Fatalf("initial commit: %d %v", sequence, err)
			}
			view, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if sequence, err := ledger.CommitActions(position, []actionLedgerRow{first}); err != nil || sequence != 1 {
				t.Fatalf("replay identity: %d %v", sequence, err)
			}
			reviewed := first
			reviewed.Action.Review = &ActionReview{Resolution: "accepted", Reason: "independent evidence", Revision: "review-v1"}
			if _, err := ledger.CommitActions(position, []actionLedgerRow{reviewed}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatalf("conflicting replay: %v", err)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 5, Ordinal: 1}, []actionLedgerRow{reviewed, testActionRow("action-b")}); err != nil {
				t.Fatal(err)
			}
			old, found, err := view.Get(t.Context(), first.Action.ID)
			if err != nil || !found || old.Action.Review != nil || ActionReviewRevision(old.Action) != ActionReviewRevision(first.Action) {
				t.Fatalf("frozen evidence changed: %+v %v", old, err)
			}
			if _, found, err := view.Get(t.Context(), "action-b"); err != nil || found {
				t.Fatalf("future action leaked: %v %v", found, err)
			}
			current, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			rows, next, err := current.Page(t.Context(), "monitor", "", 1)
			if err != nil || len(rows) != 1 || next != "action-a" || rows[0].Action.Review == nil {
				t.Fatalf("first page: %+v %q %v", rows, next, err)
			}
			rows, next, err = current.Page(t.Context(), "monitor", next, 1)
			if err != nil || len(rows) != 1 || rows[0].Action.ID != "action-b" || next != "" {
				t.Fatalf("second page: %+v %q %v", rows, next, err)
			}
			// The invalid final row must not leave the valid prefix or progress.
			invalid := testActionRow("action-z")
			invalid.MonitorID = ""
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 6}, []actionLedgerRow{testActionRow("action-c"), invalid}); err == nil {
				t.Fatal("invalid row accepted")
			}
			latest, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer latest.Close()
			if latest.sequence != 2 {
				t.Fatal("failed write advanced progress")
			}
			if _, found, err := latest.Get(t.Context(), "action-c"); err != nil || found {
				t.Fatalf("partial commit: %v %v", found, err)
			}
			// Returned records are detached, including review pointer fields.
			row, _, err := latest.Get(t.Context(), "action-a")
			if err != nil {
				t.Fatal(err)
			}
			row.Action.Review.Reason = "changed by reader"
			again, _, err := latest.Get(t.Context(), "action-a")
			if err != nil || again.Action.Review.Reason != "independent evidence" {
				t.Fatal("caller mutated retained evidence")
			}
		})
	}
}

func TestActionLedgerPinsSurviveOwnerClose(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("action")}); err != nil {
				t.Fatal(err)
			}
			view, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			copy := view
			if err := ledger.Close(); err != nil {
				t.Fatal(err)
			}
			if ledger.closedNative.Load() {
				t.Fatal("closed generation with a cursor")
			}
			if _, found, err := copy.Get(t.Context(), "action"); err != nil || !found {
				t.Fatalf("retired generation unreadable: %v %v", found, err)
			}
			if _, err := ledger.pinActions(); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("new cursor acquired retired owner")
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{testActionRow("later")}); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("retired owner accepted mutation")
			}
			if err := view.Close(); err != nil {
				t.Fatal(err)
			}
			if err := copy.Close(); err != nil {
				t.Fatal(err)
			}
			if !ledger.closedNative.Load() || ledger.viewPins.Load() != 0 {
				t.Fatal("last cursor leaked generation")
			}
			if _, _, err := copy.Get(t.Context(), "action"); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("closed cursor still readable")
			}
		})
	}
}

func TestActionLedgerPaginationBoundsConcurrentInsertion(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a"), testActionRow("m"), testActionRow("z")}); err != nil {
		t.Fatal(err)
	}
	view, err := ledger.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	rows := make([]actionLedgerRow, 0, 1500)
	for i := range 1500 {
		rows = append(rows, testActionRow(fmt.Sprintf("b-%05d", i)))
	}
	if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, rows); err != nil {
		t.Fatal(err)
	}
	var ids []string
	after := ""
	pages := 0
	for {
		page, next, err := view.Page(t.Context(), "monitor", after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			ids = append(ids, row.Action.ID)
		}
		pages++
		if next == "" {
			break
		}
		if next <= after || pages > 10 {
			t.Fatal("cursor failed to advance")
		}
		after = next
	}
	if fmt.Sprint(ids) != "[a m z]" || pages < 4 {
		t.Fatalf("snapshot pagination: %v, %d pages", ids, pages)
	}
}

func TestActionLedgerConcurrentPagesAndClose(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
		t.Fatal(err)
	}
	view, err := ledger.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 50 {
				_, _, err := view.Page(t.Context(), "", "", 100)
				if err != nil && !errors.Is(err, errCollectionLedgerClosed) {
					t.Error(err)
					return
				}
			}
		})
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if ledger.viewPins.Load() != 0 || !ledger.closedNative.Load() {
		t.Fatal("generation leaked")
	}
}

func TestActionLedgerCorruptionIsNotMissingEvidence(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
		t.Fatal(err)
	}
	view, err := ledger.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := ledger.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(actionRowsBucket).Put(actionVersionKey("a", 1), []byte("corrupt"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := view.Get(t.Context(), "a"); !errors.Is(err, errActionLedger) {
		t.Fatalf("corruption treated as absence: %v", err)
	}
}
