package persistence

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// ReviewAlert is a notification condition, not a provider outcome or permission
// to repeat an intervention. Opening and closure have independent deliveries.
type reviewAlertOwner struct {
	CatalogUID      string `json:"catalog_uid,omitempty"`
	FileIncarnation string `json:"file_incarnation,omitempty"`
}
type ReviewAlert struct {
	Owner                      *reviewAlertOwner `json:"owner,omitempty"`
	ID                         string            `json:"id"`
	ActionID                   string            `json:"action_id"`
	IncidentID                 string            `json:"incident_id"`
	Revision                   string            `json:"revision"`
	EndpointID                 string            `json:"endpoint_id"`
	Endpoint                   int               `json:"endpoint"`
	OpenedAt                   time.Time         `json:"opened_at"`
	RecoveredAt                time.Time         `json:"recovered_at,omitzero"`
	ClosedAt                   time.Time         `json:"closed_at,omitzero"`
	CloseReason                string            `json:"close_reason,omitempty"`
	Health                     string            `json:"health"`
	OpeningDelivery            string            `json:"opening_delivery,omitempty"`
	ClosingDelivery            string            `json:"closing_delivery,omitempty"`
	OpeningStarted             bool              `json:"opening_started,omitempty"`
	ClosingStarted             bool              `json:"closing_started,omitempty"`
	ClosureSuppressed          bool              `json:"closure_suppressed,omitempty"`
	ClosureBlocked             string            `json:"closure_blocked,omitempty"`
	ProviderCorrelationVersion int               `json:"provider_correlation_version"`
}

// ReviewNotice pins the meaning of one blue delivery. A closure keeps the
// opening correlation and destination; it never asserts an intervention result.
type ReviewNotice struct {
	AlertID  string `json:"alert_id"`
	ActionID string `json:"action_id"`
	Phase    string `json:"phase"`
	Reason   string `json:"reason,omitempty"`
	Health   string `json:"health"`
}

// ActionNoticeRevision fences the exact prepared message at the committed
// start boundary. Changing unstarted closure wording cannot authorize a stale
// copy already waiting in a local queue or external worker assignment.
func ActionNoticeRevision(a Action) string {
	if a.Resolution != nil {
		value, _ := json.Marshal(a.Resolution)
		return identity("notification-resolution/v1/" + string(value))
	}
	if a.ReviewNotice == nil {
		return ""
	}
	n := a.ReviewNotice
	value, _ := json.Marshal(struct{ AlertID, ActionID, Phase, Reason, Health string }{n.AlertID, n.ActionID, n.Phase, n.Reason, n.Health})
	return identity("notification-notice/v1/" + string(value))
}

func (m Monitor) observedHealth() string {
	if m.LastCheck.IsZero() {
		return "unknown"
	}
	if m.LastOutcome != "success" || m.Recovering || m.Incident || m.Warning || m.VerifyRemaining > 0 {
		return "unhealthy"
	}
	return "healthy"
}

// notificationHealth describes current availability without rewriting the last
// target observation. Older envelopes retain their original notice semantics.
func (m Monitor) notificationHealth() string {
	if m.notificationConditionsEnabled() && m.ConfigurationProblem.Code != "" {
		return "unknown"
	}
	return m.observedHealth()
}

func (m Monitor) notificationsSuppressed(at time.Time) bool {
	return m.Ignored || m.pendingAutomaticIgnore(at) || m.awaitingOutsideConfirmation() || !m.Policy.Enabled || m.snoozed(at) ||
		m.Dismissed && m.notificationIncident() != "" || m.NotificationMaintenance || m.Policy.InMaintenance(at)
}

