package persistence

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestActionRetirementAgeAndProtectedEvidence(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			ledger := newLedgerTest(t, disk, 1<<20)
			m := testMonitor()
			m.ID = "monitor"
			m.Actions = nil
			m.ActionProjection = &ActionProjection{Version: actionProjectionVersion}
			rows := []actionLedgerRow{}
			for _, age := range []int{89, 90, 91} {
				a := testActionRow(fmt.Sprintf("day-%d", age))
				a.Action.State = Succeeded
				a.Action.FinishedAt = now.Add(-time.Duration(age) * 24 * time.Hour)
				rows = append(rows, a)
			}
			for _, name := range []string{"unknown", "reviewed", "late", "missing-time"} {
				row := testActionRow(name)
				row.Action.FinishedAt = now.Add(-100 * 24 * time.Hour)
				switch name {
				case "reviewed":
					row.Action.Review = &ActionReview{Resolution: "accepted", Revision: "review"}
				case "late":
					row.Action.State = Succeeded
					row.Action.LateEvidence = &LateActionEvidence{}
				case "missing-time":
					row.Action.State = Succeeded
					row.Action.FinishedAt = time.Time{}
				}
				rows = append(rows, row)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, rows); err != nil {
				t.Fatal(err)
			}
			f := machine{collections: ledger, image: image{Version: NotificationConditionFormatVersion, Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{Sequence: 1, Position: actionLedgerPosition{Index: 1}}, ActionProjectionState: &actionProjectionState{Version: 1, Ready: true}}, actionPosition: actionLedgerPosition{Index: 2}}
			old, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			items := []ActionRetirementItem{}
			for _, row := range rows {
				items = append(items, ActionRetirementItem{ID: row.Action.ID, Sequence: 1})
			}
			// The command's identity ordering is deterministic, independent of selection.
			slices.SortFunc(items, func(a, b ActionRetirementItem) int { return strings.Compare(a.ID, b.ID) })
			result := f.applyActionRetirement(&ActionRetirementCommand{Cutoff: now.Add(-actionRetention), Items: items}, now)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if err := f.flushActionWrites(); err != nil {
				t.Fatal(err)
			}
			if f.image.ActionStorage.Retired != 2 {
				t.Fatalf("retired=%d", f.image.ActionStorage.Retired)
			}
			current, err := ledger.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			for _, id := range []string{"day-90", "day-91"} {
				if _, ok, err := current.Get(t.Context(), id); err != nil || ok {
					t.Fatal("retired action visible", id, err)
				}
				if _, ok, err := old.Get(t.Context(), id); err != nil || !ok {
					t.Fatal("older cursor lost committed row", id, err)
				}
			}
			for _, id := range []string{"day-89", "unknown", "reviewed", "late", "missing-time"} {
				if _, ok, err := current.Get(t.Context(), id); err != nil || !ok {
					t.Fatal("protected action lost", id, err)
				}
			}
		})
	}
}

func TestActionRetirementSnapshotCompactsTombstones(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 1<<20)
			row := testActionRow("retired-identity")
			row.Action.State = Succeeded
			row.Action.FinishedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{{MonitorID: row.MonitorID, Action: Action{ID: row.Action.ID}, Deleted: true}}); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.CommitActions(actionLedgerPosition{Index: 3}, []actionLedgerRow{testActionRow("retained")}); err != nil {
				t.Fatal(err)
			}
			frozen, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			var buffer bytes.Buffer
			if err := writeActionLedger(&buffer, frozen); err != nil {
				t.Fatal(err)
			}
			frozen.Close()
			if bytes.Contains(buffer.Bytes(), []byte(row.Action.ID)) {
				t.Fatal("compacted snapshot retained unreferenced tombstone")
			}
			restored := newLedgerTest(t, disk, 1<<20)
			if _, err := importActionLedger(&buffer, restored); err != nil {
				t.Fatal(err)
			}
			pin, err := restored.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			if _, ok, err := pin.Get(t.Context(), row.Action.ID); err != nil || ok {
				t.Fatal("retired row returned after snapshot", err)
			}
			if _, ok, err := pin.Get(t.Context(), "retained"); err != nil || !ok {
				t.Fatal("retained row lost", err)
			}
		})
	}
}

