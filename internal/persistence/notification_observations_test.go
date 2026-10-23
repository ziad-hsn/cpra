package persistence

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestNotificationConditionObservationsBoundedWithoutActionLookup(t *testing.T) {
	m := Monitor{Notifications: &NotificationState{Resolutions: make(map[string]NotificationResolution)}}
	at := policyTime().In(time.FixedZone("offset", 3*3600))
	for i := range 150 {
		id := fmt.Sprintf("condition-%03d", i)
		m.Notifications.Resolutions[id] = NotificationResolution{ID: id, OpeningActionID: "opening", RecoveredAt: at, MigrationReason: "migration_binding_unverified", ProviderKey: "private-key"}
	}
	first, truncated := m.NotificationConditions()
	if !truncated || len(first) != 100 || first[0].ConditionID != "condition-000" || first[99].ConditionID != "condition-099" {
		t.Fatalf("unbounded or incorrectly ordered observations: %d %v", len(first), truncated)
	}
	if first[0].PendingAt.Location() != time.UTC || first[0].State != "held" || first[0].OriginalActionID != "opening" {
		t.Fatal("incomplete pending resolution", first[0])
	}
	for range 10 {
		again, _ := m.NotificationConditions()
		if !reflect.DeepEqual(first, again) {
			t.Fatal("map iteration changed the retained page")
		}
	}
}
