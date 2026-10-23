package persistence

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// NotificationConditionFormatVersion separates a pending notification from its
// execution budget. Historical absolute deadlines retain their original meaning.
const NotificationConditionFormatVersion = 46
const notificationConditionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-46\n"

func notificationConditionStorageFormat(version int) bool {
	return version == NotificationConditionFormatVersion || version == 47 && externalStorageFormat(version)
}

func (m Monitor) notificationConditionsEnabled() bool {
	return m.Notifications != nil && m.Notifications.Version == NotificationConditionFormatVersion
}

func (m *Monitor) upgradeNotificationConditions() {
	m.upgradeNotificationState()
	m.Notifications.Version = NotificationConditionFormatVersion
}

func validateNotificationBudget(a Action, version int) error {
	if a.ProviderKey != "" && (!notificationConditionStorageFormat(version) || a.Kind != "code" || a.Color != "green" || a.Resolution == nil || !validHistoricalProviderKey(a.ProviderKey)) {
		return ErrNotificationIdentity
	}
	if a.TerminationReason != "" && (!notificationConditionStorageFormat(version) || !validNotificationReason(a.TerminationReason)) {
		return ErrNotificationIdentity
	}
	if a.Resolution != nil && (!notificationConditionStorageFormat(version) || a.Kind != "code" || a.Color != "green" || !catalogIdentifier(a.Resolution.ConditionID, 256) || a.Resolution.RecoveredAt.IsZero() || !validResolutionHealth(a.Resolution.Health)) {
		return ErrNotificationIdentity
	}
	if a.ExecutionBudget == 0 {
		return nil // Existing actions never acquire a new execution budget.
	}
	if !notificationConditionStorageFormat(version) || a.Kind != "code" || a.ExecutionBudget < 0 || a.ExecutionBudget > 24*time.Hour || a.QueuedAt.IsZero() {
		return ErrNotificationIdentity
	}
	if a.Deadline.IsZero() && (a.State == Started || a.State == Succeeded || a.State == Failed || a.State == Unknown || a.Attempt > 1 || !a.StartedAt.IsZero()) {
		return ErrNotificationIdentity
	}
	return nil
}

// NotificationResolution freezes the destination of an exposed episode. It
// remains pending through suppression without keeping completed action history
// in the monitor's execution projection.
type NotificationResolution struct {
	DeliveryID                 string        `json:"delivery_id,omitempty"`
	ProviderKey                string        `json:"provider_key,omitempty"`
	MigrationReason            string        `json:"migration_reason,omitempty"`
	ID                         string        `json:"id"`
	Revision                   string        `json:"revision"`
	CatalogUID                 string        `json:"catalog_uid,omitempty"`
	FileIncarnation            string        `json:"file_incarnation,omitempty"`
	IncidentID                 string        `json:"incident_id,omitempty"`
	Correlation                string        `json:"correlation"`
	Episode                    string        `json:"episode"`
	EndpointID                 string        `json:"endpoint_id"`
	Endpoint                   int           `json:"endpoint"`
	ProviderCorrelationVersion int           `json:"provider_correlation_version"`
	Budget                     time.Duration `json:"budget"`
	OpenedAt                   time.Time     `json:"opened_at"`
	RecoveredAt                time.Time     `json:"recovered_at,omitzero"`
	OpeningActionID            string        `json:"opening_action_id,omitempty"`
	ActionID                   string        `json:"action_id,omitempty"`
	State                      ActionState   `json:"state,omitempty"`
	Reason                     string        `json:"reason,omitempty"`
	Started                    bool          `json:"started,omitempty"`
	ExecutionDeadline          time.Time     `json:"execution_deadline,omitzero"`
}

type ResolutionNotice struct {
	ConditionID string    `json:"condition_id"`
	RecoveredAt time.Time `json:"recovered_at"`
	Health      string    `json:"health"`
}

func validDeliveryState(state ActionState) bool {
	return state == Queued || state == Started || state == Succeeded || state == Failed || state == Unknown || state == Cancelled
}
func validNotificationReason(reason string) bool {
	return reason == "" || catalogIdentifier(reason, 128)
}
func validResolutionHealth(health string) bool {
	return health == "unknown" || health == "healthy" || health == "unhealthy"
}

func (m Monitor) notificationTermination(a Action, c Command) string {
	if a.State == Unknown {
		return "outcome_unknown"
	}
	if a.State != Failed {
		return ""
	}
	if !c.Retryable {
		return "permanent_rejection"
	}
	if a.Kind == "intervention" {
		if a.Attempt >= m.Policy.recoveryLimit() {
			return "attempt_limit"
		}
		if a.Deadline.IsZero() || !c.At.Add(m.Policy.RecoveryCooldown).Before(a.Deadline) {
			return "execution_deadline"
		}
	}
	if a.Kind == "code" && a.Attempt >= 3 {
		return "attempt_limit"
	}
	if a.Kind == "code" && !notificationRetryAt(c, a.Attempt).Before(a.Deadline) {
		return "execution_deadline"
	}
	return ""
}

