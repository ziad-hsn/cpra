package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

// Every lifecycle transition in this fixture uses a supported format44
// envelope. Current-format conversion runs only after the original records
// have been committed and completed actions have left the hot projection.
func archivedNotificationFixture(t *testing.T, closing ActionState) (*Store, Monitor, Action, *Action) {
	t.Helper()
	return archivedNotificationFixtureOn(t, openReplayMemory(t), ActionFormatVersion, 0, closing)
}

func archivedNotificationFixtureOn(t *testing.T, s *Store, format, previous int, closing ActionState) (*Store, Monitor, Action, *Action) {
	t.Helper()
	input := testMonitor()
	input.Policy = lifecyclePolicy()
	at := policyTime()
	old := func(c Command) Monitor {
		t.Helper()
		if !actionStorageFormat(format) {
			c.NoticeRevision = ""
		}
		r := applyCatalogFormat(t, s, format, c)[0]
		if r.Err != nil || r.Monitor == nil {
			t.Fatal("historical lifecycle command failed", c.Kind, r.Err)
		}
		return *r.Monitor
	}
	m := old(Command{Kind: "configure", MonitorID: input.ID, Revision: input.Revision, Config: &input, At: at})
	for range previous {
		// Historical queued notices are cancelled before any start. They fill
		// the same monitor's retained index without creating provider effects.
		for _, outcome := range []string{"failure", "success", "success"} {
			at = at.Add(time.Second)
			m = old(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: outcome, At: at})
		}
	}
	m = old(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "failure", At: at.Add(time.Second)})
	opening := notificationByColor(t, m, "red", Queued)
	m = old(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), At: at.Add(2 * time.Second)})
	m = old(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, Outcome: "success", At: at.Add(3 * time.Second)})
	opening = m.Actions[opening.ID]
	var green *Action
	if closing != "" {
		for _, second := range []int{4, 5} {
			m = old(Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", At: at.Add(time.Duration(second) * time.Second)})
		}
		a := notificationByColor(t, m, "green", Queued)
		if closing != Queued {
			m = old(Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, NoticeRevision: ActionNoticeRevision(a), At: at.Add(6 * time.Second)})
			if closing != Started {
				outcome := "success"
				if closing == Failed || closing == Unknown {
					outcome = "failure"
				}
				m = old(Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: outcome, Ambiguous: closing == Unknown, At: at.Add(7 * time.Second)})
			}
		}
		a = m.Actions[a.ID]
		green = &a
	}
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	if _, present := m.Actions[opening.ID]; (format == ActionFormatVersion || format == 45) && present || m.notificationConditionsEnabled() {
		t.Fatal("fixture did not retain the old opening exclusively in its ledger")
	}
	return s, m, opening, green
}

func TestNotificationProjectionSupportedDeliveryFormats(t *testing.T) {
	for _, format := range []int{DeliveryFormatVersion, LifecycleFormatVersion} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			s, m, opening, closing := archivedNotificationFixtureOn(t, openReplayMemory(t), format, 0, Succeeded)
			if err := s.migrateNotificationConditions(t.Context()); err != nil {
				t.Fatal(err)
			}
			m, _ = s.Get(m.ID)
			v := onlyResolution(t, m)
			if m.LifecycleVersion != LifecycleFormatVersion || m.ActionVersion != ActionFormatVersion || !m.notificationConditionsEnabled() ||
				v.State != Succeeded || v.OpeningActionID != opening.ID || v.ActionID != closing.ID || v.ExecutionDeadline != closing.Deadline || v.ProviderCorrelationVersion != opening.ProviderCorrelationVersion {
				t.Fatal("supported delivery format did not acquire valid markers and its original closure facts")
			}
			for _, a := range []Action{opening, *closing} {
				if got := snapshotAction(t, s.fsm.collections, a.ID); !reflect.DeepEqual(got, a) {
					t.Fatal("format migration rewrote original provider facts")
				}
			}
			data := projectionSnapshot(t, s)
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(data))); err != nil {
				t.Fatal("migrated state cannot restore", err)
			}
		})
	}
}

