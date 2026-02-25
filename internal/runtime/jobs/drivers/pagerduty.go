package drivers

import (
	"context"
)

// PagerDutyDriver sends notifications to PagerDuty via Events API v2.
//
// TODO: Implement actual PagerDuty integration using their Events API v2.
// The implementation should:
// - POST events to events.pagerduty.com/v2/enqueue
// - Support trigger, acknowledge, and resolve actions
// - Include routing key and proper event format
type PagerDutyDriver struct {
	RoutingKey string
	Severity   string // critical, error, warning, info
}

// Name returns the driver identifier.
func (d *PagerDutyDriver) Name() string {
	return "pagerduty"
}

// Send delivers the notification to PagerDuty.
// Currently a placeholder implementation that succeeds without action.
func (d *PagerDutyDriver) Send(ctx context.Context, notification *Notification) error {
	// TODO: Implement PagerDuty Events API v2 integration
	// Example implementation:
	// event := map[string]interface{}{
	//     "routing_key": d.RoutingKey,
	//     "event_action": "trigger",
	//     "payload": map[string]interface{}{
	//         "summary": notification.Summary,
	//         "source": notification.Monitor,
	//         "severity": notification.Severity,
	//         "custom_details": map[string]string{
	//             "color": notification.Color,
	//             "action": notification.Action,
	//             "next_steps": notification.NextSteps,
	//         },
	//     },
	// }
	// return httpPost(ctx, "https://events.pagerduty.com/v2/enqueue", event)
	return nil
}
