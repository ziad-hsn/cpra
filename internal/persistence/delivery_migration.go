package persistence

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// DeliveryMigration retains the evidence used to bind an older delivery. The
// action itself keeps its original identity and review revision. An ordinal is
// usable only with the exact executable configuration that admitted it.
type DeliveryMigration struct {
	ActionID        string      `json:"action_id"`
	ReviewRevision  string      `json:"review_revision"`
	Revision        string      `json:"revision"`
	CatalogUID      string      `json:"catalog_uid,omitempty"`
	FileIncarnation string      `json:"file_incarnation,omitempty"`
	IncidentID      string      `json:"incident_id,omitempty"`
	Name            string      `json:"name"`
	Color           string      `json:"color"`
	Endpoint        int         `json:"endpoint"`
	State           ActionState `json:"state"`
	Started         bool        `json:"started"`
	StartedAt       time.Time   `json:"started_at,omitzero"`
	Bound           bool        `json:"bound"`
	EndpointID      string      `json:"endpoint_id,omitempty"`
	Reason          string      `json:"reason,omitempty"`
}

func requiresNotificationConditions(m Monitor) bool {
	if m.LastFailure.requiresConditionFormat() || m.ConfigurationProblem.requiresConditionFormat() {
		return true
	}
	if len(m.Policy.NotificationDrivers) != 0 || m.Notifications != nil && (m.Notifications.Version != 0 || len(m.Notifications.Resolutions) != 0 || len(m.Notifications.Migrations) != 0) {
		return true
	}
	for _, a := range m.Actions {
		if a.ExecutionBudget != 0 || a.TerminationReason != "" || a.Resolution != nil || a.ProviderKey != "" || a.Cause.requiresConditionFormat() || a.Failure.requiresConditionFormat() {
			return true
		}
	}
	return false
}

func (m *Monitor) captureDeliveryMigration() {
	if m.ID == "" || m.NotificationVersion != 0 {
		return
	}
	if m.Notifications == nil {
		m.Notifications = &NotificationState{}
	}
	if m.Notifications.Migrations == nil {
		m.Notifications.Migrations = make(map[string]DeliveryMigration)
	}
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.Kind != "code" || !m.OwnsAction(a) || a.IncidentID != m.IncidentID || a.DeliveryID != "" ||
			!slices.Contains([]string{"red", "yellow", "cyan", "green"}, a.Color) {
			continue
		}
		if _, exists := m.Notifications.Migrations[id]; exists {
			continue
		}
		m.Notifications.Migrations[id] = DeliveryMigration{ActionID: id, ReviewRevision: ActionReviewRevision(a), Revision: a.Revision,
			CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, IncidentID: a.IncidentID, Name: m.Name,
			Color: a.Color, Endpoint: a.Endpoint, State: a.State, Started: !a.StartedAt.IsZero() || a.Attempt > 1,
			StartedAt: a.StartedAt, Reason: "migration_binding_unverified"}
	}
	// The pre-40 provider algorithms do not have the meaning assigned to version
	// zero in format 40. Do not rewrite their action fields during the subsequent
	// lifecycle upgrade. Binding is explicit and happens only after configure.
	m.LifecycleVersion = LifecycleFormatVersion
}

func (m Monitor) hasDeliveryMigration() bool {
	if m.Notifications == nil {
		return false
	}
	for _, v := range m.Notifications.Migrations {
		if v.IncidentID == m.IncidentID {
			return true
		}
	}
	return false
}

func (m Monitor) migratedOpeningHeld(color string, endpoint int) bool {
	if m.Notifications == nil {
		return false
	}
	for _, v := range m.Notifications.Migrations {
		if v.Color == color && v.IncidentID == m.IncidentID && (!v.Bound || v.Started && v.Endpoint == endpoint) {
			return true
		}
	}
	return false
}

