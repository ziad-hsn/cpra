package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNotificationReviewDetailShowsSuppressedClosure(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			config := testConfig(t)
			if !disk {
				config.Storage.Mode = "memory"
			}
			store, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			apply := func(c Command) Monitor {
				t.Helper()
				result := submit(t, store, c)[0]
				if result.Err != nil || result.Monitor == nil || c.Kind == "start" && !result.Allowed {
					t.Fatalf("%s: %v", c.Kind, result.Err)
				}
				return *result.Monitor
			}
			at := time.Now().UTC()
			input := testMonitor()
			input.Policy = lifecyclePolicy()
			input.Policy.Intervention, input.Policy.Endpoints = true, map[string]int{"blue": 1}
			input.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(time.Second), End: at.Add(time.Hour)}}
			m := apply(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: at})
			m = apply(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "failure", At: at})
			recovery := interventionAction(t, m)
			apply(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, ExecutorSession: store.executorSession, At: at})
			apply(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, Outcome: "failure", Ambiguous: true, At: at.Add(time.Millisecond)})
			m = reconcileReviewFacts(t, store, m.ID, at.Add(time.Millisecond))
			opening := notificationByColor(t, m, "blue", Queued)
			apply(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), ExecutorSession: store.executorSession, At: at.Add(2 * time.Millisecond)})
			m = apply(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, Outcome: "success", At: at.Add(3 * time.Millisecond)})
			for n := 2; n < 2+input.Policy.Healthy; n++ {
				m = apply(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", Maintenance: true, At: at.Add(time.Duration(n) * time.Second)})
			}
			m = reconcileReviewFacts(t, store, m.ID, at.Add(5*time.Second))
			if m.ActionProjection == nil || len(m.Notifications.ReviewAlerts) != 0 || queuedColorCount(m, "blue") != 0 {
				t.Fatal("fixture did not detach its suppressed blue closure")
			}
			_, incomplete := m.NotificationConditions()
			if !incomplete {
				t.Fatal("fleet conditions falsely claimed a complete projection")
			}
			before := time.Now().UTC()
			view, found, err := store.NotificationConditionsContext(t.Context(), m.ID)
			if err != nil || !found || view.Truncated || len(view.Items) != 1 || view.Sequence == 0 || view.ObservedAt.Before(before) || view.ObservedAt.After(time.Now().UTC()) {
				t.Fatalf("detail observation: found=%v err=%v view=%+v", found, err, view)
			}
			condition := view.Items[0]
			if condition.Kind != "review_closure" || condition.Color != "blue" || condition.State != "suppressed" || condition.Reason != "suppressed" ||
				condition.OriginalActionID != recovery.ID || condition.RecoveredAt.IsZero() || condition.PendingAt.IsZero() || condition.RecoveredAt.Location() != time.UTC {
				t.Fatal("missing or manufactured closure facts", condition)
			}
			if !retainedAction(t, store, recovery.ID).Held() || retainedAction(t, store, opening.ID).State != Succeeded {
				t.Fatal("observation changed retained provider facts")
			}
			if view.IncarnationID != m.IncarnationID() || view.Revision != m.Revision || view.ControlRevision != m.ControlRevision || view.IncidentRevision != m.IncidentRevision {
				t.Fatal("condition sample lost its monitor fence")
			}
		})
	}
}

func reviewObservationFixture(t *testing.T) (Monitor, ReviewAlert) {
	t.Helper()
	m, _, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(time.Second), End: at.Add(time.Hour)}}
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	alert := m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID]
	alert.Owner = &reviewAlertOwner{CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation}
	if alert.ClosedAt.IsZero() || !alert.ClosureSuppressed || alert.ClosingDelivery != "" {
		t.Fatal("fixture closure is not pending suppression")
	}
	return m, alert
}

