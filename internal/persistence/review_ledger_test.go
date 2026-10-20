package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestReviewAssessmentDetachesCurrentDelivery(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, _, at := unknownRecoveryWithBlue(t)
			opening := notificationByColor(t, m, "blue", Queued)
			m = policyStart(t, m, opening, at.Add(time.Millisecond))
			m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
			m = policyPulse(t, m, at.Add(time.Second), "success")
			m = policyPulse(t, m, at.Add(2*time.Second), "success")
			closure := notificationByColor(t, m, "blue", Queued).Clone()
			policy := testReviewPolicy(t, ledger, &m)
			var rows []actionLedgerRow
			for _, a := range m.Actions {
				rows = append(rows, actionLedgerRow{MonitorID: m.ID, Action: a})
			}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, rows, policy); err != nil {
				t.Fatal(err)
			}
			before := m.Clone()
			f := machine{collections: ledger, image: image{Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{Sequence: 1}}}
			observed := m.Clone()
			observed.LastOutcome = "failure"
			fact := reviewFact{PolicyID: m.ReviewPolicyID, Fence: 1, At: at.Add(3 * time.Second), Context: reviewContext(observed, at.Add(3*time.Second))}
			alert := m.Notifications.ReviewAlerts[closure.ReviewNotice.AlertID]
			input, err := f.assessmentInput(fact, alert)
			if err != nil {
				t.Fatal(err)
			}
			input.assessReviewAlert(alert, fact.At, func(string, *Action) {})
			updated := input.Actions[closure.ID]
			if updated.ReviewNotice.Health != "unhealthy" || !updated.Deadline.Equal(closure.Deadline) {
				t.Fatal("assessment lost current health or original deadline")
			}
			if !reflect.DeepEqual(f.image.Monitors[m.ID], before) {
				t.Fatal("assessment mutated the authoritative projection before installation")
			}
		})
	}
}

func TestReviewAssessmentRejectsReboundHistoricalRevision(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, action, at := unknownRecoveryWithBlue(t)
			m.Revision, action.Revision = "revision-a", "revision-a"
			m.Actions, m.Notifications = nil, nil
			policy := testReviewPolicy(t, ledger, &m)
			key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
			fact := reviewRecord{Key: key, MonitorID: m.ID, Fact: &reviewFact{ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), SourceAction: action.ID, Phase: "actions", Revision: 1}}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, []actionLedgerRow{{MonitorID: m.ID, Action: action}}, append(policy, fact)); err != nil {
				t.Fatal(err)
			}
			// Make the corrupted row older than the last command digest. Equal-length
			// canonical substitution preserves storage accounting and the row key.
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{testActionRow("unrelated")}); err != nil {
				t.Fatal(err)
			}
			policyKey := reviewPolicyKey(m.ID, m.ReviewPolicyID, -1)
			row, ok, err := ledger.reviewRecord(policyKey)
			if err != nil || !ok {
				t.Fatal(err)
			}
			row.Policy.Revision = "revision-b"
			data, err := encodeReviewRecord(row)
			if err != nil {
				t.Fatal(err)
			}
			if disk {
				if err := ledger.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(reviewRowsBucket).Put([]byte(policyKey), data) }); err != nil {
					t.Fatal(err)
				}
			} else {
				ledger.actions.reviews[policyKey] = data
			}
			m.Revision = "revision-b"
			f := machine{collections: ledger, image: image{Monitors: map[string]Monitor{m.ID: m}}}
			result := f.applyReviewStep(Command{MonitorID: m.ID, Revision: key, Generation: 1, At: at})
			if !errors.Is(result.Err, errActionLedger) || len(f.reviewWrites) != 0 || len(f.actionWrites) != 0 {
				t.Fatal("old assessment was rebound or advanced", result.Err)
			}
			saved, _, err := ledger.reviewRecord(key)
			if err != nil || saved.Fact.Revision != 1 || saved.Fact.After != "" {
				t.Fatal("failed assessment changed progress", err)
			}
		})
	}
}

