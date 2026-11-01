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
	return version == DeliveryFormatVersion || lifecycleStorageFormat(version) || version > DeliveryFormatVersion && externalStorageFormat(version)
}

func requiresDeliveryFormat(m Monitor) bool {
	if m.LastFailure != (FailureDiagnostic{}) {
		return true
	}
	if m.NotificationVersion != 0 || m.NotificationEpisode != "" || m.NotificationCorrelation != "" || m.NotificationMaintenance || len(m.Policy.NotificationEndpointIDs) != 0 {
		return true
	}
	for _, a := range m.Actions {
		if a.Failure != (FailureDiagnostic{}) || a.Cause != (FailureDiagnostic{}) {
			return true
		}
		if a.DeliveryID != "" || a.NotificationEpisode != "" || a.NotificationCorrelation != "" || a.EndpointID != "" || !a.QueuedAt.IsZero() || !a.Deadline.IsZero() {
			return true
		}
	}
	return false
}

func (m *Monitor) upgradeNotifications(at time.Time, emit func(string, *Action)) {
	if m.NotificationVersion == DeliveryFormatVersion {
		if m.NotificationCorrelation == "" {
			m.NotificationCorrelation = m.NotificationEpisode
		}
		for _, id := range sortedActions(m.Actions) {
			a := m.Actions[id]
			if a.Kind == "code" && a.State == Queued && m.OwnsAction(a) && a.QueuedAt.IsZero() {
				m.cancelNotification(a, "notification_identity_incomplete", at, emit)
			}
		}
		return
	}
	// Old queued deliveries have no episode or immutable logical identity.
	// Cancel only unsent work through this committed transition. Never replay or
	// erase a started/completed/unknown action while upgrading its monitor.
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.Kind == "code" && a.State == Queued && m.OwnsAction(a) {
			m.cancelNotification(a, "notification_format_upgraded", at, emit)
		}
	}
	m.NotificationVersion = DeliveryFormatVersion
	m.NotificationEpisode = identity("notification/" + m.ID + "/" + m.CatalogUID + "/" + m.IncidentID)
	m.NotificationCorrelation = m.NotificationEpisode
	if m.IncidentID != "" && m.IncidentClosedAt.IsZero() {
		m.NotificationCorrelation = m.IncidentID
	}
	m.Cooldowns = nil
}

func (m *Monitor) changeNotificationEpisode(episode string, at time.Time, emit func(string, *Action)) {
	if m.NotificationVersion != DeliveryFormatVersion || m.NotificationEpisode == episode {
		return
	}
	m.NotificationEpisode = episode
	m.Cooldowns = nil
	if m.Notifications != nil {
		m.Notifications.Deliveries = nil
		for id, binding := range m.Notifications.Migrations {
			if binding.IncidentID != m.IncidentID {
				delete(m.Notifications.Migrations, id)
			}
		}
	}
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.Kind == "code" && a.State == Queued && !m.ControlNotification(a) && a.ReviewNotice == nil && a.Resolution == nil && a.NotificationEpisode != episode {
			m.cancelNotification(a, "notification_superseded", at, emit)
		}
	}
}

func (m *Monitor) cancelNotification(a Action, cause string, at time.Time, emit func(string, *Action)) {
	a.State, a.Outcome, a.FinishedAt = Cancelled, cause, at
	m.recordNotificationCancellation(a)
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
		if m.OwnsAction(a) && a.Kind == "code" && a.State == Queued && !m.ControlNotification(a) {
			m.cancelNotification(a, "maintenance", at, emit)
		}
	}
}

func (m Monitor) notificationEligible(a Action) bool {
	if a.Resolution != nil {
		return m.resolutionEligible(a)
	}
	if a.ReviewNotice != nil {
		return m.reviewNoticeEligible(a)
	}
	if m.ControlNotification(a) {
		return true
	}
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
	if len(m.Policy.NotificationDrivers) != 0 && !notificationConditionStorageFormat(version) {
		return ErrNotificationIdentity
	}
	if err := validateLifecycle(m, version); err != nil {
		return err
	}
	if err := m.LastFailure.validate(version); err != nil {
		return err
	}
	if m.NotificationCorrelation != "" && (!deliveryStorageFormat(version) || !catalogIdentifier(m.NotificationCorrelation, 256)) {
		return ErrNotificationIdentity
	}
	if len(m.Policy.NotificationEndpointIDs) != 0 && !deliveryStorageFormat(version) {
		return ErrNotificationIdentity
	}
	if m.NotificationVersion != 0 {
		if !deliveryStorageFormat(version) || m.NotificationVersion != DeliveryFormatVersion || !catalogIdentifier(m.NotificationEpisode, 256) {
			return ErrNotificationIdentity
		}
	} else if m.NotificationEpisode != "" || m.NotificationMaintenance {
		return ErrNotificationIdentity
	}
	for _, a := range m.Actions {
		if err := validateNotificationAction(a, version, m.NotificationVersion); err != nil {
			return err
		}
	}
	return nil
}

