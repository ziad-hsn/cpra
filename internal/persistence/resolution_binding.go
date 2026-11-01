package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// NotificationBinding contains only an opaque digest of authorized execution
// dependencies. Presentation and scheduling edits do not change that authority.
func NotificationBinding(check, endpoint, credentials string) string {
	raw, _ := json.Marshal([]string{"cpra-notification-binding-v1", check, endpoint, credentials})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (m Monitor) resolutionBinding(endpoint int) string {
	bindings := m.Policy.NotificationBindings["green"]
	if endpoint < 0 || endpoint >= len(bindings) {
		return ""
	}
	return bindings[endpoint]
}
func (m *Monitor) prepareResolutionSlots() {
	m.resolutionSlots = nil
	if len(m.Policy.NotificationBindings["green"]) == 0 {
		return
	}
	m.resolutionSlots = make(map[string]int, len(m.Policy.NotificationBindings["green"]))
	for slot, binding := range m.Policy.NotificationBindings["green"] {
		if _, exists := m.resolutionSlots[binding]; exists {
			m.resolutionSlots[binding] = -1
		} else {
			m.resolutionSlots[binding] = slot
		}
	}
}
func (m Monitor) resolutionEndpoint(v NotificationResolution) int {
	if v.Binding != "" {
		if m.resolutionSlots == nil {
			m.prepareResolutionSlots()
		}
		if slot, ok := m.resolutionSlots[v.Binding]; ok && slot >= 0 {
			drivers := m.Policy.NotificationDrivers["green"]
			if slot < len(drivers) && drivers[slot] == "external" || notificationEndpointIdentity(m, "green", slot) == v.EndpointID {
				return slot
			}
		}
		return -1
	}
	if v.Endpoint >= 0 && v.Endpoint < m.Policy.Endpoints["green"] && notificationEndpointIdentity(m, "green", v.Endpoint) == v.EndpointID {
		return v.Endpoint
	}
	return -1
}
func (m Monitor) resolutionAuthorized(v NotificationResolution) bool {
	if m.Removed || v.CatalogUID != m.CatalogUID || v.FileIncarnation != m.FileIncarnation || v.MigrationReason != "" {
		return false
	}
	endpoint := m.resolutionEndpoint(v)
	if endpoint < 0 {
		return false
	}
	if v.Binding == "" {
		return v.Revision == m.Revision && endpoint == v.Endpoint
	}
	return v.Binding == m.resolutionBinding(endpoint)
}
func (m Monitor) retainedResolution(a Action) bool {
	if a.Resolution == nil || m.Notifications == nil || a.ID == "" {
		return false
	}
	v, ok := m.Notifications.Resolutions[a.Resolution.ConditionID]
	return ok && v.Binding != "" && v.ActionID == a.ID && v.Revision == a.Revision && v.EndpointID == a.EndpointID && m.resolutionAuthorized(v)
}

// Blue assessment keeps its captured destination slot. A binding proves the
// target and credential dependencies, but never authorizes a replacement slot.
// Older alerts without that proof retain their original revision restriction.
func (m Monitor) reviewBinding(endpoint int) string {
	if binding, ok := m.reviewBindings[endpoint]; ok {
		return binding
	}
	bindings := m.Policy.NotificationBindings["blue"]
	if endpoint >= 0 && endpoint < len(bindings) && m.ActionVersion == DispatchFormatVersion {
		return bindings[endpoint]
	}
	return ""
}

func (m Monitor) reviewBindingOwner(endpoint int) *reviewAlertOwner {
	if m.reviewBinding(endpoint) == "" {
		return nil
	}
	return &reviewAlertOwner{CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation}
}

func (m Monitor) reviewAlertAuthorized(alert ReviewAlert) bool {
	if m.Removed || alert.Endpoint < 0 || alert.Endpoint >= m.Policy.Endpoints["blue"] || notificationEndpointIdentity(m, "blue", alert.Endpoint) != alert.EndpointID {
		return false
	}
	if alert.Binding == "" {
		return alert.Revision == m.Revision
	}
	return m.ActionVersion == DispatchFormatVersion && alert.Owner != nil && alert.Owner.CatalogUID == m.CatalogUID && alert.Owner.FileIncarnation == m.FileIncarnation && alert.Binding == m.reviewBinding(alert.Endpoint)
}

func (m Monitor) retainedReviewNotice(a Action) bool {
	if m.ActionVersion != DispatchFormatVersion || m.Removed || !m.OwnsAction(a) || a.ID == "" || a.ReviewBinding == "" || a.ReviewNotice == nil || a.ReviewNotice.Phase != "close" || a.Kind != "code" || a.Color != "blue" || a.Endpoint < 0 || a.Endpoint >= m.Policy.Endpoints["blue"] || a.ReviewBinding != m.reviewBinding(a.Endpoint) || a.EndpointID != notificationEndpointIdentity(m, "blue", a.Endpoint) {
		return false
	}
	// Active projections deliberately exclude historical review conditions.
	// The action binding was validated against its original retained condition
	// at commit. Hydrated authoritative transitions also check that condition.
	if m.Notifications != nil {
		if alert, ok := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]; ok {
			return alert.Binding == a.ReviewBinding && alert.ActionID == a.ReviewNotice.ActionID && alert.Revision == a.Revision && alert.EndpointID == a.EndpointID && alert.Endpoint == a.Endpoint && a.DeliveryID == alert.ClosingDelivery && alert.OpeningStarted && m.reviewAlertAuthorized(alert)
		}
	}
	return true
}

func (m Monitor) retainedNotification(a Action) bool {
	return m.retainedResolution(a) || m.retainedReviewNotice(a)
}

// ActionConfigurationCurrent retains a resolution only with unchanged target,
// endpoint and credential authority. Callers still enforce executor/control fences.
func (m Monitor) ActionConfigurationCurrent(a Action) bool {
	return a.Revision == m.Revision || m.retainedNotification(a)
}

// ActionEndpoint returns the currently authorized runtime slot without rewriting
// historical endpoint ordinals in action evidence.
func (m Monitor) ActionEndpoint(a Action) int {
	if m.retainedResolution(a) {
		return m.resolutionEndpoint(m.Notifications.Resolutions[a.Resolution.ConditionID])
	}
	return a.Endpoint
}
func (m *Monitor) bindResolutionUpgrade(config Monitor) {
	if m.Notifications == nil {
		return
	}
	config.FileIncarnation = m.FileIncarnation
	for id, v := range m.Notifications.Resolutions {
		if v.Binding != "" || v.MigrationReason != "" {
			continue
		}
		if m.Revision != config.Revision || v.Revision != m.Revision || m.CatalogUID != "" && m.DependencyRevision != config.DependencyRevision {
			v.MigrationReason = "migration_binding_uncertain"
			m.Notifications.Resolutions[id] = v
			continue
		}
		if v.Endpoint < config.Policy.Endpoints["green"] && v.EndpointID == notificationEndpointIdentity(config, "green", v.Endpoint) {
			v.Binding = config.resolutionBinding(v.Endpoint)
			m.Notifications.Resolutions[id] = v
		}
	}
}