func onlyResolution(t *testing.T, m Monitor) NotificationResolution {
	t.Helper()
	if m.Notifications == nil || len(m.Notifications.Resolutions) != 1 {
		t.Fatal("expected one frozen resolution condition")
	}
	for _, v := range m.Notifications.Resolutions {
		return v
	}
	panic("unreachable")
}

func TestNotificationProjectionBindsRetainedStartWithoutRewritingAction(t *testing.T) {
	s, m, opening, _ := archivedNotificationFixture(t, "")
	if err := s.migrateNotificationConditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	v := onlyResolution(t, m)
	if v.OpeningActionID != opening.ID || v.Revision != opening.Revision || v.Correlation != opening.NotificationCorrelation ||
		v.MigrationReason != "" || v.ActionID != "" || !v.OpenedAt.Equal(opening.StartedAt) {
		t.Fatal("migration failed to freeze the proven original opening", v)
	}
	if got := snapshotAction(t, s.fsm.collections, opening.ID); !reflect.DeepEqual(got, opening) {
		t.Fatal("migration rewrote an opening identity, deadline or review digest")
	}
	index := s.fsm.image.Index
	if err := s.migrateNotificationConditions(t.Context()); err != nil || s.fsm.image.Index != index {
		t.Fatal("completed migration was repeated", err)
	}
	for _, second := range []int{8, 9} {
		r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", At: policyTime().Add(time.Duration(second) * time.Second)})[0]
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(r.Err)
		}
		m = *r.Monitor
	}
	green := notificationByColor(t, m, "green", Queued)
	if green.Resolution == nil || green.Resolution.ConditionID != v.ID || green.NotificationCorrelation != opening.NotificationCorrelation || green.ExecutionBudget != NotificationTimeout || !green.Deadline.IsZero() {
		t.Fatal("new closure lost its original condition or consumed budget at admission")
	}
	_ = projectionSnapshot(t, s)
}

func TestNotificationProjectionPreservesExistingGreenOutcomesAndDeadlines(t *testing.T) {
	for _, state := range []ActionState{Queued, Started, Succeeded, Failed, Unknown} {
		t.Run(string(state), func(t *testing.T) {
			s, m, opening, closing := archivedNotificationFixture(t, state)
			if err := s.migrateNotificationConditions(t.Context()); err != nil {
				t.Fatal(err)
			}
			m, _ = s.Get(m.ID)
			v := onlyResolution(t, m)
			if v.ActionID != closing.ID || v.DeliveryID != closing.DeliveryID || v.State != state || v.ExecutionDeadline != closing.Deadline || v.RecoveredAt.IsZero() {
				t.Fatal("existing closure gained a new identity/outcome/deadline", v)
			}
			for _, a := range []Action{opening, *closing} {
				if got := snapshotAction(t, s.fsm.collections, a.ID); !reflect.DeepEqual(got, a) {
					t.Fatal("conversion modified retained action bytes")
				}
			}
			if state == Started {
				result := submit(t, s, Command{Kind: "recover", At: policyTime().Add(8 * time.Second)})[0]
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				m, _ = s.Get(m.ID)
				if onlyResolution(t, m).State != Unknown || snapshotAction(t, s.fsm.collections, closing.ID).State != Unknown {
					t.Fatal("interrupted historical closure was not held")
				}
			}
			if state == Queued {
				result := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: closing.ID,
					NoticeRevision: ActionNoticeRevision(*closing), At: closing.Deadline.Add(time.Second)})[0]
				if result.Err != nil || result.Allowed {
					t.Fatal("expired historical closure received a grant", result.Err)
				}
				m, _ = s.Get(m.ID)
				if v := onlyResolution(t, m); v.State != Cancelled || v.ExecutionDeadline != closing.Deadline {
					t.Fatal("expiry failed to update the bound condition without renewing its budget")
				}
			}
			for _, second := range []int{80, 81} {
				r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1,
					Outcome: "success", At: policyTime().Add(time.Duration(second) * time.Second)})[0]
				if r.Err != nil || r.Monitor == nil {
					t.Fatal(r.Err)
				}
				m = *r.Monitor
			}
			all, _ := monitorWithActionHistory(t, s, m.ID)
			if len(all.Actions) != 2 || queuedColorCount(m, "green") != 0 {
				t.Fatal("historical closure was duplicated after migration")
			}
			_ = projectionSnapshot(t, s)
		})
	}
}