func (m *Monitor) assessReviewAlerts(at time.Time, emit func(string, *Action)) {
	if m.Notifications == nil || m.ActionProjection != nil && !m.reviewAssessment {
		return
	}
	s := m.Notifications
	// Unknown interventions from earlier outages still block conflicting work.
	// Each new qualifying outage receives its own review-alert correlation.
	if m.Recovering && m.outageQualified() {
		for _, id := range sortedActions(m.Actions) {
			a := m.Actions[id]
			if !m.OwnsAction(a) || a.Kind != "intervention" || !a.Held() {
				continue
			}
			for endpoint := range m.Policy.Endpoints["blue"] {
				endpointID := notificationEndpointIdentity(*m, "blue", endpoint)
				originalKey := identity("review-alert/" + a.ID + "/" + m.IncidentID + "/" + endpointID)
				// Namespace admission cannot reopen a condition already committed
				// by an older writer with an established provider correlation.
				if old, exists := s.ReviewAlerts[originalKey]; exists && old.ActionID == a.ID && old.IncidentID == m.IncidentID && old.EndpointID == endpointID {
					continue
				}
				key := m.namespacedIdentity(originalKey)
				if _, exists := s.ReviewAlerts[key]; exists {
					continue
				}
				if s.ReviewAlerts == nil {
					s.ReviewAlerts = make(map[string]ReviewAlert)
				}
				s.ReviewAlerts[key] = ReviewAlert{ID: key, ActionID: a.ID, IncidentID: m.IncidentID, Revision: m.Revision,
					EndpointID: endpointID, Endpoint: endpoint, OpenedAt: at, Health: m.notificationHealth(), ProviderCorrelationVersion: 1}
				emit("review_alert_opened", &Action{ID: a.ID, Kind: "intervention", CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: a.Revision, IncidentID: m.IncidentID,
					ReviewNotice: &ReviewNotice{AlertID: key, ActionID: a.ID, Phase: "open", Health: m.notificationHealth()}})
			}
		}
	}
	ids := make([]string, 0, len(s.ReviewAlerts))
	for id := range s.ReviewAlerts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s.ReviewAlerts[id] = m.assessReviewAlert(s.ReviewAlerts[id], at, emit)
	}
}

func (m *Monitor) assessReviewAlert(alert ReviewAlert, at time.Time, emit func(string, *Action)) ReviewAlert {

	a, exists := m.Actions[alert.ActionID]
	reviewed := exists && a.Review != nil && !a.Held() && a.Review.Resolution != "inconclusive" && !a.Review.Conflict
	if exists && !m.OwnsAction(a) {
		alert.ClosureBlocked = "incarnation_changed"
		if reviewed && alert.ClosedAt.IsZero() {
			alert.ClosedAt, alert.CloseReason, alert.Health = at, "action_reviewed", "unknown"
			emit("review_alert_closed", &Action{ID: a.ID, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: alert.Revision, IncidentID: alert.IncidentID, Kind: "intervention",
				ReviewNotice: &ReviewNotice{AlertID: alert.ID, ActionID: a.ID, Phase: "close", Reason: "action_reviewed", Health: "unknown"}})
		}
		return alert
	}
	recovered := m.IncidentID == alert.IncidentID && !m.IncidentClosedAt.IsZero() && m.observedHealth() == "healthy"
	if recovered && alert.RecoveredAt.IsZero() {
		alert.RecoveredAt = m.IncidentClosedAt
	}
	if !alert.ClosingStarted && alert.CloseReason == "action_reviewed" && !reviewed && alert.RecoveredAt.IsZero() {
		if closing, ok := m.reviewDelivery(alert.ClosingDelivery); ok && closing.State == Queued {
			m.cancelNotification(closing, "review_condition_changed", at, emit)
		}
		alert.ClosedAt, alert.CloseReason, alert.ClosingDelivery = time.Time{}, "", ""
	}
	if alert.ClosedAt.IsZero() && (reviewed || recovered) {
		alert.ClosedAt, alert.Health = at, m.notificationHealth()
		alert.CloseReason = "health_recovered"
		if reviewed {
			alert.CloseReason = "action_reviewed"
		}
		if opening, ok := m.reviewDelivery(alert.OpeningDelivery); ok && opening.State == Queued {
			m.cancelNotification(opening, "review_condition_closed", at, emit)
		}
		emit("review_alert_closed", &Action{ID: alert.ActionID, Kind: "intervention", CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: alert.Revision, IncidentID: alert.IncidentID,
			ReviewNotice: &ReviewNotice{AlertID: alert.ID, ActionID: alert.ActionID, Phase: "close", Reason: alert.CloseReason, Health: alert.Health}})
	}
	phase := "open"
	if !alert.ClosedAt.IsZero() {
		phase = "close"
		closing, queued := m.reviewDelivery(alert.ClosingDelivery)
		queued = queued && closing.State == Queued
		// A queued retry follows a proven no-effect rejection. Its original
		// deadline and delivery remain fixed, while current-health wording can
		// advance before the next start. Started/unknown attempts stay frozen.
		refresh := !alert.ClosingStarted || m.notificationConditionsEnabled() && queued && closing.Attempt > 1
		if refresh {
			alert.Health = m.notificationHealth()
			if !reviewed && !alert.RecoveredAt.IsZero() {
				alert.CloseReason = "health_recovered"
			}
			// Revise only an unstarted payload. Its delivery identity and original
			// deadline remain unchanged, including when observations advance.
			if queued {
				closing = closing.Clone()
				closing.ReviewNotice.Health, closing.ReviewNotice.Reason = alert.Health, alert.CloseReason
				m.Actions[closing.ID] = closing
			}
		}
		if !alert.OpeningStarted {
			return alert // An unsent opening is not remote exposure.
		}
		alert.ClosureSuppressed = m.notificationsSuppressed(at)
	}
	if alert.Revision != m.Revision || alert.Endpoint >= m.Policy.Endpoints["blue"] || notificationEndpointIdentity(*m, "blue", alert.Endpoint) != alert.EndpointID {
		alert.ClosureBlocked = "configuration_changed"
	} else if !m.notificationsSuppressed(at) {
		alert.ClosureBlocked = ""
		m.queueReviewNotice(&alert, phase, at, emit)
	}
	return alert
}