func TestNotificationReviewObservationUsesCurrentTransaction(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, alert := reviewObservationFixture(t)
			record := reviewRecord{Key: reviewAlertKey(m.ID, alert.ID), MonitorID: m.ID, Alert: &alert}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, []reviewRecord{record}); err != nil {
				t.Fatal(err)
			}
			pin, err := ledger.pinCurrentObservation(1)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			alert.ClosureBlocked = "configuration_changed"
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 2}, nil, []reviewRecord{record}); err != nil {
				t.Fatal(err)
			}
			// A generation pin is not MVCC for mutable review records.
			view, err := pin.currentReviewConditions(t.Context(), m, nil)
			if err != nil || view.Sequence != 2 || len(view.Items) != 1 || view.Items[0].State != "blocked" || view.Items[0].Reason != "configuration_changed" {
				t.Fatalf("used pinned historical progress for a current read: %+v %v", view, err)
			}
			if err := ledger.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := pin.currentReviewConditions(t.Context(), m, nil); err != nil || ledger.closedNative.Load() {
				t.Fatal("owner closure reclaimed a pinned generation", err)
			}
			if err := pin.Close(); err != nil || !ledger.closedNative.Load() {
				t.Fatal("last observation pin did not release its generation", err)
			}
		})
	}
}

func TestNotificationReviewObservationPreservesOldOwnership(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, alert := reviewObservationFixture(t)
			m.FileIncarnation = "original-owner"
			alert.Owner.FileIncarnation = m.FileIncarnation
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, []reviewRecord{{Key: reviewAlertKey(m.ID, alert.ID), MonitorID: m.ID, Alert: &alert}}); err != nil {
				t.Fatal(err)
			}
			pin, err := ledger.pinCurrentObservation(1)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			for _, change := range []string{"config", "owner", "adopted"} {
				owner := m
				want := "configuration_changed"
				switch change {
				case "config":
					owner.Revision = "replacement"
				case "owner":
					owner.FileIncarnation = "replacement-owner"
					want = "incarnation_changed"
				case "adopted":
					owner.CatalogUID = "adopted-owner"
					original := m.FileIncarnation
					owner.AdoptedFileIncarnation = &original
					want = "suppressed"
				}
				view, err := pin.currentReviewConditions(t.Context(), owner, nil)
				if err != nil || len(view.Items) != 1 || view.Items[0].Reason != want || view.Items[0].OriginalActionID != alert.ActionID || view.Items[0].RecoveredAt != alert.RecoveredAt {
					t.Fatalf("%s rebound or hid historical condition: %+v %v", change, view, err)
				}
			}
		})
	}
}

func TestNotificationReviewObservationBounds(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, oversized := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/bytes=%t", disk, oversized), func(t *testing.T) {
				ledger := newLedgerTest(t, disk, 1<<24)
				m, alert := reviewObservationFixture(t)
				var rows []reviewRecord
				count := notificationConditionLimit + 1
				if oversized {
					count = 1
					alert.Revision = strings.Repeat("r", notificationReviewBytes)
				}
				for n := range count {
					a := alert
					a.ID = fmt.Sprintf("review-%03d", n)
					rows = append(rows, reviewRecord{Key: reviewAlertKey(m.ID, a.ID), MonitorID: m.ID, Alert: &a})
				}
				if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, rows); err != nil {
					t.Fatal(err)
				}
				pin, err := ledger.pinCurrentObservation(1)
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				view, err := pin.currentReviewConditions(t.Context(), m, nil)
				want := notificationConditionLimit
				if oversized {
					want = 0
				}
				if err != nil || !view.Truncated || len(view.Items) != want || view.Sequence != 1 {
					t.Fatalf("bound not represented honestly: count=%d truncated=%v sequence=%d err=%v", len(view.Items), view.Truncated, view.Sequence, err)
				}
			})
		}
	}
}

