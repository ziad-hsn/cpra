package persistence

import (
	"fmt"
	"testing"
	"time"
)

func TestDelayedReviewFactCannotRestoreStaleHealthyPayload(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, closure, recoveryID, at := closureHealthFixture(t, "blue", NotificationConditionFormatVersion)
			policies := testReviewPolicy(t, ledger, &m)
			alert := m.Notifications.ReviewAlerts[closure.ReviewNotice.AlertID]
			key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
			fact := reviewFact{ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), Phase: "conditions", Revision: 1}
			var actions []actionLedgerRow
			for _, action := range m.Actions {
				actions = append(actions, actionLedgerRow{MonitorID: m.ID, Action: action})
			}
			reviews := append(policies,
				reviewRecord{Key: key, MonitorID: m.ID, Fact: &fact},
				reviewRecord{Key: reviewAlertKey(m.ID, alert.ID), MonitorID: m.ID, Alert: &alert})
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, actions, reviews); err != nil {
				t.Fatal(err)
			}
			m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_missing")})
			f := machine{collections: ledger, image: image{Version: NotificationConditionFormatVersion, Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{Sequence: 1}}}
			r := f.applyReviewItem(Command{Kind: "action_reconcile", MonitorID: m.ID, Revision: key, Generation: 1, At: at.Add(2 * time.Second)})
			if r.Err != nil || !r.Allowed || r.Monitor == nil {
				t.Fatal("delayed assessment", r.Err)
			}
			got := r.Monitor.Actions[closure.ID]
			condition := r.Monitor.Notifications.ReviewAlerts[alert.ID]
			if got.State != Queued || got.ReviewNotice.Health != "unknown" || condition.Health != "unknown" ||
				got.Deadline != closure.Deadline || got.DeliveryID != closure.DeliveryID || condition.RecoveredAt != alert.RecoveredAt ||
				!r.Monitor.Actions[recoveryID].Held() || r.Monitor.observedHealth() != "healthy" {
				t.Fatal("old fact rewrote availability, recovery evidence, timing or action hold")
			}
		})
	}
}
