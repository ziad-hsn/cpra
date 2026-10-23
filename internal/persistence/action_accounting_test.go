package persistence

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestActionAccountingObservationIsOnlyInPublishedSample(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("held")}); err != nil {
				t.Fatal(err)
			}
			store := &Store{fsm: &machine{collections: ledger}}
			before := time.Now().UTC()
			if err := store.sampleActionAccounting(1, 12, "below_threshold"); err != nil {
				t.Fatal(err)
			}
			sample := store.actionAccounting.Load()
			if sample == nil || sample.ObservedAt.IsZero() || sample.ObservedAt.Before(before) || sample.ObservedAt.After(time.Now().UTC()) || sample.ObservedAt.Location() != time.UTC {
				t.Fatal("published accounting lacks its UTC observation time")
			}
			if sample.Protected != 1 || sample.CleanupBlocked != 1 || sample.CleanupBlockedBytes != 12 {
				t.Fatal("sample lost action or selection accounting")
			}
			stored, err := ledger.actionAccounting()
			if err != nil || !stored.ObservedAt.IsZero() || stored.CleanupBlocked != 0 {
				t.Fatal("an operational sample modified deterministic ledger accounting", err)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{testActionRow("another-held")}); err != nil {
				t.Fatal(err)
			}
			if current := store.actionAccounting.Load(); current != sample || current.Protected != 1 || current.ObservedAt != sample.ObservedAt {
				t.Fatal("a ledger update falsely refreshed the previously published sample")
			}
			if err := store.sampleActionAccounting(0, 0, "below_threshold"); err != nil {
				t.Fatal(err)
			}
			fresh := store.actionAccounting.Load()
			if fresh == nil || fresh == sample || fresh.Protected != 2 || fresh.ObservedAt.Before(sample.ObservedAt) {
				t.Fatal("a new sample failed to publish current accounting and observation time")
			}
			stored, err = ledger.actionAccounting()
			if err != nil || !stored.ObservedAt.IsZero() || stored.Protected != 2 {
				t.Fatal("sample publication contaminated persisted row updates", err)
			}
		})
	}
}

func TestActionDiskAccountingRejectsObservationTimestamp(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("held")}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.Update(func(tx *bolt.Tx) error {
		state, err := actionDiskAccounting(tx)
		if err != nil {
			return err
		}
		state.ObservedAt = time.Now().UTC()
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return tx.Bucket(actionMetaBucket).Put([]byte("accounting"), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.actionAccounting(); !errors.Is(err, errActionLedger) {
		t.Fatal("persisted observational timestamp was accepted as deterministic accounting", err)
	}
}