func (m *Monitor) bindDeliveryMigration(at time.Time) {
	if !m.hasDeliveryMigration() {
		return
	}
	// Keep current episode assessment stable across the first post-upgrade pulse.
	if m.IncidentID != "" {
		m.NotificationCorrelation = m.namespacedIdentity(m.IncidentID)
		if m.IncidentClosedAt.IsZero() {
			m.NotificationEpisode = identity("outage/" + m.IncidentID)
		} else {
			m.NotificationEpisode = identity("recovered/" + m.IncidentID)
		}
	}
	ids := sortedMigrationIDs(m.Notifications.Migrations)
	for _, id := range ids {
		v := m.Notifications.Migrations[id]
		if v.IncidentID != m.IncidentID {
			continue
		}
		v.Bound, v.Reason = false, "migration_binding_unverified"
		endpoints, drivers := m.Policy.NotificationEndpointIDs[v.Color], m.Policy.NotificationDrivers[v.Color]
		if v.Revision == m.Revision && v.CatalogUID == m.CatalogUID && v.FileIncarnation == m.FileIncarnation &&
			v.Endpoint >= 0 && v.Endpoint < m.Policy.Endpoints[v.Color] && v.Endpoint < len(endpoints) && v.Endpoint < len(drivers) {
			v.Bound, v.Reason, v.EndpointID = true, "", notificationEndpointIdentity(*m, v.Color, v.Endpoint)
		}
		m.Notifications.Migrations[id] = v
		if v.Started && v.Color != "green" {
			m.NotificationAttempted = m.NotificationCorrelation
			opened := v.StartedAt
			if opened.IsZero() {
				opened = m.IncidentOpenedAt
			}
			if !opened.IsZero() {
				m.captureResolutionTargets(m.NotificationCorrelation, v.ActionID, opened, 0)
			}
		}
	}
	// Each closing destination selects a provable opening. Map or action order
	// must not let an unrelated yellow delivery mask a retained PagerDuty start.
	for key, resolution := range m.Notifications.Resolutions {
		if resolution.Correlation != m.NotificationCorrelation || resolution.Started || resolution.ActionID != "" {
			continue
		}
		resolution.MigrationReason = "migration_binding_unverified"
		greenDrivers := m.Policy.NotificationDrivers["green"]
		if resolution.Endpoint >= len(greenDrivers) {
			m.Notifications.Resolutions[key] = resolution
			continue
		}
		driver := greenDrivers[resolution.Endpoint]
		for _, id := range ids {
			opening := m.Notifications.Migrations[id]
			if opening.IncidentID != m.IncidentID || !opening.Started || opening.Color == "green" || !opening.Bound {
				continue
			}
			if driver == "opsgenie" {
				// Historical aliases were process-local UUIDs absent from state.
				resolution.MigrationReason = "migration_provider_correlation_unverified"
				continue
			}
			if driver == "pagerduty" {
				if m.Policy.NotificationDrivers[opening.Color][opening.Endpoint] != "pagerduty" || !validHistoricalProviderKey("cpra:"+opening.Name) {
					resolution.MigrationReason = "migration_provider_correlation_unverified"
					continue
				}
				// Pre-40 used this literal key, not format-40's version-zero hash.
				resolution.ProviderKey = "cpra:" + opening.Name
			}
			resolution.OpeningActionID, resolution.MigrationReason = opening.ActionID, ""
			break
		}
		if !m.IncidentClosedAt.IsZero() {
			resolution.RecoveredAt = m.IncidentClosedAt
		}
		for _, id := range ids {
			green := m.Notifications.Migrations[id]
			if green.Color != "green" || !green.Started || green.IncidentID != m.IncidentID || green.Endpoint != resolution.Endpoint {
				continue
			}
			if !green.Bound || green.EndpointID != resolution.EndpointID {
				resolution.MigrationReason = "migration_binding_unverified"
			}
			if resolution.ActionID != "" {
				// Multiple retained starts cannot be collapsed to whichever map
				// entry happens to be last. Hold the combined evidence explicitly.
				resolution.State = Unknown
				resolution.MigrationReason = "migration_delivery_evidence_conflict"
				continue
			}
			resolution.ActionID, resolution.Started, resolution.State = green.ActionID, true, green.State
			if green.State == Started || green.State == Queued {
				resolution.State = Unknown
			}
		}
		m.Notifications.Resolutions[key] = resolution
	}
}

func sortedMigrationIDs(values map[string]DeliveryMigration) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func validHistoricalProviderKey(key string) bool {
	if !utf8.ValidString(key) || !strings.HasPrefix(key, "cpra:") || len(key) > 255 || len(key) <= 5 {
		return false
	}
	for _, c := range key {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func (m Monitor) validateDeliveryMigrations(version int) error {
	if len(m.Notifications.Migrations) == 0 {
		return nil
	}
	if !notificationConditionStorageFormat(version) || !m.notificationConditionsEnabled() {
		return ErrNotificationIdentity
	}
	for id, v := range m.Notifications.Migrations {
		if id != v.ActionID || !catalogIdentifier(id, 256) || !catalogIdentifier(v.ReviewRevision, 256) || v.Revision == "" || len(v.Revision) > 512 ||
			!slices.Contains([]string{"red", "yellow", "cyan", "green"}, v.Color) || v.Endpoint < 0 || !validDeliveryState(v.State) ||
			!validNotificationReason(v.Reason) || v.Bound && !catalogIdentifier(v.EndpointID, 256) || !v.Started && !v.StartedAt.IsZero() {
			return ErrNotificationIdentity
		}
	}
	return nil
}

func (m Monitor) notificationRetainsAction(id string) bool {
	if m.Notifications == nil {
		return false
	}
	for _, v := range m.Notifications.Deliveries {
		if v.ActionID == id && (v.State == Queued || v.State == Started || v.State == "") && !v.Terminal {
			return true
		}
	}
	for _, v := range m.Notifications.Resolutions {
		if (v.ActionID == id || v.OpeningActionID == id) && resolutionRetainsAction(v) {
			return true
		}
	}
	if v, ok := m.Notifications.Migrations[id]; ok && (v.IncidentID == m.IncidentID || v.Reason != "") {
		return true
	}
	return false
}