func TestNotificationReviewObservationUsesLatestDeliveryAttempt(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			m, _, at := unknownRecoveryWithBlue(t)
			first := notificationByColor(t, m, "blue", Queued)
			m = policyStart(t, m, first, at.Add(time.Millisecond))
			m = policyOutcome(t, m, first, at.Add(2*time.Millisecond), "failure", false, true)
			latest := notificationByColor(t, m, "blue", Queued)
			if latest.Attempt != 2 || latest.DeliveryID != first.DeliveryID {
				t.Fatal("fixture did not create the bounded retry")
			}
			alert := m.Notifications.ReviewAlerts[first.ReviewNotice.AlertID]
			alert.Owner = &reviewAlertOwner{CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation}
			var rows []actionLedgerRow
			for _, a := range m.Actions {
				rows = append(rows, actionLedgerRow{MonitorID: m.ID, Action: a})
			}
			if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, rows, []reviewRecord{{Key: reviewAlertKey(m.ID, alert.ID), MonitorID: m.ID, Alert: &alert}}); err != nil {
				t.Fatal(err)
			}
			pin, err := ledger.pinCurrentObservation(1)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			view, err := pin.currentReviewConditions(t.Context(), m, nil)
			if err != nil || len(view.Items) != 1 || view.Items[0].State != string(Queued) {
				t.Fatal("displayed the earlier rejection instead of the pending retry", view, err)
			}
			encoded, _ := json.Marshal(view)
			if strings.Contains(string(encoded), "EndpointID") || strings.Contains(string(encoded), "NotificationCorrelation") || strings.Contains(string(encoded), "Policy") {
				t.Fatal("condition observation exposed private destination configuration")
			}
		})
	}
}

func TestNotificationReviewDetailShowsStartedClosureAndSafeRetry(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			config := testConfig(t)
			if !disk {
				config.Storage.Mode = "memory"
			}
			store, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			apply := func(c Command) Monitor {
				t.Helper()
				result := submit(t, store, c)[0]
				if result.Err != nil || result.Monitor == nil || c.Kind == "start" && !result.Allowed {
					t.Fatalf("%s: %v", c.Kind, result.Err)
				}
				return *result.Monitor
			}
			at := time.Now().UTC()
			input := testMonitor()
			input.Policy = lifecyclePolicy()
			input.Policy.Intervention, input.Policy.Endpoints = true, map[string]int{"blue": 1}
			m := apply(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: at})
			m = apply(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "failure", At: at})
			recovery := interventionAction(t, m)
			apply(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, ExecutorSession: store.executorSession, At: at})
			apply(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, Outcome: "failure", Ambiguous: true, At: at.Add(time.Millisecond)})
			m = reconcileReviewFacts(t, store, m.ID, at.Add(time.Millisecond))
			opening := notificationByColor(t, m, "blue", Queued)
			apply(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), ExecutorSession: store.executorSession, At: at.Add(2 * time.Millisecond)})
			m = apply(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, Outcome: "success", At: at.Add(3 * time.Millisecond)})
			for n := 2; n < 2+input.Policy.Healthy; n++ {
				m = apply(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", At: at.Add(time.Duration(n) * time.Second)})
			}
			m = reconcileReviewFacts(t, store, m.ID, at.Add(4*time.Second))
			closure := notificationByColor(t, m, "blue", Queued)
			assertCondition := func(state ActionState) {
				t.Helper()
				view, found, err := store.NotificationConditionsContext(t.Context(), m.ID)
				if err != nil || !found || view.Truncated || len(view.Items) != 1 || view.Items[0].Kind != "review_closure" || view.Items[0].State != string(state) || view.Items[0].OriginalActionID != recovery.ID {
					t.Fatalf("closure %s is missing, duplicated or stale: %+v found=%v err=%v", state, view, found, err)
				}
			}
			// Both relevant-condition and active-execution membership exist until
			// the first start. Their union must contain this condition only once.
			assertCondition(Queued)
			apply(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: closure.ID, NoticeRevision: ActionNoticeRevision(closure), ExecutorSession: store.executorSession, At: at.Add(4 * time.Second)})
			assertCondition(Started)
			m = apply(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: closure.ID, Outcome: "failure", Retryable: true, At: at.Add(4*time.Second + time.Millisecond)})
			retry := notificationByColor(t, m, "blue", Queued)
			if retry.Attempt != 2 || retry.DeliveryID != closure.DeliveryID || retry.ReviewNotice == nil || retry.ReviewNotice.Phase != "close" {
				t.Fatal("fixture did not queue the existing logical closure's second attempt")
			}
			store.fsm.mu.RLock()
			ledger := store.fsm.collections
			store.fsm.mu.RUnlock()
			record, found, err := ledger.reviewRecord(reviewAlertKey(m.ID, closure.ReviewNotice.AlertID))
			if err != nil || !found || !record.Alert.ClosingStarted || len(reviewConditionKeys(record)) != 0 {
				t.Fatal("fixture did not leave the routine-assessment index", err)
			}
			assertCondition(Queued)
			if !retainedAction(t, store, recovery.ID).Held() || retry.Deadline != retainedAction(t, store, closure.ID).Deadline {
				t.Fatal("observation changed the held effect or closure budget")
			}
		})
	}
}

