package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// CodePagerDutyJob sends alert notifications to PagerDuty.
// This is currently a placeholder implementation.
//
// TODO: Implement actual PagerDuty integration using their Events API v2.
type CodePagerDutyJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
}

// Execute sends the alert to PagerDuty.
// Currently a mock implementation that succeeds without action.
func (c *CodePagerDutyJob) Execute(_ context.Context) Result {
	// TODO: Implement PagerDuty Events API v2 integration
	return Result{
		Ent:     c.Entity,
		Err:     nil,
		Payload: map[string]interface{}{"type": "code", "driver": "pagerduty", "color": c.Color},
	}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodePagerDutyJob) Copy() Job { job := *c; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (c *CodePagerDutyJob) GetEnqueueTime() time.Time { return c.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (c *CodePagerDutyJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (c *CodePagerDutyJob) GetStartTime() time.Time { return c.StartTime }

// SetStartTime sets when the job started executing.
func (c *CodePagerDutyJob) SetStartTime(t time.Time) { c.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (c *CodePagerDutyJob) IsNil() bool { return c == nil }