func TestReviewLedgerAtomicFactsAndSnapshotFences(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m := testMonitor()
			m.ID = "monitor"
			m.Actions = nil
			policy := testReviewPolicy(t, ledger, &m)
			key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000001"
			at := time.Now().UTC()
			fact := reviewRecord{Key: key, MonitorID: m.ID, Fact: &reviewFact{ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), Phase: "actions", Revision: 1}}
			row := testActionRow("a")
			seq, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}, append(policy, fact))
			if err != nil || seq != 1 {
				t.Fatal(seq, err)
			}
			row.Action.Review = &ActionReview{Resolution: "accepted", Revision: "assertion"}
			if _, err = ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			fact.Fact.Revision++
			fact.Fact.After = "a"
			if _, err = ledger.CommitActionState(actionLedgerPosition{Index: 3}, nil, []reviewRecord{fact}); err != nil {
				t.Fatal(err)
			}
			if seq, err = ledger.CommitActionState(actionLedgerPosition{Index: 3}, nil, []reviewRecord{fact}); err != nil || seq != 3 {
				t.Fatal("duplicate progress", seq, err)
			}
			frozen, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Close()
			var stream bytes.Buffer
			if err = writeActionLedger(&stream, frozen); err != nil {
				t.Fatal(err)
			}
			restored := newLedgerTest(t, disk, 1<<20)
			if _, err = importActionLedger(&stream, restored); err != nil {
				t.Fatal("restore", err)
			}
			pin, err := restored.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			latest, ok, err := pin.Get(t.Context(), "a")
			if err != nil || !ok || latest.Action.Review == nil {
				t.Fatal("latest head lost", err)
			}
			pin.sequence = 1
			old, ok, err := pin.Get(t.Context(), "a")
			if err != nil || !ok || old.Action.Review != nil {
				t.Fatal("fenced version lost", err)
			}
			saved, ok, err := restored.reviewRecord(key)
			if err != nil || !ok || saved.Fact.Revision != 2 || saved.Fact.After != "a" {
				t.Fatal("progress lost", err)
			}
		})
	}
}

func TestReviewSnapshotRequiresHistoricalVersionCoverage(t *testing.T) {
	for _, damage := range []string{"none", "missing", "monitor", "evidence"} {
		t.Run(damage, func(t *testing.T) {
			ledger := newLedgerTest(t, false, 1<<20)
			m := testMonitor()
			m.ID = "monitor"
			m.Actions = nil
			policy := testReviewPolicy(t, ledger, &m)
			at := time.Now().UTC()
			a := Action{ID: "retained", Revision: m.Revision, Kind: "intervention", Attempt: 1, State: Unknown, Outcome: "failure", NotBefore: at, StartedAt: at, FinishedAt: at.Add(time.Millisecond)}
			key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
			fact := reviewRecord{Key: key, MonitorID: m.ID, Fact: &reviewFact{ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), Phase: "actions", Revision: 1}}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, []actionLedgerRow{{MonitorID: m.ID, Action: a}}, append(policy, fact)); err != nil {
				t.Fatal(err)
			}
			a.FinishedAt = at.Add(2 * time.Millisecond)
			seq, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{{MonitorID: m.ID, Action: a}})
			if err != nil {
				t.Fatal(err)
			}
			view, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			// Damage only an older frozen version. Export computes a valid framing checksum;
			// latest-head digests and summaries still match, so semantic joins must fail.
			switch damage {
			case "missing":
				view.actions.versions[a.ID] = view.actions.versions[a.ID][1:]
			case "monitor", "evidence":
				row, err := decodeActionRow(view.actions.versions[a.ID][0])
				if err != nil {
					t.Fatal(err)
				}
				if damage == "monitor" {
					row.MonitorID = "different-monitor"
				} else {
					row.Action.ExecutorSession = "unbound-executor"
				}
				data, err := encodeActionRow(row)
				if err != nil {
					t.Fatal(err)
				}
				view.actions.versions[a.ID][0] = data
			}
			var stream bytes.Buffer
			if err := writeActionLedger(&stream, view); err != nil {
				t.Fatal("transport export", err)
			}
			restored := newLedgerTest(t, false, 1<<20)
			if _, err := importActionLedger(&stream, restored); err != nil {
				if damage == "none" {
					t.Fatal("valid stream rejected", err)
				}
				return // Structural predecessor validation may reject before the image join.
			}
			frozen, err := restored.Freeze()
			if err != nil {
				if damage == "none" {
					t.Fatal(err)
				}
				return
			}
			defer frozen.Close()
			i := image{Version: ActionFormatVersion, Index: 2, Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{Sequence: seq, Position: actionLedgerPosition{Index: 2}}}
			err = validateActionStorageRows(i, frozen)
			if damage == "none" && err != nil {
				t.Fatal("valid pending fence rejected", err)
			}
			if damage != "none" && err == nil {
				t.Fatal("incomplete or forged historical evidence accepted")
			}
		})
	}
}

