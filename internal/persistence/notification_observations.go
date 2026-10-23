package persistence

import (
	"slices"
	"strings"
	"time"
)

// NotificationConditionObservation contains only public condition metadata. A
// condition can exist without an action, so action pagination cannot replace it.
type NotificationConditionObservation struct {
	ConditionID      string
	Kind             string
	Color            string
	State            string
	Reason           string
	OriginalActionID string
	PendingAt        time.Time
	RecoveredAt      time.Time
	MigrationReason  string
}

// NotificationConditions returns a deterministic, bounded projection. It never
// reads the action ledger or exposes destination configuration/provider keys.
func (m Monitor) NotificationConditions() (items []NotificationConditionObservation, truncated bool) {
	items, truncated = m.notificationConditions()
	// Detached review conditions require a bounded ledger read on the detail
	// path. Fleet projection must not imply this in-memory list is complete.
	return items, truncated || m.ActionProjection != nil && m.ActionProjection.ReviewConditions > 0 || m.Notifications != nil && len(m.Notifications.ReviewAlerts) > 0
}

func (m Monitor) notificationConditions() (items []NotificationConditionObservation, truncated bool) {
	if m.Notifications == nil {
		return nil, false
	}
	const limit = 100
	add := func(v NotificationConditionObservation) {
		if v.State == "" {
			v.State = "pending"
		}
		v.PendingAt, v.RecoveredAt = v.PendingAt.UTC(), v.RecoveredAt.UTC()
		index, _ := slices.BinarySearchFunc(items, v.ConditionID, func(a NotificationConditionObservation, id string) int {
			return strings.Compare(a.ConditionID, id)
		})
		if len(items) == limit {
			truncated = true
			if index == limit {
				return
			}
			items = items[:limit-1]
		}
		items = slices.Insert(items, index, v)
	}
	for slot, v := range m.Notifications.Deliveries {
		color, _, _ := strings.Cut(slot, "/")
		add(NotificationConditionObservation{ConditionID: m.namespacedIdentity(identity("notification-condition/" + v.Episode + "/" + slot + "/" + v.Condition)),
			Kind: "opening", Color: color, State: string(v.State), Reason: v.Reason, OriginalActionID: v.ActionID, PendingAt: v.PendingAt})
	}
	for _, v := range m.Notifications.Resolutions {
		pending := v.RecoveredAt
		if pending.IsZero() {
			pending = v.OpenedAt
		}
		state := string(v.State)
		if v.MigrationReason != "" {
			state = "held"
		}
		add(NotificationConditionObservation{ConditionID: v.ID, Kind: "resolution", Color: "green", State: state, Reason: v.Reason,
			OriginalActionID: v.OpeningActionID, PendingAt: pending, RecoveredAt: v.RecoveredAt, MigrationReason: v.MigrationReason})
	}
	for _, v := range m.Notifications.Migrations {
		if v.Reason == "" {
			continue // Bound migration evidence is represented by its condition/action.
		}
		add(NotificationConditionObservation{ConditionID: "migration/" + v.ActionID, Kind: "migration", Color: v.Color, State: "held",
			Reason: v.Reason, OriginalActionID: v.ActionID, PendingAt: v.StartedAt, MigrationReason: v.Reason})
	}
	return items, truncated
}