func (m *Monitor) recordNotificationCondition(a Action) {
	if !m.notificationConditionsEnabled() || a.Kind != "code" {
		return
	}
	conditionID := ""
	if a.Resolution != nil {
		conditionID = a.Resolution.ConditionID
	} else if a.Color == "green" && a.DeliveryID != "" {
		// Imported fixed-budget actions keep their exact bytes and review
		// digest. Bind progress by the original logical delivery, including a
		// proven safe retry with a new attempt identity.
		for id, v := range m.Notifications.Resolutions {
			if v.DeliveryID == a.DeliveryID && v.Revision == a.Revision && v.CatalogUID == a.CatalogUID &&
				v.FileIncarnation == a.FileIncarnation && v.Correlation == a.NotificationCorrelation && v.EndpointID == a.EndpointID {
				conditionID = id
				break
			}
		}
	}
	if conditionID != "" {
		v, ok := m.Notifications.Resolutions[conditionID]
		if ok {
			v.ActionID, v.State, v.Reason = a.ID, a.State, a.TerminationReason
			v.DeliveryID = a.DeliveryID
			if a.State == Queued && v.Reason == "" {
				v.Reason = "awaiting_execution_capacity"
			}
			v.Started = v.Started || !a.StartedAt.IsZero() || a.Attempt > 1
			if !a.Deadline.IsZero() {
				v.ExecutionDeadline = a.Deadline
			}
			if a.State == Cancelled {
				v.Reason = a.Outcome
			}
			m.Notifications.Resolutions[v.ID] = v
		}
		return
	}
	if a.Control != nil || a.ReviewNotice != nil || a.NotificationEpisode != m.NotificationEpisode || a.DeliveryID == "" {
		return
	}
	if m.Notifications.Deliveries == nil {
		m.Notifications.Deliveries = make(map[string]DeliveryState)
	}
	slot := deliverySlot(a.Color, a.EndpointID)
	d := m.Notifications.Deliveries[slot]
	if d.Episode != a.NotificationEpisode || d.Condition != deliveryCondition(a.RecoveryFailure) {
		d = DeliveryState{Episode: a.NotificationEpisode, Condition: deliveryCondition(a.RecoveryFailure)}
	}
	d.ActionID, d.PendingAt, d.State, d.Reason = a.ID, a.QueuedAt, a.State, a.TerminationReason
	if a.State == Queued && d.Reason == "" {
		d.Reason = "awaiting_execution_capacity"
	}
	if a.State == Cancelled {
		d.Reason = a.Outcome
	}
	if !a.StartedAt.IsZero() {
		d.Started = true
		if d.StartedAt.IsZero() {
			d.StartedAt = a.StartedAt
		}
	}
	m.Notifications.Deliveries[slot] = d
}

func (m *Monitor) captureNotificationResolution(a Action) {
	if !m.notificationConditionsEnabled() || a.Kind != "code" || a.Resolution != nil || a.ReviewNotice != nil ||
		(a.Color != "yellow" && a.Color != "red" && a.Color != "cyan") || a.State != Started || a.NotificationCorrelation != m.NotificationCorrelation {
		return
	}
	m.captureResolutionTargets(a.NotificationCorrelation, a.ID, a.StartedAt, a.ProviderCorrelationVersion)
}

func (m *Monitor) captureResolutionTargets(correlation, opening string, at time.Time, providerVersion int) {
	if m.Notifications.Resolutions == nil {
		m.Notifications.Resolutions = make(map[string]NotificationResolution)
	}
	for endpoint := range m.Policy.Endpoints["green"] {
		endpointID := notificationEndpointIdentity(*m, "green", endpoint)
		id := m.namespacedIdentity(identity("resolution/" + m.incarnationIdentity() + "/" + correlation + "/" + endpointID))
		if _, exists := m.Notifications.Resolutions[id]; exists {
			continue
		}
		m.Notifications.Resolutions[id] = NotificationResolution{ID: id, Revision: m.Revision, CatalogUID: m.CatalogUID,
			FileIncarnation: m.FileIncarnation, IncidentID: m.notificationIncident(), Correlation: correlation,
			Episode: identity("resolved/" + correlation), EndpointID: endpointID, Endpoint: endpoint,
			ProviderCorrelationVersion: providerVersion, Budget: m.Policy.notificationTimeout("green", endpoint), OpenedAt: at, OpeningActionID: opening}
	}
}