func (m *Monitor) queueReviewNotice(alert *ReviewAlert, phase string, at time.Time, emit func(string, *Action)) {
	var originalDeadline time.Time
	id, started := alert.OpeningDelivery, alert.OpeningStarted
	if phase == "close" {
		id, started = alert.ClosingDelivery, alert.ClosingStarted
	}
	if started {
		return // The controller owns bounded retries; unknown effects stay held.
	}
	if old, ok := m.reviewDelivery(id); ok {
		if old.State != Cancelled {
			return
		}
		switch old.Outcome {
		case "maintenance", "monitor_ignored", "snooze", "dismiss", "notification_superseded":
		case "worker_notification_changed":
			// No start grant was issued for the obsolete message. A replacement
			// can use only the remaining authorization, never a fresh budget.
			if old.ExecutionBudget == 0 || !old.Deadline.IsZero() {
				if old.Deadline.IsZero() || !at.Before(old.Deadline) {
					return
				}
				originalDeadline = old.Deadline
			}
		default:
			return // Expiry/rejection does not create a fresh execution budget.
		}
	}
	m.Sequence++
	id = m.namespacedIdentity(identity(fmt.Sprintf("review-delivery/%s/%s/%s/%d", m.incarnationIdentity(), alert.ID, phase, m.Sequence)))
	notice := &ReviewNotice{AlertID: alert.ID, ActionID: alert.ActionID, Phase: phase, Health: alert.Health}
	if phase == "close" {
		notice.Reason = alert.CloseReason
	}
	a := Action{ID: id, DeliveryID: id, CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation, Revision: alert.Revision,
		IncidentID: alert.IncidentID, Kind: "code", Color: "blue", Endpoint: alert.Endpoint, EndpointID: alert.EndpointID,
		NotificationEpisode: alert.ID, NotificationCorrelation: alert.ID, ProviderCorrelationVersion: alert.ProviderCorrelationVersion,
		Attempt: 1, State: Queued, NotBefore: at, QueuedAt: at, Deadline: at.Add(m.Policy.notificationTimeout("blue", alert.Endpoint)), ReviewNotice: notice}
	if budget, ok := m.reviewTimeouts[alert.Endpoint]; ok {
		a.Deadline = at.Add(budget)
	}
	if m.notificationConditionsEnabled() {
		a.ExecutionBudget, a.Deadline = a.Deadline.Sub(at), time.Time{}
	}
	if alert.Owner != nil {
		a.CatalogUID, a.FileIncarnation = alert.Owner.CatalogUID, alert.Owner.FileIncarnation
	}
	if !originalDeadline.IsZero() {
		a.Deadline, a.ExecutionBudget = originalDeadline, 0
	}
	if phase == "open" {
		alert.OpeningDelivery = id
	} else {
		alert.ClosingDelivery = id
	}
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[id] = a
	emit("action_queued", &a)
}

