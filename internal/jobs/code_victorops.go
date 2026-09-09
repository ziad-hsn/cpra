package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// CodeVictorOpsJob delivers an alert to Splunk On-Call (VictorOps) via the
// generic REST endpoint. Pure stdlib, always compiled.
type CodeVictorOpsJob struct {
	Execution
	EnqueueTime     time.Time
	StartTime       time.Time
	Monitor         string
	Message         string
	Color           string
	RestEndpointKey string
	RoutingKey      string
	MessageType     string
	EntityID        string
	Entity          ecs.Entity
	ID              uuid.UUID
}

func (c *CodeVictorOpsJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "victorops", "color": c.Color}
	messageType := c.MessageType
	if messageType == "" {
		messageType = "CRITICAL"
	}
	url := fmt.Sprintf("https://alert.victorops.com/integrations/generic/20131114/alert/%s/%s", c.RestEndpointKey, c.RoutingKey)
	body, err := json.Marshal(map[string]string{
		"message_type":  messageType,
		"entity_id":     c.EntityID,
		"state_message": c.Message,
	})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	resp, err := postNotification(ctx, url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeVictorOpsJob) Copy() Job                  { job := *c; return &job }
func (c *CodeVictorOpsJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeVictorOpsJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeVictorOpsJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeVictorOpsJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeVictorOpsJob) IsNil() bool                { return c == nil }
