package jobs

import (
	"context"

	"cpra/internal/runtime/jobs/drivers"
)

// CodeNotificationJob is a unified job type for sending code alert notifications
// to various backends (Slack, PagerDuty, Email, Webhook).
//
// This replaces the duplicated CodeSlackJob, CodePagerDutyJob, CodeEmailJob,
// and CodeWebhookJob types with a single type that uses the Strategy pattern
// via the NotificationDriver interface.
//
// The driver field determines which notification backend is used, allowing
// new notification channels to be added without modifying this job type.
type CodeNotificationJob struct {
	BaseJob

	// Monitor is the name of the monitor that triggered the alert.
	Monitor string

	// Message is the pre-formatted human-readable alert message.
	Message string

	// Color indicates the alert severity (red, yellow, green, cyan, gray).
	Color string

	// DriverName identifies the notification driver (slack, pagerduty, email, webhook).
	// This is used for routing results and pool management.
	DriverName string

	// Driver is the notification backend implementation.
	// If nil, the job succeeds without sending (for testing/placeholder mode).
	Driver drivers.NotificationDriver

	// Additional fields for rich notifications
	Status    string
	Severity  string
	Summary   string
	Action    string
	NextSteps string
}

// Execute sends the alert through the configured notification driver.
// If no driver is configured, it returns success without action (placeholder mode).
func (c *CodeNotificationJob) Execute(ctx context.Context) Result {
	payload := map[string]interface{}{
		"type":   "code",
		"driver": c.DriverName,
		"color":  c.Color,
	}

	// If driver is configured, attempt to send the notification
	if c.Driver != nil {
		notification := &drivers.Notification{
			Monitor:   c.Monitor,
			Color:     c.Color,
			Status:    c.Status,
			Severity:  c.Severity,
			Summary:   c.Summary,
			Action:    c.Action,
			NextSteps: c.NextSteps,
			Message:   c.Message,
		}

		if err := c.Driver.Send(ctx, notification); err != nil {
			return Result{
				Ent:     c.Entity,
				Err:     err,
				Payload: payload,
			}
		}
	}

	return Result{
		Ent:     c.Entity,
		Err:     nil,
		Payload: payload,
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodeNotificationJob) Copy() Job {
	job := *c
	return &job
}

// Reset clears all fields for pool reuse.
func (c *CodeNotificationJob) Reset() {
	c.BaseJob.Reset()
	c.Monitor = ""
	c.Message = ""
	c.Color = ""
	c.DriverName = ""
	c.Driver = nil
	c.Status = ""
	c.Severity = ""
	c.Summary = ""
	c.Action = ""
	c.NextSteps = ""
}

// IsNil checks if the job is nil.
func (c *CodeNotificationJob) IsNil() bool {
	return c == nil
}