// assessReviewAlertEvents is also used by committed worker finalization, where
// an unknown action can arise without a health result in the same command.
func (m *Monitor) assessReviewAlertEvents(at time.Time) []Event {
	if !actionStorageFormat(m.ActionVersion) || m.Removed {
		return nil
	}
	var events []Event
	m.assessReviewAlerts(at, func(kind string, a *Action) {
		e := Event{MonitorID: m.ID, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation,
			Revision: a.Revision, At: at, Type: kind, IncidentID: a.IncidentID,
			ActionID: a.ID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome,
			DeliveryID: a.DeliveryID, NotificationEpisode: a.NotificationEpisode, EndpointID: a.EndpointID,
			ReviewNotice: a.ReviewNotice}
		events = append(events, e.Clone())
	})
	return events
}

func (m Monitor) reviewDelivery(id string) (Action, bool) {
	if id == "" {
		return Action{}, false
	}
	for attempt := 3; attempt >= 1; attempt-- {
		key := id
		if attempt > 1 {
			key = identity(fmt.Sprintf("%s/attempt/%d", id, attempt))
		}
		if a, ok := m.Actions[key]; ok {
			return a, true
		}
	}
	return Action{}, false
}

func (m *Monitor) recordReviewNoticeStart(a Action) {
	if m.Notifications == nil || a.ReviewNotice == nil {
		return
	}
	alert, ok := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]
	if !ok {
		return
	}
	if a.ReviewNotice.Phase == "open" {
		alert.OpeningStarted = true
	} else {
		alert.ClosingStarted = true
	}
	m.Notifications.ReviewAlerts[alert.ID] = alert
}

func (m Monitor) reviewNoticeEligible(a Action) bool {
	if m.Notifications == nil || a.ReviewNotice == nil {
		return false
	}
	alert, ok := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]
	if !ok || a.ReviewNotice.ActionID != alert.ActionID || a.EndpointID != alert.EndpointID || a.Revision != alert.Revision {
		return false
	}
	if a.ReviewNotice.Phase == "open" {
		underlying, exists := m.Actions[alert.ActionID]
		return exists && underlying.Held() && m.OwnsAction(underlying) && alert.ClosedAt.IsZero() && alert.IncidentID == m.IncidentID && m.Recovering && m.outageQualified()
	}
	if alert.ClosedAt.IsZero() || !alert.OpeningStarted || a.ReviewNotice.Health != m.notificationHealth() || a.ReviewNotice.Reason != alert.CloseReason {
		return false
	}
	if a.ReviewNotice.Reason == "health_recovered" {
		return !alert.RecoveredAt.IsZero()
	}
	action, exists := m.Actions[alert.ActionID]
	return exists && action.Review != nil && !action.Held() && action.Review.Resolution != "inconclusive" && !action.Review.Conflict
}

func validateReviewNotice(a Action, version int) error {
	n := a.ReviewNotice
	if n == nil {
		return nil
	}
	if !actionStorageFormat(version) || a.Kind != "code" || a.Color != "blue" || !catalogIdentifier(n.AlertID, 256) || !catalogIdentifier(n.ActionID, 256) ||
		a.NotificationCorrelation != n.AlertID || a.NotificationEpisode != n.AlertID || (n.Phase != "open" && n.Phase != "close") ||
		(n.Health != "healthy" && n.Health != "unhealthy" && n.Health != "unknown") ||
		(n.Phase == "open" && n.Reason != "" || n.Phase == "close" && n.Reason != "health_recovered" && n.Reason != "action_reviewed") {
		return errors.New("invalid recovery review notification")
	}
	return nil
}

