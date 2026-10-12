package persistence

import (
	"errors"
	"fmt"
	"time"
)

// DeliveryFormatVersion adds episode-bound delivery identities and one overall
// action deadline. Formats 1–39 retain their original transition semantics.
const DeliveryFormatVersion = 40
const deliverySnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-40\n"
const NotificationTimeout = 45 * time.Second

var ErrNotificationIdentity = errors.New("invalid notification identity or deadline")

func deliveryStorageFormat(version int) bool {
	return version == DeliveryFormatVersion || version > DeliveryFormatVersion && externalStorageFormat(version)
}

func (m *Monitor) upgradeNotifications(at time.Time, emit func(string, *Action)) {
	if m.NotificationVersion == DeliveryFormatVersion {
		return
	}
	// Old queued deliveries have no episode or immutable logical identity.
	// Cancel only unsent work through this committed transition. Never replay or
	// erase a started/completed/unknown action while upgrading its monitor.
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.Kind == "code" && a.State == Queued && a.CatalogUID == m.CatalogUID {
			m.cancelNotification(a, "notification_format_upgraded", at, emit)
		}
	}
	m.NotificationVersion = DeliveryFormatVersion
	m.NotificationEpisode = identity("notification/" + m.ID + "/" + m.CatalogUID + "/" + m.IncidentID)
	m.Cooldowns = nil
}

func (m *Monitor) changeNotificationEpisode(episode string, at time.Time, emit func(string, *Action)) {
	if m.NotificationVersion != DeliveryFormatVersion || m.NotificationEpisode == episode {
		return
	}
	m.NotificationEpisode = episode
	m.Cooldowns = nil
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.CatalogUID == m.CatalogUID && a.Kind == "code" && a.State == Queued && a.NotificationEpisode != episode {
			m.cancelNotification(a, "notification_superseded", at, emit)
		}
	}
}

func (m *Monitor) cancelNotification(a Action, cause string, at time.Time, emit func(string, *Action)) {
	a.State, a.Outcome, a.FinishedAt = Cancelled, cause, at
	m.Actions[a.ID] = a
	emit("action_cancelled", &a)
}

func (m *Monitor) suppressMaintenanceNotifications(at time.Time, emit func(string, *Action)) {
	if m.NotificationVersion != DeliveryFormatVersion {
		return
	}
	m.NotificationMaintenance = true
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.CatalogUID == m.CatalogUID && a.Kind == "code" && a.State == Queued {
			m.cancelNotification(a, "maintenance", at, emit)
		}
	}
}

func (m Monitor) notificationEligible(a Action) bool {
	if m.NotificationVersion != DeliveryFormatVersion || a.Kind != "code" {
		return true
	}
	if a.NotificationEpisode != m.NotificationEpisode || a.IncidentID != "" && a.IncidentID != m.IncidentID {
		return false
	}
	switch a.Color {
	case "red":
		return m.Incident
	case "yellow":
		return m.Warning || m.Recovering
	case "cyan":
		return m.VerifyRemaining > 0
	case "green":
		return !m.Warning && !m.Recovering && !m.Incident && m.LastOutcome == "success"
	default:
		return true
	}
}

func validateNotificationIdentity(m Monitor, version int) error {
	if m.NotificationVersion != 0 {
		if !deliveryStorageFormat(version) || m.NotificationVersion != DeliveryFormatVersion || !catalogIdentifier(m.NotificationEpisode, 256) {
			return ErrNotificationIdentity
		}
	} else if m.NotificationEpisode != "" || m.NotificationMaintenance {
		return ErrNotificationIdentity
	}
	for _, a := range m.Actions {
		hasIdentity := a.DeliveryID != "" || a.NotificationEpisode != "" || a.EndpointID != "" || !a.Deadline.IsZero()
		if !hasIdentity {
			continue // Retained pre-migration outcomes keep their original evidence.
		}
		if !deliveryStorageFormat(version) || m.NotificationVersion != DeliveryFormatVersion || a.Kind != "code" ||
			!catalogIdentifier(a.DeliveryID, 256) || !catalogIdentifier(a.NotificationEpisode, 256) || !catalogIdentifier(a.EndpointID, 256) ||
			!a.Deadline.IsZero() && (a.Deadline.Year() < 1 || a.Deadline.Year() > 9999 || !a.Deadline.After(a.NotBefore) && a.State == Started) {
			return ErrNotificationIdentity
		}
	}
	return nil
}

func notificationEndpointIdentity(m Monitor, color string, endpoint int) string {
	// Revision pins the endpoint configuration; the ordinal belongs to its
	// immutable color-specific destination list. No target or credential enters
	// this identity or the Raft record.
	return identity(fmt.Sprintf("endpoint/%s/%s/%s/%s/%d", m.ID, m.CatalogUID, m.Revision, color, endpoint))
}
