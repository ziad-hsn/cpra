package persistence

import (
	"errors"
	"maps"
	"slices"
	"time"
)

// NotificationState retains the current episode's eligibility independently of
// retained action evidence. Its size follows configured destinations, not checks.
type NotificationState struct {
	OutsideFailures   int                      `json:"outside_failures,omitempty"`
	QualifiedIncident string                   `json:"qualified_incident,omitempty"`
	MaintenanceOutage bool                     `json:"maintenance_outage,omitempty"`
	IgnoreOutage      string                   `json:"ignore_outage,omitempty"`
	Deliveries        map[string]DeliveryState `json:"deliveries,omitempty"`
	ReviewAlerts      map[string]ReviewAlert   `json:"review_alerts,omitempty"`
}

type DeliveryState struct {
	Episode   string    `json:"episode"`
	Condition string    `json:"condition,omitempty"`
	Started   bool      `json:"started,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	Terminal  bool      `json:"terminal,omitempty"`
}

func (s *NotificationState) clone() *NotificationState {
	if s == nil {
		return nil
	}
	c := *s
	c.Deliveries = maps.Clone(s.Deliveries)
	c.ReviewAlerts = maps.Clone(s.ReviewAlerts)
	for id, alert := range c.ReviewAlerts {
		if alert.Owner != nil {
			owner := *alert.Owner
			alert.Owner = &owner
			c.ReviewAlerts[id] = alert
		}
	}
	return &c
}

func (m *Monitor) upgradeNotificationState() {
	if m.Notifications != nil {
		return
	}
	m.Notifications = &NotificationState{}
	// Exposure requires a recorded start. Initial queued/cancelled deliveries
	// and health observations do not establish that a notification was attempted.
	ids := sortedActions(m.Actions)
	slices.SortStableFunc(ids, func(left, right string) int {
		a, b := m.Actions[left], m.Actions[right]
		return notificationExposureTime(a).Compare(notificationExposureTime(b))
	})
	for _, id := range ids {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.Kind == "code" && a.NotificationEpisode == m.NotificationEpisode && (!a.StartedAt.IsZero() || a.Attempt > 1) {
			m.recordNotificationStart(a)
		}
	}
	m.pruneNotificationState()
}

func notificationExposureTime(a Action) time.Time {
	if !a.StartedAt.IsZero() {
		return a.StartedAt
	}
	// Admission time orders an older retry conservatively. It is never written
	// as StartedAt: the exact previous start is not available in that record.
	return a.QueuedAt
}

func deliveryCondition(failure *RecoveryFailure) string {
	if failure != nil {
		return failure.ActionID
	}
	return ""
}

func deliverySlot(color, endpointID string) string { return color + "/" + endpointID }

func (m Monitor) deliveryStarted(color string, endpoint int, failure *RecoveryFailure) bool {
	if m.Notifications == nil {
		return false
	}
	v := m.Notifications.Deliveries[deliverySlot(color, notificationEndpointIdentity(m, color, endpoint))]
	return (v.Started || v.Terminal) && v.Episode == m.NotificationEpisode && v.Condition == deliveryCondition(failure)
}

func (m *Monitor) recordNotificationCancellation(a Action) {
	if m.Notifications == nil || a.Control != nil || a.ReviewNotice != nil || a.NotificationEpisode != m.NotificationEpisode {
		return
	}
	switch a.Outcome {
	case "notification_deadline", "notification_deadline_unrecorded", "notification_identity_incomplete", "worker_admission_expired":
	default:
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
	d.Terminal = true
	m.Notifications.Deliveries[slot] = d
}

func (m Monitor) deliveryCooling(color string, endpoint int, at time.Time) bool {
	if m.Notifications == nil || color == "green" && m.Policy.RecoveryBypass {
		return false
	}
	v := m.Notifications.Deliveries[deliverySlot(color, notificationEndpointIdentity(m, color, endpoint))]
	return v.Started && v.Episode == m.NotificationEpisode && !v.StartedAt.IsZero() && at.Before(v.StartedAt.Add(m.Policy.Cooldown))
}

func (m *Monitor) pruneNotificationState() {
	if m.Notifications == nil {
		return
	}
	valid := make(map[string]bool)
	for color, count := range m.Policy.Endpoints {
		for endpoint := range count {
			valid[deliverySlot(color, notificationEndpointIdentity(*m, color, endpoint))] = true
		}
	}
	for slot := range m.Notifications.Deliveries {
		if !valid[slot] {
			delete(m.Notifications.Deliveries, slot)
		}
	}
}

func (m *Monitor) recordNotificationStart(a Action) {
	if m.Notifications == nil || a.Kind != "code" || a.Control != nil || a.ReviewNotice != nil || a.NotificationEpisode != m.NotificationEpisode {
		return
	}
	if m.Notifications.Deliveries == nil {
		m.Notifications.Deliveries = make(map[string]DeliveryState)
	}
	m.Notifications.Deliveries[deliverySlot(a.Color, a.EndpointID)] = DeliveryState{Episode: a.NotificationEpisode, Condition: deliveryCondition(a.RecoveryFailure), Started: true, StartedAt: a.StartedAt}
	if m.Cooldowns == nil {
		m.Cooldowns = make(map[string]time.Time)
	}
	m.Cooldowns[a.Color] = a.StartedAt
}

func (m *Monitor) observeOutageQualification(c Command) {
	s := m.Notifications
	if s == nil {
		return
	}
	if c.Maintenance {
		s.OutsideFailures = 0
		if s.QualifiedIncident != m.IncidentID {
			s.MaintenanceOutage = true
		}
		return
	}
	s.OutsideFailures = min(max(1, m.Policy.Unhealthy), s.OutsideFailures+1)
	if s.OutsideFailures >= max(1, m.Policy.Unhealthy) {
		s.QualifiedIncident, s.MaintenanceOutage = m.IncidentID, false
	}
	if m.Ignored && s.QualifiedIncident == m.IncidentID {
		s.IgnoreOutage = m.IncidentID
	}
}

func (m Monitor) outageQualified() bool {
	return m.Notifications == nil || m.Notifications.QualifiedIncident == m.IncidentID && m.IncidentID != ""
}

func (m Monitor) awaitingOutsideConfirmation() bool {
	return m.Notifications != nil && m.Recovering && m.Notifications.MaintenanceOutage
}

func validateNotificationState(m Monitor, version int) error {
	s := m.Notifications
	if s == nil {
		return nil
	}
	if !actionStorageFormat(version) || m.ActionVersion != ActionFormatVersion || s.OutsideFailures < 0 || s.OutsideFailures > max(1, m.Policy.Unhealthy) {
		return errors.New("invalid notification assessment")
	}
	for _, id := range []string{s.QualifiedIncident, s.IgnoreOutage} {
		if id != "" && !catalogIdentifier(id, 256) {
			return errors.New("invalid notification assessment identity")
		}
	}
	if !m.Ignored && s.IgnoreOutage != "" {
		return errors.New("inactive Ignore retains an outage")
	}
	validSlots := make(map[string]bool)
	for color, count := range m.Policy.Endpoints {
		for endpoint := range count {
			validSlots[deliverySlot(color, notificationEndpointIdentity(m, color, endpoint))] = true
		}
	}
	for slot, d := range s.Deliveries {
		if !validSlots[slot] || !catalogIdentifier(slot, 512) || !catalogIdentifier(d.Episode, 256) || d.Episode != m.NotificationEpisode || d.Condition != "" && !catalogIdentifier(d.Condition, 256) ||
			!d.Started && !d.StartedAt.IsZero() || !d.StartedAt.IsZero() && (d.StartedAt.Year() < 1 || d.StartedAt.Year() > 9999) || !d.Started && !d.Terminal {
			return errors.New("invalid notification exposure")
		}
	}
	for id, alert := range s.ReviewAlerts {
		if id != alert.ID {
			return errors.New("invalid recovery review alert identity")
		}
		if err := m.validateReviewAlert(alert); err != nil {
			return err
		}

	}
	return nil
}