func (m Monitor) validateReviewAlert(alert ReviewAlert) error {
	if err := validateReviewAlertFields(alert); err != nil {
		return err
	}
	action, exists := m.Actions[alert.ActionID]
	if !exists || action.Kind != "intervention" || action.State != Unknown {
		return errors.New("review alert has no retained uncertain intervention")
	}
	owner := reviewAlertOwner{CatalogUID: action.CatalogUID, FileIncarnation: action.FileIncarnation}
	if alert.Owner != nil {
		owner = *alert.Owner
	}
	for _, phase := range []string{"open", "close"} {
		id, started := alert.OpeningDelivery, alert.OpeningStarted
		if phase == "close" {
			id, started = alert.ClosingDelivery, alert.ClosingStarted
		}
		if id == "" {
			if started {
				return errors.New("review alert exposure has no delivery")
			}
			continue
		}
		delivery, ok := m.reviewDelivery(id)
		if !ok || delivery.ReviewNotice == nil || delivery.ReviewNotice.AlertID != alert.ID || delivery.ReviewNotice.ActionID != action.ID || delivery.ReviewNotice.Phase != phase ||
			delivery.DeliveryID != id || delivery.EndpointID != alert.EndpointID || delivery.Endpoint != alert.Endpoint || delivery.Revision != alert.Revision ||
			delivery.IncidentID != alert.IncidentID || delivery.NotificationEpisode != alert.ID || delivery.NotificationCorrelation != alert.ID ||
			delivery.ProviderCorrelationVersion != alert.ProviderCorrelationVersion || delivery.CatalogUID != owner.CatalogUID || delivery.FileIncarnation != owner.FileIncarnation ||
			started != (!delivery.StartedAt.IsZero() || delivery.Attempt > 1) {
			return errors.New("review alert delivery identity mismatch")
		}
	}
	return nil
}

func validateReviewAlertFields(alert ReviewAlert) error {
	if !catalogIdentifier(alert.ID, 256) || !catalogIdentifier(alert.ActionID, 256) || !catalogIdentifier(alert.IncidentID, 256) || alert.Revision == "" || !catalogIdentifier(alert.EndpointID, 256) || alert.Endpoint < 0 || alert.Endpoint >= math.MaxInt32 || alert.OpenedAt.IsZero() || alert.OpenedAt.Year() < 1 || alert.OpenedAt.Year() > 9999 ||
		(!alert.ClosedAt.IsZero() && (alert.ClosedAt.Before(alert.OpenedAt) || alert.ClosedAt.Year() > 9999)) || alert.CloseReason != "" && alert.CloseReason != "health_recovered" && alert.CloseReason != "action_reviewed" ||
		(!alert.RecoveredAt.IsZero() && (alert.RecoveredAt.Before(alert.OpenedAt) || alert.RecoveredAt.Year() > 9999)) || alert.ProviderCorrelationVersion != 1 || (alert.ClosedAt.IsZero() != (alert.CloseReason == "")) || alert.ClosingStarted && !alert.OpeningStarted ||
		(alert.Health != "healthy" && alert.Health != "unhealthy" && alert.Health != "unknown") || alert.ClosureBlocked != "" && alert.ClosureBlocked != "configuration_changed" && alert.ClosureBlocked != "incarnation_changed" {
		return errors.New("invalid recovery review alert")
	}
	for _, id := range []string{alert.OpeningDelivery, alert.ClosingDelivery} {
		if id != "" && !catalogIdentifier(id, 256) {
			return errors.New("invalid recovery review alert delivery")
		}
	}
	if o := alert.Owner; o != nil && (o.CatalogUID != "" && !catalogIdentifier(o.CatalogUID, 256) || o.FileIncarnation != "" && !catalogIdentifier(o.FileIncarnation, 256)) {
		return errors.New("invalid recovery review alert owner")
	}
	return nil
}
