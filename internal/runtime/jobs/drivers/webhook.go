package drivers

import (
	"context"
)

// WebhookDriver sends notifications to a generic HTTP webhook endpoint.
//
// TODO: Implement actual webhook POST with configurable payload format.
// The implementation should:
// - POST JSON payload to configured URL
// - Support custom headers and authentication
// - Handle retries and timeouts
type WebhookDriver struct {
	URL     string
	Method  string            // POST, PUT, etc.
	Headers map[string]string // Custom headers
}

// Name returns the driver identifier.
func (d *WebhookDriver) Name() string {
	return "webhook"
}

// Send delivers the notification to the webhook endpoint.
// Currently a placeholder implementation that succeeds without action.
func (d *WebhookDriver) Send(ctx context.Context, notification *Notification) error {
	// TODO: Implement HTTP POST to webhook URL with JSON payload
	// Example implementation:
	// payload := map[string]interface{}{
	//     "monitor": notification.Monitor,
	//     "color": notification.Color,
	//     "status": notification.Status,
	//     "severity": notification.Severity,
	//     "summary": notification.Summary,
	//     "action": notification.Action,
	//     "next_steps": notification.NextSteps,
	//     "message": notification.Message,
	// }
	// jsonData, _ := json.Marshal(payload)
	// req, _ := http.NewRequestWithContext(ctx, d.Method, d.URL, bytes.NewReader(jsonData))
	// for k, v := range d.Headers {
	//     req.Header.Set(k, v)
	// }
	// resp, err := http.DefaultClient.Do(req)
	// if err != nil { return err }
	// defer resp.Body.Close()
	// if resp.StatusCode >= 400 { return fmt.Errorf("webhook returned %d", resp.StatusCode) }
	// return nil
	return nil
}
