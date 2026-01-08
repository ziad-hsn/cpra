package drivers

import (
	"context"
)

// SlackDriver sends notifications to Slack via webhooks.
//
// TODO: Implement actual Slack integration using incoming webhooks or Slack API.
// The implementation should:
// - POST JSON payload to configured webhook URL
// - Support message formatting with blocks/attachments
// - Handle rate limiting and retries
type SlackDriver struct {
	WebhookURL string
}

// Name returns the driver identifier.
func (d *SlackDriver) Name() string {
	return "slack"
}

// Send delivers the notification to Slack.
// Currently a placeholder implementation that succeeds without action.
func (d *SlackDriver) Send(ctx context.Context, notification *Notification) error {
	// TODO: Implement Slack webhook or API integration
	// Example implementation:
	// payload := map[string]interface{}{
	//     "text": notification.Message,
	//     "attachments": []map[string]interface{}{
	//         {
	//             "color": colorToHex(notification.Color),
	//             "fields": []map[string]string{
	//                 {"title": "Monitor", "value": notification.Monitor, "short": true},
	//                 {"title": "Status", "value": notification.Status, "short": true},
	//             },
	//         },
	//     },
	// }
	// return httpPost(ctx, d.WebhookURL, payload)
	return nil
}