func testReviewPolicy(t *testing.T, ledger *collectionLedger, m *Monitor) []reviewRecord {
	t.Helper()
	f := machine{collections: ledger}
	if err := f.captureReviewPolicy(m); err != nil {
		t.Fatal(err)
	}
	var records []reviewRecord
	for _, r := range f.reviewWrites {
		records = append(records, r)
	}
	return records
}

func TestReviewPolicyRowsAreImmutableAfterCommit(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m := testMonitor()
			m.Policy.Endpoints = map[string]int{"blue": 1}
			rows := testReviewPolicy(t, ledger, &m)
			position := actionLedgerPosition{Index: 1}
			if _, err := ledger.CommitActionState(position, nil, rows); err != nil {
				t.Fatal(err)
			}
			if seq, err := ledger.CommitActionState(position, nil, rows); err != nil || seq != 1 {
				t.Fatal("exact-position retry failed", seq, err)
			}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 2}, nil, rows); err == nil {
				t.Fatal("immutable policy was rewritten at a new position")
			}
			deleted := reviewRecord{Key: rows[0].Key, MonitorID: m.ID, Deleted: true}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 2}, nil, []reviewRecord{deleted}); err == nil {
				t.Fatal("referenced policy could be deleted")
			}
			saved, ok, err := ledger.reviewRecord(rows[0].Key)
			if err != nil || !ok || saved.Sequence != 1 || saved.Policy == nil {
				t.Fatal("rejected replacement changed the original", err)
			}
		})
	}
}

func TestReviewReadRejectsSwappedAndFuturePolicyRows(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, damage := range []string{"swapped", "future"} {
			t.Run(fmt.Sprint(disk)+"/"+damage, func(t *testing.T) {
				ledger := newLedgerTest(t, disk, 1<<20)
				m := testMonitor()
				m.Policy.Endpoints = map[string]int{"blue": 1}
				rows := testReviewPolicy(t, ledger, &m)
				if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, rows); err != nil {
					t.Fatal(err)
				}
				key := rows[0].Key
				row, _, err := ledger.reviewRecord(key)
				if err != nil {
					t.Fatal(err)
				}
				if damage == "swapped" {
					row.Policy.ID = identity("different-policy")
					row.Key = reviewPolicyKey(m.ID, row.Policy.ID, -1)
				} else {
					row.Sequence++
				}
				data, err := encodeReviewRecord(row)
				if err != nil {
					t.Fatal(err)
				}
				if disk {
					err = ledger.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(reviewRowsBucket).Put([]byte(key), data) })
				} else {
					ledger.actions.reviews[key] = data
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := ledger.reviewRecord(key); err == nil {
					t.Fatal("exact lookup accepted a foreign policy or future commit")
				}
			})
		}
	}
}

func TestReviewReadRejectsPartialActionNamespace(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	if err := ledger.db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucket(reviewRowsBucket); return err }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.reviewRecord(reviewPolicyKey("monitor", identity("policy"), -1)); err == nil {
		t.Fatal("partial namespace was mistaken for a collection-only store")
	}
}