func (m *Monitor) resolveNotificationConditions(at time.Time, emit func(string, *Action)) {
	// Recovery resolves only previously authorized destinations. A missing
	// condition can mean configuration cancellation; it is not permission to
	// recreate the old episode using the current destination. Ignore recovery
	// and supported-format migration capture their own explicit authorization.
	for id, v := range m.Notifications.Resolutions {
		if v.Correlation == m.NotificationCorrelation && v.RecoveredAt.IsZero() {
			v.RecoveredAt = at
			m.Notifications.Resolutions[id] = v
		}
	}
	m.assessNotificationResolutions(at, emit)
}

func (m Monitor) resolutionEligible(a Action) bool {
	if a.Resolution == nil || !m.notificationConditionsEnabled() {
		return false
	}
	v, ok := m.Notifications.Resolutions[a.Resolution.ConditionID]
	return ok && v.ActionID == a.ID && !v.RecoveredAt.IsZero() && v.RecoveredAt.Equal(a.Resolution.RecoveredAt) &&
		v.Revision == m.Revision && v.CatalogUID == m.CatalogUID && v.FileIncarnation == m.FileIncarnation &&
		v.Endpoint < m.Policy.Endpoints["green"] && v.EndpointID == notificationEndpointIdentity(m, "green", v.Endpoint) &&
		a.NotificationCorrelation == v.Correlation && a.EndpointID == v.EndpointID && a.Resolution.Health == m.notificationHealth()
}

// refreshQueuedNotificationHealth revises only already-authorized closure
// payloads. It creates no action or recovery fact and cannot renew a budget.
func (m *Monitor) refreshQueuedNotificationHealth(health string, emit func(string, *Action)) {
	if !m.notificationConditionsEnabled() {
		return
	}
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.State != Queued || !m.OwnsAction(a) || a.Revision != m.Revision {
			continue
		}
		kind := ""
		switch {
		case a.Resolution != nil && a.Resolution.Health != health:
			a = a.Clone()
			a.Resolution.Health = health
			kind = "notification_resolution_updated"
		case a.ReviewNotice != nil && a.ReviewNotice.Phase == "close" && a.ReviewNotice.Health != health:
			a = a.Clone()
			a.ReviewNotice.Health = health
			kind = "review_alert_updated"
			if alert, ok := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]; ok && alert.ClosingDelivery == a.DeliveryID {
				alert.Health = health
				m.Notifications.ReviewAlerts[alert.ID] = alert
			}
		default:
			continue
		}
		m.Actions[id] = a
		emit(kind, &a)
	}
}

// cancelObsoleteResolutions retires execution bookkeeping only after recording
// why an unstarted condition can no longer use its original destination. The
// action ledger retains provider facts, reviews and independent unknown holds.
func (m *Monitor) cancelObsoleteResolutions(at time.Time, emit func(string, *Action)) []Event {
	if !m.notificationConditionsEnabled() {
		return nil
	}
	var events []Event
	for _, id := range slices.Sorted(maps.Keys(m.Notifications.Resolutions)) {
		v := m.Notifications.Resolutions[id]
		if v.MigrationReason != "" || v.State == Started || v.State == Unknown {
			continue
		}
		if !m.Removed && v.Revision == m.Revision && v.CatalogUID == m.CatalogUID && v.FileIncarnation == m.FileIncarnation &&
			v.Endpoint < m.Policy.Endpoints["green"] && v.EndpointID == notificationEndpointIdentity(*m, "green", v.Endpoint) {
			continue
		}
		if v.State != Succeeded && v.State != Failed {
			reason := "configuration_changed"
			if m.Removed {
				reason = "configuration_removed"
			}
			if a, ok := m.Actions[v.ActionID]; ok && a.State == Queued {
				m.cancelNotification(a, reason, at, emit)
			}
			events = append(events, Event{MonitorID: m.ID, Revision: v.Revision, CatalogUID: v.CatalogUID,
				FileIncarnation: v.FileIncarnation, IncidentID: v.IncidentID, At: at,
				Type: "notification_resolution_cancelled", ConditionID: id, OpeningActionID: v.OpeningActionID,
				ActionID: v.ActionID, NotificationEpisode: v.Episode, EndpointID: v.EndpointID,
				Kind: "code", Color: "green", Endpoint: v.Endpoint, Reason: reason, Outcome: reason})
		}
		delete(m.Notifications.Resolutions, id)
	}
	return events
}

