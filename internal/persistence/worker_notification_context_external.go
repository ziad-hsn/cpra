//go:build externaljobs

package persistence

import "time"

// WorkerNotification is a frozen, non-secret projection of the committed
// delivery. It is authenticated with the assignment, never inferred by a worker
// from operator-supplied parameters or current monitor state.
type WorkerNotification struct {
	Resolution    *ResolutionNotice `json:"resolution,omitempty"`
	Format        int               `json:"format"`
	Color         string            `json:"color"`
	DeliveryID    string            `json:"delivery_id"`
	EndpointID    string            `json:"endpoint_id"`
	IncidentID    string            `json:"incident_id,omitempty"`
	CorrelationID string            `json:"correlation_id"`
	At            time.Time         `json:"at"`
	Review        *ReviewNotice     `json:"review,omitempty"`
}

func NotificationForWorker(a Action) *WorkerNotification {
	if a.Kind != "code" {
		return nil
	}
	n := &WorkerNotification{Format: 1, Color: a.Color, DeliveryID: a.DeliveryID, EndpointID: a.EndpointID, IncidentID: a.IncidentID, CorrelationID: a.NotificationCorrelation, At: a.QueuedAt}
	if a.Resolution != nil {
		n.Format = 2
		copy := *a.Resolution
		n.Resolution = &copy
	}
	if a.ReviewNotice != nil {
		copy := *a.ReviewNotice
		n.Review = &copy
	}
	return n
}

func (n *WorkerNotification) clone() *WorkerNotification {
	if n == nil {
		return nil
	}
	copy := *n
	if n.Resolution != nil {
		v := *n.Resolution
		copy.Resolution = &v
	}
	if n.Review != nil {
		review := *n.Review
		copy.Review = &review
	}
	return &copy
}

func (n *WorkerNotification) valid() bool {
	if n == nil {
		return true
	}
	if n.Format != 1 && n.Format != 2 || !catalogIdentifier(n.DeliveryID, 256) || !catalogIdentifier(n.EndpointID, 256) || !catalogIdentifier(n.CorrelationID, 256) ||
		n.IncidentID != "" && !catalogIdentifier(n.IncidentID, 256) || n.At.IsZero() || n.At.Year() < 1 || n.At.Year() > 9999 {
		return false
	}
	if n.Resolution != nil && (n.Format != 2 || n.Color != "green" || n.Review != nil || !catalogIdentifier(n.Resolution.ConditionID, 256) || n.Resolution.RecoveredAt.IsZero() || !validResolutionHealth(n.Resolution.Health)) {
		return false
	}
	switch n.Color {
	case "red", "yellow", "green", "cyan", "gray", "blue":
	default:
		return false
	}
	if n.Review != nil {
		return validateReviewNotice(Action{Kind: "code", Color: n.Color, NotificationCorrelation: n.CorrelationID, NotificationEpisode: n.CorrelationID, ReviewNotice: n.Review}, 45) == nil
	}
	return n.Color != "blue"
}