func TestActionRetirementProtectsLiveDependencies(t *testing.T) {
	setups := map[string]func(*machine, *Monitor, Action){
		"execution reference": func(_ *machine, m *Monitor, a Action) {
			m.Actions = map[string]Action{a.ID: a}
		},
		"later attempt": func(_ *machine, m *Monitor, a Action) {
			m.Actions = map[string]Action{"later-attempt": {ID: "later-attempt", Kind: "code", State: Queued, DeliveryID: a.DeliveryID}}
		},
		"pending recovery-failure notification": func(_ *machine, m *Monitor, a Action) {
			m.Actions = map[string]Action{"notification": {ID: "notification", Kind: "code", State: Queued, RecoveryFailure: &RecoveryFailure{ActionID: a.ID}}}
		},
		"pending resolution": func(_ *machine, m *Monitor, a Action) {
			m.Notifications = &NotificationState{Version: NotificationConditionFormatVersion,
				Resolutions: map[string]NotificationResolution{"closure": {OpeningActionID: a.ID}}}
		},
		"uncertain migration proof": func(_ *machine, m *Monitor, a Action) {
			m.Notifications = &NotificationState{Version: NotificationConditionFormatVersion,
				Resolutions: map[string]NotificationResolution{"closure": {OpeningActionID: a.ID, State: Unknown, MigrationReason: "migration_delivery_evidence_conflict"}}}
		},
		"suppressed worker resolution": func(_ *machine, m *Monitor, a Action) {
			m.Notifications = &NotificationState{Version: NotificationConditionFormatVersion,
				Resolutions: map[string]NotificationResolution{"closure": {OpeningActionID: a.ID, State: Cancelled, Reason: "worker_not_started"}}}
		},
		"unbound migration": func(_ *machine, m *Monitor, a Action) {
			m.Notifications = &NotificationState{Version: NotificationConditionFormatVersion,
				Migrations: map[string]DeliveryMigration{a.ID: {ActionID: a.ID, Reason: "migration_binding_unverified"}}}
		},
		"pending receipt": func(f *machine, _ *Monitor, a Action) {
			f.image.Operations = map[string]OperationReceipt{"operation": {ActionID: a.ID}}
		},
		"staged review assessment": func(f *machine, m *Monitor, _ Action) {
			f.reviewWrites = map[string]reviewRecord{"fact": {MonitorID: m.ID, Fact: &reviewFact{}}}
		},
	}
	for _, disk := range []bool{false, true} {
		for name, setup := range setups {
			t.Run(fmt.Sprintf("disk=%v/%s", disk, name), func(t *testing.T) {
				ledger := newLedgerTest(t, disk, 1<<20)
				now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
				row := testActionRow("dependency")
				row.Action.State, row.Action.DeliveryID = Succeeded, "logical-delivery"
				row.Action.FinishedAt = now.Add(-100 * 24 * time.Hour)
				if _, err := ledger.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{row}); err != nil {
					t.Fatal(err)
				}
				m := testMonitor()
				m.ID, m.Actions = row.MonitorID, nil
				m.ActionProjection = &ActionProjection{Version: actionProjectionVersion}
				f := machine{collections: ledger, image: image{Version: NotificationConditionFormatVersion,
					Monitors: map[string]Monitor{m.ID: m}, ActionStorage: &actionStorageState{Sequence: 1, Position: actionLedgerPosition{Index: 1}},
					ActionProjectionState: &actionProjectionState{Version: 1, Ready: true}}, actionPosition: actionLedgerPosition{Index: 2}}
				setup(&f, &m, row.Action)
				f.image.Monitors[m.ID] = m
				command := &ActionRetirementCommand{Cutoff: now.Add(-actionRetention), Items: []ActionRetirementItem{{ID: row.Action.ID, Sequence: 1}}}
				result := f.applyActionRetirement(command, now)
				if result.Err != nil || f.image.ActionStorage.Retired != 0 || len(f.actionWrites) != 0 {
					t.Fatal("retirement ignored a live dependency", result.Err)
				}
				// Removing the dependency is insufficient if the selected head changed.
				m.Actions, m.Notifications, f.image.Operations, f.reviewWrites = nil, nil, nil, nil
				f.image.Monitors[m.ID] = m
				row.Action.Outcome = "accepted"
				if _, err := ledger.CommitActions(actionLedgerPosition{Index: 2}, []actionLedgerRow{row}); err != nil {
					t.Fatal(err)
				}
				result = f.applyActionRetirement(command, now)
				if result.Err != nil || f.image.ActionStorage.Retired != 0 || len(f.actionWrites) != 0 {
					t.Fatal("stale retirement selection removed a changed head", result.Err)
				}
				command.Items[0].Sequence = 2
				result = f.applyActionRetirement(command, now)
				if result.Err != nil || f.image.ActionStorage.Retired != 1 || len(f.actionWrites) != 1 {
					t.Fatal("fresh dependency-free selection did not retire", result.Err)
				}
			})
		}
	}
}

