package jobs

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// CodeMattermostJob delivers an alert to a Mattermost incoming webhook
// (Slack-compatible payload). Pure stdlib, always compiled.
type CodeMattermostJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	WebhookURL  string
	Channel     string
	Username    string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeMattermostJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "mattermost", "color": c.Color}
	fields := map[string]string{"text": c.Message}
	if c.Channel != "" {
		fields["channel"] = c.Channel
	}
	if c.Username != "" {
		fields["username"] = c.Username
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	resp, err := postNotification(ctx, c.WebhookURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeMattermostJob) Copy() Job                  { job := *c; return &job }
func (c *CodeMattermostJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeMattermostJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeMattermostJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeMattermostJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeMattermostJob) IsNil() bool                { return c == nil }