func TestNotificationProjectionMapsSafeRetryToOriginalFixedBudget(t *testing.T) {
	s, m, _, original := archivedNotificationFixture(t, Queued)
	if err := s.migrateNotificationConditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	at := policyTime().Add(8 * time.Second)
	for _, command := range []Command{
		{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: original.ID, NoticeRevision: ActionNoticeRevision(*original), At: at},
		{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: original.ID, Outcome: "failure", Retryable: true, At: at.Add(time.Second)},
	} {
		r := submit(t, s, command)[0]
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(command.Kind, r.Err)
		}
		m = *r.Monitor
	}
	retry := notificationByColor(t, m, "green", Queued)
	v := onlyResolution(t, m)
	if retry.Attempt != 2 || retry.DeliveryID != original.DeliveryID || retry.Deadline != original.Deadline || retry.Resolution != nil ||
		v.ActionID != retry.ID || v.State != Queued || v.ExecutionDeadline != original.Deadline {
		t.Fatal("safe retry lost original binding or acquired a new budget")
	}
	for _, command := range []Command{
		{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, NoticeRevision: ActionNoticeRevision(retry), At: retry.NotBefore},
		{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, Outcome: "success", At: retry.NotBefore.Add(time.Second)},
	} {
		r := submit(t, s, command)[0]
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(command.Kind, r.Err)
		}
		m = *r.Monitor
	}
	if v := onlyResolution(t, m); v.State != Succeeded || v.ActionID != retry.ID || v.ExecutionDeadline != original.Deadline {
		t.Fatal("completion did not update the original logical closure")
	}
}

func TestNotificationProjectionDoesNotBindExposureAcrossRevisionChange(t *testing.T) {
	s, m, opening, _ := archivedNotificationFixture(t, "")
	input := testMonitor()
	input.Revision, input.Policy = "changed-target", lifecyclePolicy()
	r := applyCatalogFormat(t, s, ActionFormatVersion, Command{Kind: "configure", MonitorID: m.ID, Revision: input.Revision, Config: &input, At: policyTime().Add(4 * time.Second)})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if err := s.migrateNotificationConditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	v := onlyResolution(t, m)
	if v.OpeningActionID != "" || v.MigrationReason != "migration_start_unverified" {
		t.Fatal("an exposure flag rebound an old start to replacement configuration")
	}
	if got := snapshotAction(t, s.fsm.collections, opening.ID); !reflect.DeepEqual(got, opening) {
		t.Fatal("unproven binding altered original evidence")
	}
	for _, second := range []int{8, 9} {
		r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", At: policyTime().Add(time.Duration(second) * time.Second)})[0]
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(r.Err)
		}
		m = *r.Monitor
	}
	if queuedColorCount(m, "green") != 0 {
		t.Fatal("unproven migration invoked a replacement destination")
	}
}

func TestNotificationProjectionRequiresExactFrozenOwner(t *testing.T) {
	_, m, opening, _ := archivedNotificationFixture(t, "")
	for _, field := range []string{"revision", "file-incarnation", "catalog-uid", "endpoint", "correlation"} {
		t.Run(field, func(t *testing.T) {
			a := opening.Clone()
			switch field {
			case "revision":
				a.Revision = "different-revision"
			case "file-incarnation":
				a.FileIncarnation = "different-incarnation"
			case "catalog-uid":
				a.CatalogUID = "different-owner"
			case "endpoint":
				a.EndpointID = "different-endpoint"
			case "correlation":
				a.NotificationCorrelation = "different-correlation"
			}
			p := &notificationProjectionMigration{At: policyTime()}
			p.include(m, a)
			if p.Opening != nil || len(p.Closures) != 0 {
				t.Fatal("migration reassigned retained evidence across its original binding")
			}
		})
	}
}

