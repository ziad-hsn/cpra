package persistence

import (
	"reflect"
	"testing"
	"time"
)

// This exercises committed commands, the action ledger and retained history.
// It constructs no controller, scheduler or provider transport.
func TestNotificationResolutionCancellationSurvivesStoreSnapshot(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		name := "before-recovery"
		if recovered {
			name = "recovered-during-maintenance"
		}
		t.Run(name, func(t *testing.T) {
			config := testConfig(t)
			store, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			apply := func(command Command) Monitor {
				t.Helper()
				result := submit(t, store, command)[0]
				if result.Err != nil || result.Monitor == nil {
					t.Fatalf("%s did not commit a monitor transition: %v", command.Kind, result.Err)
				}
				return *result.Monitor
			}
			input := testMonitor()
			input.Policy = lifecyclePolicy()
			current := apply(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: time.Now().UTC()})
			current = apply(Command{Kind: "pulse", MonitorID: current.ID, Revision: current.Revision,
				Generation: current.Generation + 1, Outcome: "failure", At: time.Now().UTC()})
			opening := notificationByColor(t, current, "red", Queued)
			execution, err := store.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: current.ID,
				Revision: current.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), At: time.Now().UTC()})
			if execution != nil {
				defer func() { _ = execution.Finish(t.Context()) }()
			}
			if err != nil {
				t.Fatal(err)
			}
			current = apply(Command{Kind: "result", MonitorID: current.ID, Revision: current.Revision,
				ActionID: opening.ID, Outcome: "success", At: time.Now().UTC()})
			if err := execution.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			opening = snapshotAction(t, store.fsm.collections, opening.ID)
			if opening.State != Succeeded || opening.StartedAt.IsZero() || opening.ExecutorFinishedAt.IsZero() || !ordinaryTerminalAction(opening) {
				t.Fatal("fixture did not retain a completed, fenced ordinary opening")
			}
			if recovered {
				at := time.Now().UTC()
				input.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(-time.Second), End: at.Add(time.Hour)}}
				current = apply(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: at})
				for range input.Policy.Healthy {
					current = apply(Command{Kind: "pulse", MonitorID: current.ID, Revision: current.Revision,
						Generation: current.Generation + 1, Outcome: "success", Maintenance: true, At: time.Now().UTC()})
				}
			}
			if !current.notificationConditionsEnabled() || len(current.Notifications.Resolutions) != 1 {
				t.Fatal("fixture lost its pending closure condition")
			}
			var condition NotificationResolution
			for _, value := range current.Notifications.Resolutions {
				condition = value
			}
			if condition.ActionID != "" || condition.Started || !condition.ExecutionDeadline.IsZero() || !condition.RecoveredAt.IsZero() != recovered {
				t.Fatal("fixture manufactured closure execution or did not record the selected recovery state")
			}
			blocked := func() bool {
				t.Helper()
				store.fsm.mu.Lock()
				defer store.fsm.mu.Unlock()
				value, err := store.fsm.actionRetirementBlocked(store.fsm.image.Monitors[input.ID], opening)
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
			if !current.notificationRetainsAction(opening.ID) || !blocked() {
				t.Fatal("pending closure did not protect its opening dependency")
			}
			input.Revision = "replacement"
			cancelledAt := time.Now().UTC()
			apply(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: cancelledAt})
			var firstCancellation Event
			assertStored := func() {
				t.Helper()
				current, exists := store.Get(input.ID)
				if !exists || current.Revision != input.Revision || current.Notifications == nil || len(current.Notifications.Resolutions) != 0 || current.notificationRetainsAction(opening.ID) || blocked() {
					t.Fatal("obsolete condition still occupies operational state or pins ordinary history")
				}
				if got := snapshotAction(t, store.fsm.collections, opening.ID); !reflect.DeepEqual(got, opening) {
					t.Fatal("condition cancellation changed retained opening facts")
				}
				retained, exists := monitorWithActionHistory(t, store, input.ID)
				if !exists || len(retained.Actions) != 1 {
					t.Fatal("condition cancellation manufactured an action or lost the original opening")
				}
				history, err := store.History().Page(input.ID, "", 100)
				if err != nil {
					t.Fatal(err)
				}
				starts, cancellations := 0, 0
				for _, event := range history.Events {
					if event.Type == "action_started" {
						starts++
						if event.ActionID != opening.ID {
							t.Fatal("history contains a manufactured closure start")
						}
					}
					if event.Type != "notification_resolution_cancelled" {
						continue
					}
					cancellations++
					if event.ID == "" || event.ConditionID != condition.ID || event.OpeningActionID != opening.ID ||
						event.Revision != opening.Revision || event.CatalogUID != opening.CatalogUID || event.FileIncarnation != opening.FileIncarnation ||
						event.IncidentID != opening.IncidentID || event.NotificationEpisode != condition.Episode || event.EndpointID != condition.EndpointID ||
						event.ActionID != "" || event.DeliveryID != "" || event.Resolution != nil || !event.At.Equal(cancelledAt) ||
						event.Reason != "configuration_changed" || event.Outcome != "configuration_changed" {
						t.Fatal("condition cancellation lost original ownership or invented delivery/recovery facts")
					}
					if firstCancellation.ID == "" {
						firstCancellation = event
					} else if !reflect.DeepEqual(event, firstCancellation) {
						t.Fatal("snapshot/restart rewrote the committed cancellation event")
					}
				}
				if cancellations != 1 || starts != 1 || history.NextCursor != "" {
					t.Fatal("history duplicated or omitted condition cancellation/execution evidence")
				}
			}
			assertStored()
			if err := store.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			assertStored()
			// A later observation must not turn the absence of the cancelled
			// condition into permission to close its episode at a new destination.
			for range input.Policy.Healthy {
				current, _ = store.Get(input.ID)
				apply(Command{Kind: "pulse", MonitorID: current.ID, Revision: current.Revision,
					Generation: current.Generation + 1, Outcome: "success", At: time.Now().UTC()})
			}
			assertStored()
		})
	}
}
