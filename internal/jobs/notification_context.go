package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// notificationIdentity contains committed identifiers and event time. Retries
// retain these values; no provider secret or resolved target is included.
type notificationIdentity struct {
	MonitorID     string    `json:"monitor_id,omitempty"`
	IncarnationID string    `json:"incarnation_id,omitempty"`
	IncidentID    string    `json:"incident_id,omitempty"`
	EpisodeID     string    `json:"episode_id,omitempty"`
	ActionID      string    `json:"action_id,omitempty"`
	DeliveryID    string    `json:"delivery_id,omitempty"`
	EndpointID    string    `json:"endpoint_id,omitempty"`
	At            time.Time `json:"at,omitzero"`
}

type notificationEnvelope struct {
	notificationIdentity
	Monitor string `json:"monitor"`
	Color   string `json:"color"`
	Message string `json:"message"`
}

func bindFailureDiagnostic(job Job, dispatch *Dispatch) {
	if dispatch.Kind != "code" || dispatch.FailureCode == "" {
		return
	}
	for range 8 {
		if target, ok := job.(interface{ SetFailureDiagnostic(string, string) }); ok {
			target.SetFailureDiagnostic(dispatch.FailureCode, dispatch.FailureMessage)
			return
		}
		wrapper, ok := job.(interface{ Unwrap() Job })
		if !ok {
			return
		}
		job = wrapper.Unwrap()
	}
}

func bindNotificationContext(job Job, dispatch *Dispatch) {
	if dispatch.Kind != "code" {
		return
	}
	incarnation := dispatch.CatalogUID
	if incarnation == "" {
		incarnation = dispatch.FileIncarnation
	}
	for wrapped, depth := job, 0; depth < 8; depth++ {
		if target, ok := wrapped.(interface{ SetNotificationIdentity(notificationIdentity) }); ok {
			target.SetNotificationIdentity(notificationIdentity{
				MonitorID: dispatch.MonitorID, IncarnationID: incarnation,
				IncidentID: dispatch.IncidentID, EpisodeID: dispatch.NotificationCorrelation,
				ActionID: dispatch.ActionID, DeliveryID: dispatch.DeliveryID,
				EndpointID: dispatch.EndpointID, At: dispatch.NotificationAt.UTC(),
			})
			break
		}
		wrapper, ok := wrapped.(interface{ Unwrap() Job })
		if !ok {
			break
		}
		wrapped = wrapper.Unwrap()
	}
	for wrapped, depth := job, 0; depth < 8; depth++ {
		if target, ok := wrapped.(*CodeLogJob); ok {
			target.Log = dispatch.NotificationLog
			break
		}
		wrapper, ok := wrapped.(interface{ Unwrap() Job })
		if !ok {
			break
		}
		wrapped = wrapper.Unwrap()
	}
	if dispatch.ControlChanged {
		wrapped := job
		for range 8 {
			if target, ok := wrapped.(interface{ SetControlNotification(bool, time.Time) }); ok {
				target.SetControlNotification(dispatch.ControlEnabled, dispatch.ControlAt)
				break
			}
			wrapper, ok := wrapped.(interface{ Unwrap() Job })
			if !ok {
				break
			}
			wrapped = wrapper.Unwrap()
		}
	}
	incident := dispatch.NotificationCorrelation
	if incident == "" {
		incident = dispatch.IncidentID
	}
	if incident == "" || dispatch.EndpointID == "" {
		return
	}
	// Delivery identity includes every configured option, which is necessary
	// for start fencing. A provider incident instead spans colors: presentation
	// changes must not make its recovery target a different remote incident.
	correlation := func(provider string, destination ...string) string {
		if dispatch.ProviderCorrelationVersion == 0 {
			digest := sha256.Sum256([]byte(strings.Join([]string{dispatch.MonitorID, dispatch.CatalogUID, incident, dispatch.EndpointID}, "\x00")))
			return "cpra:" + hex.EncodeToString(digest[:])
		}
		parts := append([]string{provider, dispatch.MonitorID, incarnation, incident}, destination...)
		digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return "cpra:" + hex.EncodeToString(digest[:])
	}
	for range 8 {
		switch target := job.(type) {
		case *CodePagerDutyJob:
			url := target.URL
			if url == "" {
				url = "https://events.pagerduty.com/v2/enqueue"
			}
			target.CorrelationKey = correlation("pagerduty", url, target.RoutingKey)
			return
		case *CodeOpsgenieJob:
			url, err := notificationURL(target.URL, "https://api.opsgenie.com/v2/alerts")
			if err == nil {
				target.CorrelationKey = correlation("opsgenie", url, target.APIKey)
			}
			return
		case *CodeVictorOpsJob:
			if dispatch.ProviderCorrelationVersion == 0 {
				return
			} // Earlier deliveries used the configured EntityID.
			url, err := victorOpsURL(target.URL, target.RestEndpointKey, target.RoutingKey)
			if err == nil {
				target.CorrelationKey = correlation("victorops", url, target.EntityID)
			}
			return
		case interface{ Unwrap() Job }:
			job = target.Unwrap()
		default:
			return
		}
	}
}