func TestNotificationProjectionResumesBoundedSnapshotAndRejectsOldEnvelope(t *testing.T) {
	s, m, opening, _ := archivedNotificationFixture(t, "")
	s.fsm.mu.Lock()
	for batch := range 3 {
		rows := make([]actionLedgerRow, 0, 64)
		for index := range 64 {
			a := opening.Clone()
			a.ID = fmt.Sprintf("history-%03d", batch*64+index)
			a.DeliveryID = a.ID
			a.NotificationCorrelation, a.NotificationEpisode, a.IncidentID = "older-correlation", "older-episode", "older-incident"
			rows = append(rows, actionLedgerRow{MonitorID: m.ID, Action: a})
		}
		position := actionLedgerPosition{Index: s.fsm.image.Index + 1}
		sequence, err := s.fsm.collections.CommitActions(position, rows)
		if err != nil {
			s.fsm.mu.Unlock()
			t.Fatal(err)
		}
		s.fsm.image.ActionStorage = &actionStorageState{Sequence: sequence, Position: position}
		s.fsm.image.Index = position.Index
	}
	// Rebuild only the fixture's aggregate count to match its added history.
	current := s.fsm.image.Monitors[m.ID]
	current.ActionProjection.Notifications.Accepted += 192
	s.fsm.image.Monitors[m.ID] = current
	s.fsm.mu.Unlock()
	command := Command{Kind: "action_project", Revision: "notifications", MonitorID: m.ID, Generation: s.fsm.image.ActionProjectionState.Revision, At: policyTime().Add(5 * time.Second)}
	encoded, err := json.Marshal(envelope{Version: ActionFormatVersion, Commands: []Command{command}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEnvelope(encoded); err == nil {
		t.Fatal("format44 accepted a new notification migration phase")
	}
	result := submit(t, s, command)[0]
	if result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	state := s.fsm.image.ActionProjectionState.clone()
	if state.Notifications == nil || state.Notifications.After == "" || !s.fsm.controllerHealthState().actionMigration {
		t.Fatal("migration did not retain a bounded pending cursor")
	}
	if r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Outcome: "success", At: command.At})[0]; r.Err != errActionProjectionPending {
		t.Fatal("partial migration admitted ordinary observations", r.Err)
	}
	if state.Notifications.Opening != nil {
		copy := state.clone()
		copy.Notifications.Opening.ActionID = "changed"
		if state.Notifications.Opening.ActionID == "changed" {
			t.Fatal("snapshot migration state shares mutable opening data")
		}
	}
	snapshot := projectionSnapshot(t, s)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, s.fsm.image.ActionProjectionState) {
		t.Fatal("snapshot lost the original migration cursor and action fence")
	}
	if err := s.migrateNotificationConditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	if !m.notificationConditionsEnabled() || s.fsm.image.ActionProjectionState.Notifications != nil || onlyResolution(t, m).OpeningActionID != opening.ID {
		t.Fatal("resumed migration failed to publish the original opening")
	}
	_ = projectionSnapshot(t, s)
}

func TestNotificationProjectionEmptyStoreDoesNotCreateMigration(t *testing.T) {
	s := openCatalogMemory(t)
	index := s.fsm.image.Index
	if err := s.migrateNotificationConditions(t.Context()); err != nil || s.fsm.image.Index != index || s.fsm.image.ActionProjectionState.Notifications != nil {
		t.Fatal("empty store created unnecessary committed migration work", err)
	}
	if p := (*notificationProjectionMigration)(nil).clone(); p != nil {
		t.Fatal("nil migration acquired state")
	}
}