func TestActionRetirementProtectsEveryConflictingMigrationDelivery(t *testing.T) {
	ledger := newLedgerTest(t, true, 1<<20)
	a := testActionRow("nonselected-proof").Action
	a.Kind, a.Color, a.State = "code", "green", Succeeded
	a.Revision, a.CatalogUID, a.FileIncarnation = "original-revision", "original-owner", "original-incarnation"
	a.NotificationCorrelation, a.EndpointID, a.DeliveryID = "original-correlation", "original-endpoint", "nonselected-delivery"
	a.FinishedAt = policyTime()
	m := testMonitor()
	m.Actions = nil
	m.Revision, m.CatalogUID, m.FileIncarnation = "replacement-revision", "replacement-owner", "replacement-incarnation"
	m.ActionProjection = &ActionProjection{Version: actionProjectionVersion}
	m.Notifications = &NotificationState{Version: NotificationConditionFormatVersion, Resolutions: map[string]NotificationResolution{
		"condition": {ID: "condition", OpeningActionID: "opening", ActionID: "selected-proof", DeliveryID: "selected-delivery",
			State: Unknown, MigrationReason: "migration_delivery_evidence_conflict", Revision: a.Revision, CatalogUID: a.CatalogUID,
			FileIncarnation: a.FileIncarnation, Correlation: a.NotificationCorrelation, EndpointID: a.EndpointID},
	}}
	f := machine{collections: ledger, image: image{ActionProjectionState: &actionProjectionState{Version: 1, Ready: true}}}
	for _, field := range []string{"matching", "revision", "catalog-owner", "file-owner", "correlation", "endpoint", "color", "kind"} {
		t.Run(field, func(t *testing.T) {
			row := a.Clone()
			switch field {
			case "revision":
				row.Revision = "another-revision"
			case "catalog-owner":
				row.CatalogUID = "another-owner"
			case "file-owner":
				row.FileIncarnation = "another-incarnation"
			case "correlation":
				row.NotificationCorrelation = "another-correlation"
			case "endpoint":
				row.EndpointID = "another-endpoint"
			case "color":
				row.Color = "red"
			case "kind":
				row.Kind = "intervention"
			}
			blocked, err := f.actionRetirementBlocked(m, row)
			if err != nil || blocked != (field == "matching") {
				t.Fatal("conflict retention did not match only the original frozen closure evidence", blocked, err)
			}
		})
	}
}

func TestActionRetirementMetadataValidation(t *testing.T) {
	for _, progress := range []actionStorageState{
		{Retired: 1},
		{RetiredBytes: 100},
		{Retired: 1, RetiredBytes: 100},
		{Sequence: 1, Position: actionLedgerPosition{Index: 1}, Retired: 1},
	} {
		if err := validateActionStorageHeader(image{Version: NotificationConditionFormatVersion, Index: 1, ActionStorage: &progress}); err == nil {
			t.Fatalf("accepted inconsistent retirement metadata: %+v", progress)
		}
	}
	progress := actionStorageState{Sequence: 1, Position: actionLedgerPosition{Index: 1}, Retired: 1, RetiredBytes: 100}
	if err := validateActionStorageHeader(image{Version: NotificationConditionFormatVersion, Index: 1, ActionStorage: &progress}); err != nil {
		t.Fatal(err)
	}
}