// MaintenanceBetween reports whether a queued delivery was covered by an
// absolute window, including one missed while the controller was stopped.
func (p Policy) MaintenanceBetween(from, through time.Time) bool {
	if from.IsZero() || through.Before(from) {
		return false
	}
	for _, window := range p.Maintenance {
		if !window.Start.After(through) && window.End.After(from) {
			return true
		}
	}
	return false
}

func notificationEndpointIdentity(m Monitor, color string, endpoint int) string {
	if value, ok := m.reviewEndpointIDs[endpoint]; color == "blue" && ok {
		return value
	}
	if ids := m.Policy.NotificationEndpointIDs[color]; endpoint >= 0 && endpoint < len(ids) {
		return identity(fmt.Sprintf("endpoint/%s/%s/%s", m.ID, m.incarnationIdentity(), ids[endpoint]))
	}
	// Revision pins the endpoint configuration; the ordinal belongs to its
	// immutable color-specific destination list. No target or credential enters
	// this identity or the Raft record.
	return identity(fmt.Sprintf("endpoint/%s/%s/%s/%s/%d", m.ID, m.incarnationIdentity(), m.Revision, color, endpoint))
}

func validateNotificationAction(a Action, version, notificationVersion int) error {
	if err := validateActionRule(a, version); err != nil {
		return err
	}
	if err := validateNotificationBudget(a, version); err != nil {
		return err
	}
	if err := a.Cause.validate(version); err != nil {
		return err
	}
	if err := a.Failure.validate(version); err != nil {
		return err
	}
	if a.NotificationCorrelation != "" && (!deliveryStorageFormat(version) || !catalogIdentifier(a.NotificationCorrelation, 256)) {
		return ErrNotificationIdentity
	}
	hasIdentity := a.DeliveryID != "" || a.NotificationEpisode != "" || a.EndpointID != "" || !a.QueuedAt.IsZero() || !a.Deadline.IsZero()
	if !hasIdentity {
		return nil // Retained pre-migration outcomes keep their original evidence.
	}
	if a.Kind == "intervention" && lifecycleStorageFormat(version) {
		if a.DeliveryID != "" || a.NotificationEpisode != "" || a.EndpointID != "" || a.NotificationCorrelation != "" ||
			a.QueuedAt.IsZero() || a.QueuedAt.After(a.NotBefore) || a.QueuedAt.Year() < 1 || a.QueuedAt.Year() > 9999 ||
			!validInterventionTiming(a, version) {
			return ErrNotificationIdentity
		}
		return nil
	}
	if !deliveryStorageFormat(version) || notificationVersion != DeliveryFormatVersion || a.Kind != "code" ||
		!catalogIdentifier(a.DeliveryID, 256) || !catalogIdentifier(a.NotificationEpisode, 256) || !catalogIdentifier(a.EndpointID, 256) ||
		!a.QueuedAt.IsZero() && (a.QueuedAt.Year() < 1 || a.QueuedAt.Year() > 9999 || a.QueuedAt.After(a.NotBefore)) ||
		!a.Deadline.IsZero() && (a.Deadline.Year() < 1 || a.Deadline.Year() > 9999 || !a.Deadline.After(a.NotBefore) && a.State == Started) {
		return ErrNotificationIdentity
	}
	return nil
}

// Format50 grants a captured budget at first start. Older absolute deadlines
// retain their original queue-time validation. A retry can only shorten the
// remaining budget, never renew it from its later StartedAt.
func validInterventionTiming(a Action, version int) bool {
	if dispatchStorageFormat(version) && a.ExecutionBudget > 0 {
		if a.Deadline.IsZero() {
			return a.StartedAt.IsZero() && (a.State == Queued || a.State == Cancelled)
		}
		return a.Deadline.Year() <= 9999 && a.Deadline.Year() >= 1 && (a.StartedAt.IsZero() || a.Deadline.After(a.StartedAt) && !a.Deadline.After(a.StartedAt.Add(a.ExecutionBudget)))
	}
	return a.Deadline.After(a.NotBefore) && a.Deadline.Sub(a.NotBefore) <= 24*time.Hour && a.Deadline.Year() <= 9999
}
