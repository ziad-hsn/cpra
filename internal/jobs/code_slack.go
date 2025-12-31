package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// CodeSlackJob sends alert notifications to Slack.
// This is currently a placeholder implementation.
//
// TODO: Implement actual Slack integration using incoming webhooks or Slack API.
type CodeSlackJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
}

// Execute sends the alert to Slack.
// Currently a mock implementation that succeeds without action.
func (c *CodeSlackJob) Execute(_ context.Context) Result {
	// TODO: Implement Slack webhook or API integration
	return Result{
		Ent:     c.Entity,
		Err:     nil,
		Payload: map[string]interface{}{"type": "code", "driver": "slack", "color": c.Color},
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodeSlackJob) Copy() Job { job := *c; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (c *CodeSlackJob) GetEnqueueTime() time.Time { return c.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (c *CodeSlackJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (c *CodeSlackJob) GetStartTime() time.Time { return c.StartTime }

// SetStartTime sets when the job started executing.
func (c *CodeSlackJob) SetStartTime(t time.Time) { c.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (c *CodeSlackJob) IsNil() bool { return c == nil }