func (m *Monitor) assessNotificationResolutions(at time.Time, emit func(string, *Action)) {
	if !m.notificationConditionsEnabled() {
		return
	}
	for _, id := range slices.Sorted(maps.Keys(m.Notifications.Resolutions)) {
		v := m.Notifications.Resolutions[id]
		if v.MigrationReason != "" {
			v.Reason = v.MigrationReason
			m.Notifications.Resolutions[id] = v
			continue
		}
		// Terminal outcome evidence belongs in the action ledger. Keep the small
		// current-episode summary, and drop it after a new correlation takes over.
		if !resolutionRetainsAction(v) {
			if v.Correlation != m.NotificationCorrelation {
				delete(m.Notifications.Resolutions, id)
			}
			continue
		}
		if v.RecoveredAt.IsZero() {
			continue
		}
		if v.State == Started {
			continue
		}
		if v.State == Cancelled {
			if v.Started || !v.ExecutionDeadline.IsZero() {
				continue
			}
			switch v.Reason {
			case "maintenance", "monitor_ignored", "snooze", "dismiss", "notification_superseded", "worker_notification_changed", "worker_not_started":
			default:
				continue
			}
		}
		if v.Revision != m.Revision || v.CatalogUID != m.CatalogUID || v.FileIncarnation != m.FileIncarnation ||
			v.Endpoint >= m.Policy.Endpoints["green"] || v.EndpointID != notificationEndpointIdentity(*m, "green", v.Endpoint) {
			v.Reason = "configuration_changed"
			m.Notifications.Resolutions[id] = v
			continue
		}
		if m.notificationsSuppressed(at) {
			v.Reason = "suppressed"
			m.Notifications.Resolutions[id] = v
			continue
		}
		if a, exists := m.Actions[v.ActionID]; exists {
			if a.State == Queued {
				if a.Resolution != nil && a.Resolution.Health != m.notificationHealth() {
					a.Resolution = &ResolutionNotice{ConditionID: id, RecoveredAt: v.RecoveredAt, Health: m.notificationHealth()}
					m.Actions[a.ID] = a
					emit("notification_resolution_updated", &a)
				}
				continue
			}
			if a.State != Cancelled || !a.StartedAt.IsZero() || a.Attempt > 1 {
				continue
			}
			switch a.Outcome {
			case "maintenance", "monitor_ignored", "snooze", "dismiss", "notification_superseded", "worker_notification_changed", "worker_not_started":
			default:
				continue
			}
			if !a.Deadline.IsZero() {
				continue
			} // Never renew an existing absolute budget.
		}
		m.Sequence++
		actionID := m.namespacedIdentity(identity(fmt.Sprintf("resolution-delivery/%s/%d", id, m.Sequence)))
		a := Action{ID: actionID, DeliveryID: actionID, Revision: v.Revision, CatalogUID: v.CatalogUID, FileIncarnation: v.FileIncarnation,
			IncidentID: v.IncidentID, Kind: "code", Color: "green", Endpoint: v.Endpoint, EndpointID: v.EndpointID,
			NotificationEpisode: v.Episode, NotificationCorrelation: v.Correlation, ProviderCorrelationVersion: v.ProviderCorrelationVersion,
			ProviderKey: v.ProviderKey, ExecutionBudget: v.Budget, QueuedAt: at, NotBefore: at, Attempt: 1, State: Queued,
			Resolution: &ResolutionNotice{ConditionID: id, RecoveredAt: v.RecoveredAt, Health: m.notificationHealth()}}
		v.ActionID, v.State, v.Reason = actionID, Queued, ""
		m.Notifications.Resolutions[id] = v
		if m.Actions == nil {
			m.Actions = make(map[string]Action)
		}
		m.Actions[actionID] = a
		emit("action_queued", &a)
	}
}

func (m Monitor) validateResolutionConditions(version int) error {
	if len(m.Notifications.Resolutions) == 0 {
		return nil
	}
	if !notificationConditionStorageFormat(version) || !m.notificationConditionsEnabled() {
		return ErrNotificationIdentity
	}
	for id, v := range m.Notifications.Resolutions {
		if !validNotificationReason(v.MigrationReason) || v.ProviderKey != "" && !validHistoricalProviderKey(v.ProviderKey) || id != v.ID || !catalogIdentifier(id, 256) || v.Revision == "" || len(v.Revision) > 512 ||
			!catalogIdentifier(v.Correlation, 256) || !catalogIdentifier(v.Episode, 256) || !catalogIdentifier(v.EndpointID, 256) ||
			v.Endpoint < 0 || v.OpenedAt.IsZero() || !v.RecoveredAt.IsZero() && v.RecoveredAt.Before(v.OpenedAt) ||
			v.Budget <= 0 || v.Budget > 24*time.Hour || v.State != "" && !validDeliveryState(v.State) || !validNotificationReason(v.Reason) ||
			v.ActionID != "" && !catalogIdentifier(v.ActionID, 256) || v.OpeningActionID != "" && !catalogIdentifier(v.OpeningActionID, 256) || v.DeliveryID != "" && !catalogIdentifier(v.DeliveryID, 256) {
			return ErrNotificationIdentity
		}
	}
	return nil
}
