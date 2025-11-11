package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// CodeWebhookJob sends alert notifications to a webhook endpoint.
// This is currently a placeholder implementation.
//
// TODO: Implement actual webhook POST with configurable payload format.
type CodeWebhookJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
}

// Execute sends the alert to the webhook endpoint.
// Currently a mock implementation that succeeds without action.
func (c *CodeWebhookJob) Execute(_ context.Context) Result {
	// TODO: Implement HTTP POST to webhook URL with JSON payload
	return Result{
		Ent:     c.Entity,
		Err:     nil,
		Payload: map[string]interface{}{"type": "code", "driver": "webhook", "color": c.Color},
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodeWebhookJob) Copy() Job { job := *c; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (c *CodeWebhookJob) GetEnqueueTime() time.Time { return c.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (c *CodeWebhookJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (c *CodeWebhookJob) GetStartTime() time.Time { return c.StartTime }

// SetStartTime sets when the job started executing.
func (c *CodeWebhookJob) SetStartTime(t time.Time) { c.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (c *CodeWebhookJob) IsNil() bool { return c == nil }
