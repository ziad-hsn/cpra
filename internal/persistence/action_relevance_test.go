package persistence

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestActionRelevanceSkipsOrdinaryHistory(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, count := range []int{0, 500, 5000} {
			t.Run(fmt.Sprintf("disk=%v/history=%d", disk, count), func(t *testing.T) {
				ledger := newLedgerTest(t, disk, 1<<24)
				rows := make([]actionLedgerRow, 0, count+1)
				for n := 0; n < count; n++ {
					row := testActionRow(fmt.Sprintf("completed-%05d", n))
					row.Action.State = Succeeded
					rows = append(rows, row)
				}
				rows = append(rows, testActionRow("unknown"))
				if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, rows); err != nil {
					t.Fatal(err)
				}
				pin, err := ledger.pinActions()
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				page, _, err := pin.relevantPage(t.Context(), "monitor", "", 1, 1<<20)
				if err != nil || len(page) != 1 || page[0].Action.ID != "unknown" {
					t.Fatalf("one candidate visit must reach the hold regardless of ordinary history: rows=%v err=%v", page, err)
				}
			})
		}
	}
}

func TestActionRelevancePreservesCapturedHoldAcrossReviewAndSnapshot(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m := testMonitor()
			m.ID = "monitor"
			m.Actions = nil
			policy := testReviewPolicy(t, ledger, &m)
			at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
			fact := reviewRecord{Key: key, MonitorID: m.ID, Fact: &reviewFact{IndexVersion: 1, ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), Phase: "actions", Revision: 1}}
			row := testActionRow("held")
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}, append(policy, fact)); err != nil {
				t.Fatal(err)
			}
			row.Action.Review = &ActionReview{Resolution: "accepted", Revision: "assertion"}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			frozen, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			var stream bytes.Buffer
			if err := writeActionLedger(&stream, frozen); err != nil {
				t.Fatal(err)
			}
			if err := frozen.Close(); err != nil {
				t.Fatal(err)
			}
			restored := newLedgerTest(t, disk, 1<<20)
			if _, err := importActionLedger(&stream, restored); err != nil {
				t.Fatal(err)
			}
			pin, err := restored.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			pin.sequence = 1
			page, _, err := pin.relevantPage(t.Context(), m.ID, "", 1, 1<<20)
			pin.Close()
			if err != nil || len(page) != 1 || !page[0].Action.Held() {
				t.Fatalf("captured hold lost after review/restore: %v %v", page, err)
			}
			if _, err := restored.CommitActionState(actionLedgerPosition{Index: 3}, nil, []reviewRecord{{Key: key, MonitorID: m.ID, Deleted: true}}); err != nil {
				t.Fatal(err)
			}
			pin, err = restored.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			page, next, err := pin.relevantPage(t.Context(), m.ID, "", 1, 1<<20)
			pin.Close()
			if err != nil || len(page) != 0 || next != "" {
				t.Fatalf("completed fact retained an obsolete candidate: %v %s %v", page, next, err)
			}
		})
	}
}