func TestNotificationReviewDetailActiveIndexCrowdingIsIncomplete(t *testing.T) {
	ledger := newLedgerTest(t, false, 1<<20)
	m := testMonitor()
	store := &Store{stop: make(chan struct{}), fsm: &machine{collections: ledger, image: image{Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{}}}}
	store.fsm.rebuildActionIndexes()
	for n := range notificationConditionLimit + 1 {
		store.fsm.indexAction(m.ID, Action{ID: fmt.Sprintf("a-%03d", n), Kind: "code", Color: "red", State: Queued})
	}
	store.fsm.indexAction(m.ID, Action{ID: "z-blue", Kind: "code", Color: "blue", State: Queued, ReviewNotice: &ReviewNotice{AlertID: "pending-closure", Phase: "close"}})
	view, found, err := store.NotificationConditionsContext(t.Context(), m.ID)
	if err != nil || !found || !view.Truncated || len(view.Items) != 0 {
		t.Fatalf("crowded active index falsely reported complete conditions: %+v %v", view, err)
	}
}

func TestNotificationReviewDetailRejectsMonitorChangeOutsideIOLock(t *testing.T) {
	for _, change := range []string{"generation", "control", "incident", "conditions", "ledger", "unrelated"} {
		t.Run(change, func(t *testing.T) {
			ledger := newLedgerTest(t, true, 1<<20)
			m := testMonitor()
			store := &Store{stop: make(chan struct{}), fsm: &machine{collections: ledger, image: image{Monitors: map[string]Monitor{m.ID: m}}}}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			ledger.mu.Lock()
			locked := true
			defer func() {
				if locked {
					ledger.mu.Unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				_, _, err := store.NotificationConditionsContext(ctx, m.ID)
				done <- err
			}()
			for ledger.viewPins.Load() == 0 && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			if ledger.viewPins.Load() == 0 {
				t.Fatal("read did not pin before database access")
			}
			// The pending database read must not keep either state lock held.
			unlock, err := store.lockStateContext(ctx, true)
			if err != nil {
				t.Fatal("database read retained the state locks", err)
			}
			switch change {
			case "generation":
				m.Generation++
			case "control":
				m.ControlRevision = "changed"
			case "incident":
				m.IncidentRevision = "changed"
			case "conditions":
				m.Notifications = &NotificationState{}
			case "ledger":
				store.fsm.collections = nil
			case "unrelated":
				store.fsm.image.Index++
			}
			store.fsm.image.Monitors[m.ID] = m
			unlock()
			ledger.mu.Unlock()
			locked = false
			err = <-done
			if change == "unrelated" && err != nil || change != "unrelated" && !errors.Is(err, ErrNotificationObservationChanged) {
				t.Fatalf("incorrect contention result for %s: %v", change, err)
			}
			if ledger.viewPins.Load() != 0 || store.fsm.err != nil || store.err != nil {
				t.Fatal("read contention leaked a pin or poisoned storage health")
			}
		})
	}
}

func TestNotificationReviewDetailCancellationReleasesPin(t *testing.T) {
	ledger := newLedgerTest(t, false, 1<<20)
	m := testMonitor()
	store := &Store{stop: make(chan struct{}), fsm: &machine{collections: ledger, image: image{Monitors: map[string]Monitor{m.ID: m}}}}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err := store.NotificationConditionsContext(ctx, m.ID)
	if !errors.Is(err, context.DeadlineExceeded) || ledger.viewPins.Load() != 0 || store.fsm.err != nil {
		t.Fatal("cancelled observation retained ownership or changed health", err)
	}
}
