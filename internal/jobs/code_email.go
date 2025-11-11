package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// CodeEmailJob sends alert notifications via email.
// This is currently a placeholder implementation.
//
// TODO: Implement actual email sending using SMTP or email service API.
type CodeEmailJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
}

// Execute sends the alert via email.
// Currently a mock implementation that succeeds without action.
func (c *CodeEmailJob) Execute(_ context.Context) Result {
	// TODO: Implement SMTP or email API integration
	return Result{
		Ent:     c.Entity,
		Err:     nil,
		Payload: map[string]interface{}{"type": "code", "driver": "email", "color": c.Color},
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodeEmailJob) Copy() Job { job := *c; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (c *CodeEmailJob) GetEnqueueTime() time.Time { return c.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (c *CodeEmailJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (c *CodeEmailJob) GetStartTime() time.Time { return c.StartTime }

// SetStartTime sets when the job started executing.
func (c *CodeEmailJob) SetStartTime(t time.Time) { c.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (c *CodeEmailJob) IsNil() bool { return c == nil }
