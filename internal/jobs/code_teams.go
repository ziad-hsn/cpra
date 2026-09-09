//go:build teams

package jobs

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CodeTeamsJob delivers an alert to Microsoft Teams via a Workflow/Power
// Automate incoming webhook (Adaptive Card).
type CodeTeamsJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	WebhookURL  string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newCodeTeamsJob(cfg *schema.CodeNotificationTeams, monitor, color, message string, entity ecs.Entity) (Job, error) {
	return &CodeTeamsJob{
		ID:         uuid.New(),
		Entity:     entity,
		Monitor:    monitor,
		Color:      color,
		Message:    message,
		WebhookURL: cfg.WebhookURL,
	}, nil
}

func (c *CodeTeamsJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "teams", "color": c.Color}
	data := map[string]interface{}{"type": "message", "attachments": []interface{}{map[string]interface{}{
		"contentType": "application/vnd.microsoft.card.adaptive", "content": map[string]interface{}{
			"type": "AdaptiveCard", "version": "1.2", "body": []interface{}{map[string]interface{}{"type": "TextBlock", "text": c.Message, "wrap": true}}}}}}
	if err := sendJSON(ctx, http.MethodPost, c.WebhookURL, nil, data); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}

	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeTeamsJob) Copy() Job                  { job := *c; return &job }
func (c *CodeTeamsJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeTeamsJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeTeamsJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeTeamsJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeTeamsJob) IsNil() bool                { return c == nil }
