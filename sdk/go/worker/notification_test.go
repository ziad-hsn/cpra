//go:build externaljobs

package worker

import (
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestNotificationContextValidationAndHandlerOwnership(t *testing.T) {
	a := assignment("execution", "notification")
	a.Notification = &api.WorkerNotification{Format: 1, Color: "blue", DeliveryID: "delivery", EndpointID: "endpoint", CorrelationID: "review", At: time.Now().UTC(), Review: &api.ReviewNotice{AlertID: "review", ActionID: "uncertain-action", Phase: "close", Reason: "health_recovered", Health: "healthy"}}
	if err := validateAssignment(a, a.ServerID, a.WorkerUID, a.SessionID); err != nil {
		t.Fatal(err)
	}
	copy := cloneAssignment(a)
	copy.Notification.Review.Health = "unhealthy"
	copy.Notification.CorrelationID = "different"
	copy.Parameters[0] = ' '
	if a.Notification.Review.Health != "healthy" || a.Notification.CorrelationID != "review" || a.Parameters[0] != '{' {
		t.Fatal("handler mutation changed protocol-owned assignment")
	}
	if err := validateAssignment(copy, a.ServerID, a.WorkerUID, a.SessionID); err == nil {
		t.Fatal("changed correlation was accepted")
	}
	for _, change := range []func(*api.Assignment){
		func(a *api.Assignment) { a.Notification.Review = nil },
		func(a *api.Assignment) { a.Notification.Review.Reason = "success" },
		func(a *api.Assignment) { a.Notification.Review.Health = "restored" },
		func(a *api.Assignment) { a.Kind = "recovery" },
	} {
		bad := cloneAssignment(a)
		change(&bad)
		if err := validateAssignment(bad, a.ServerID, a.WorkerUID, a.SessionID); err == nil {
			t.Fatal("malformed blue notification accepted")
		}
	}
}

func TestResolutionContextValidationAndHandlerOwnership(t *testing.T) {
	a := assignment("resolution", "notification")
	at := time.Now().UTC()
	a.Notification = &api.WorkerNotification{Format: 2, Color: "green", DeliveryID: "delivery", EndpointID: "endpoint", IncidentID: "earlier-incident", CorrelationID: "original-correlation", At: at,
		Resolution: &api.ResolutionNotice{ConditionID: "original-condition", RecoveredAt: at.Add(-time.Minute), Health: "unhealthy"}}
	if err := validateAssignment(a, a.ServerID, a.WorkerUID, a.SessionID); err != nil {
		t.Fatal(err)
	}
	copy := cloneAssignment(a)
	copy.Notification.Resolution.Health = "healthy"
	copy.Notification.Resolution.ConditionID = "other-condition"
	if a.Notification.Resolution.Health != "unhealthy" || a.Notification.Resolution.ConditionID != "original-condition" {
		t.Fatal("handler mutation changed the original resolution")
	}
	for _, change := range []func(*api.Assignment){
		func(a *api.Assignment) { a.Notification.Format = 1 },
		func(a *api.Assignment) { a.Notification.Color = "red" },
		func(a *api.Assignment) { a.Notification.Resolution.ConditionID = "" },
		func(a *api.Assignment) { a.Notification.Resolution.RecoveredAt = time.Time{} },
		func(a *api.Assignment) { a.Notification.Resolution.Health = "restored" },
		func(a *api.Assignment) { a.Notification.Review = &api.ReviewNotice{} },
		func(a *api.Assignment) { a.Kind = "recovery" },
	} {
		bad := cloneAssignment(a)
		change(&bad)
		if err := validateAssignment(bad, a.ServerID, a.WorkerUID, a.SessionID); err == nil {
			t.Fatal("malformed resolution accepted")
		}
	}
}
